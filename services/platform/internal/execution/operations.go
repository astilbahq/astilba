package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func (s *Service) Plan(ctx context.Context, p store.Principal, ws uuid.UUID, in api.PlanRequest) (api.ExecutionPlan, error) {
	plan := api.ExecutionPlan{Id: uuid.New(), ServerId: in.ServerId, EnvironmentId: in.EnvironmentId, Action: in.Action, Image: TestImage, ExpiresAt: time.Now().Add(10 * time.Minute)}
	plan.ResourceId = uuid.NewSHA1(in.ServerId, []byte(in.EnvironmentId.String()+":probe:v1"))
	if in.Action != "start" && in.Action != "inspect" && in.Action != "remove" {
		return plan, domain.Invalid("action")
	}
	err := s.scope(ctx, p, ws, true, false, false, "operation.plan", plan.Id, func(tx pgx.Tx) error {
		if err := s.deploymentServer(ctx, tx, ws, in.ServerId); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT draft_revision_id FROM environments WHERE workspace_id=$1 AND id=$2", ws, in.EnvironmentId).Scan(&plan.RevisionId); err != nil {
			return err
		}
		plan.Hash = Hash(struct {
			Server, Env, Revision, Resource uuid.UUID
			Action                          api.TestAction
			Image                           string
		}{plan.ServerId, plan.EnvironmentId, plan.RevisionId, plan.ResourceId, plan.Action, plan.Image})
		_, err := tx.Exec(ctx, `INSERT INTO execution_plans(id,workspace_id,server_id,environment_id,revision_id,action,resource_id,image,hash,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, plan.Id, ws, plan.ServerId, plan.EnvironmentId, plan.RevisionId, plan.Action, plan.ResourceId, plan.Image, plan.Hash, plan.ExpiresAt)
		return err
	})
	return plan, err
}
func (s *Service) deploymentServer(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) error {
	var role string
	var cap api.AgentCapabilities
	var pre api.HostPreflight
	var cb, pb []byte
	err := tx.QueryRow(ctx, `SELECT role,capabilities,preflight FROM servers WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND NOT awaiting_transfer AND credential_hash IS NOT NULL FOR UPDATE`, ws, id).Scan(&role, &cb, &pb)
	if err != nil {
		return err
	}
	if role == "build" {
		return domain.Forbidden
	}
	if json.Unmarshal(cb, &cap) != nil || json.Unmarshal(pb, &pre) != nil || cap.Protocol != 1 || !cap.Docker || !pre.Ready {
		return domain.Invalid("server")
	}
	return nil
}

const opColumns = `id,server_id,COALESCE(environment_id,'00000000-0000-0000-0000-000000000000'),generation,state,kind,cancel_requested,note,steps,created_at`

func scanOperation(row pgx.Row) (api.Operation, error) {
	var v api.Operation
	var b []byte
	err := row.Scan(&v.Id, &v.ServerId, &v.EnvironmentId, &v.Generation, &v.State, &v.Kind, &v.CancelRequested, &v.Note, &b, &v.CreatedAt)
	if err == nil {
		err = json.Unmarshal(b, &v.Steps)
	}
	return v, err
}
func (s *Service) Apply(ctx context.Context, p store.Principal, ws, planID, key uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, true, false, false, "operation.apply", planID, func(tx pgx.Tx) error {
		var existingPlan *uuid.UUID
		err := tx.QueryRow(ctx, "SELECT plan_id FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, key).Scan(&existingPlan)
		if err == nil {
			if existingPlan == nil || *existingPlan != planID {
				return domain.Conflict
			}
			out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, key))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		var plan api.ExecutionPlan
		err = tx.QueryRow(ctx, `SELECT id,server_id,environment_id,revision_id,action,resource_id,image,hash,expires_at FROM execution_plans WHERE workspace_id=$1 AND id=$2`, ws, planID).Scan(&plan.Id, &plan.ServerId, &plan.EnvironmentId, &plan.RevisionId, &plan.Action, &plan.ResourceId, &plan.Image, &plan.Hash, &plan.ExpiresAt)
		if err != nil {
			return err
		}
		if time.Now().After(plan.ExpiresAt) {
			return domain.Conflict
		}
		if err = s.deploymentServer(ctx, tx, ws, plan.ServerId); err != nil {
			return err
		}
		var generation int64
		err = tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3 RETURNING execution_generation`, ws, plan.EnvironmentId, plan.RevisionId).Scan(&generation)
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Conflict
		}
		if err != nil {
			return err
		}
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.ServerId, EnvironmentId: plan.EnvironmentId, Generation: generation, PlanHash: plan.Hash, Kind: "test", Action: plan.Action, ResourceId: plan.ResourceId, Image: plan.Image, Deadline: time.Now().Add(30 * time.Minute)}
		out, err = s.insert(ctx, tx, ws, key, &planID, c)
		return err
	})
	return out, err
}
func (s *Service) insert(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, planID *uuid.UUID, c api.ExecutionCommand) (api.Operation, error) {
	var out api.Operation
	var controller int64
	var awaiting bool
	if e := tx.QueryRow(ctx, "SELECT controller_generation,awaiting_transfer FROM servers WHERE workspace_id=$1 AND id=$2", ws, c.ServerId).Scan(&controller, &awaiting); e != nil {
		return out, e
	}
	if awaiting {
		return out, domain.Conflict
	}
	if controller > 0 {
		c.ControllerGeneration = &controller
	}
	if err := s.commandAdmission(ctx, tx, ws, c); err != nil {
		return out, err
	}
	var pending int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled')", ws).Scan(&pending); err != nil {
		return out, err
	}
	if pending >= 50 {
		return out, domain.RateLimited
	}
	if c.Kind != "upgrade" {
		if err := noActiveUpgrade(ctx, tx, c.ServerId); err != nil {
			return out, err
		}
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return out, err
	}
	var env *uuid.UUID
	if c.EnvironmentId != uuid.Nil {
		env = &c.EnvironmentId
	}
	_, err = tx.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,plan_id,request_key,command,kind,state) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'queued')`, c.Id, ws, c.ServerId, env, c.Generation, planID, key, raw, c.Kind)
	if err != nil {
		return out, err
	}
	job, err := s.Queue.InsertTx(ctx, tx, jobArgs{ID: c.Id}, &river.InsertOpts{MaxAttempts: 100})
	if err != nil {
		return out, err
	}
	if _, err = tx.Exec(ctx, "UPDATE execution_operations SET job_id=$2 WHERE id=$1", c.Id, job.Job.ID); err != nil {
		return out, err
	}
	return scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE id=$1", c.Id))
}
func (s *Service) Operations(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.Operation, error) {
	out := []api.Operation{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			v, e := scanOperation(rows)
			if e != nil {
				return e
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	if err == nil {
		for i := range out {
			if !Terminal(out[i].State) && out[i].State != "needs_attention" {
				var stale bool
				err = s.Store.Pool.QueryRow(ctx, "SELECT last_seen IS NULL OR last_seen<now()-interval '45 seconds' FROM servers WHERE id=$1", out[i].ServerId).Scan(&stale)
				if err != nil {
					return out, err
				}
				if stale {
					out[i].State = "waiting_for_agent"
				}
			}
		}
	}
	return out, err
}
func (s *Service) Cancel(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.scope(ctx, p, ws, true, false, false, "operation.cancel", id, func(tx pgx.Tx) error {
		n, e := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true,state=CASE WHEN kind='pipeline' AND state='queued' THEN 'cancelled' ELSE state END WHERE workspace_id=$1 AND id=$2 AND state NOT IN ('succeeded','failed','cancelled')`, ws, id)
		if e == nil && n.RowsAffected() != 1 {
			return domain.Conflict
		}
		return e
	})
}
func (s *Service) RequestUpdate(ctx context.Context, p store.Principal, ws, id uuid.UUID, version string) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, true, true, true, "server.update", id, func(tx pgx.Tx) error {
		var cap api.AgentCapabilities
		var raw []byte
		var generation int64
		err := tx.QueryRow(ctx, `UPDATE servers SET update_generation=update_generation+1 WHERE workspace_id=$1 AND id=$2 AND NOT revoked AND credential_hash IS NOT NULL RETURNING capabilities,update_generation`, ws, id).Scan(&raw, &generation)
		if err != nil {
			return err
		}
		if json.Unmarshal(raw, &cap) != nil || cap.Protocol != 1 {
			return domain.Conflict
		}
		var release *api.AgentRelease
		for i := range s.Releases {
			r := &s.Releases[i]
			if r.Version == version && string(r.Arch) == string(cap.Arch) && r.Protocol == 1 {
				release = r
				break
			}
		}
		if release == nil || release.Version == cap.Version {
			return domain.Invalid("version")
		}
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: id, Generation: generation, Kind: "update", Release: release, Action: "inspect", Deadline: time.Now().Add(30 * time.Minute)}
		c.PlanHash = Hash(release)
		out, err = s.insert(ctx, tx, ws, uuid.New(), nil, c)
		return err
	})
	return out, err
}

type jobArgs struct {
	ID uuid.UUID `json:"id"`
}

func (jobArgs) Kind() string { return "platform_execution_v1" }

type worker struct {
	river.WorkerDefaults[jobArgs]
	service *Service
}

func (w *worker) Work(ctx context.Context, job *river.Job[jobArgs]) error {
	tx, err := w.service.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ws uuid.UUID
	if err = tx.QueryRow(ctx, "SELECT workspace_id FROM execution_operations WHERE id=$1", job.Args.ID).Scan(&ws); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", ws); err != nil {
		return err
	}
	var server uuid.UUID
	if err = tx.QueryRow(ctx, "SELECT s.id FROM servers s JOIN execution_operations o ON o.server_id=s.id WHERE o.id=$1 FOR UPDATE OF s", job.Args.ID).Scan(&server); err != nil {
		return err
	}
	var state api.ExecutionState
	var raw []byte
	var cancelled bool
	if err = tx.QueryRow(ctx, "SELECT state,command,cancel_requested FROM execution_operations WHERE id=$1 FOR UPDATE", job.Args.ID).Scan(&state, &raw, &cancelled); err != nil {
		return err
	}
	if Terminal(state) {
		if _, err = river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	var c api.ExecutionCommand
	if err = json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if e := w.service.commandAdmission(ctx, tx, ws, c); e != nil {
		if !errors.Is(e, store.SubscriptionRequired) {
			return e
		}
		if state == "queued" {
			_, err = tx.Exec(ctx, "UPDATE execution_operations SET state='cancelled',cancel_requested=true,note='Subscription expired before dispatch.' WHERE id=$1", c.Id)
		} else {
			_, err = tx.Exec(ctx, "UPDATE execution_operations SET cancel_requested=true WHERE id=$1", c.Id)
		}
		if err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if c.Kind == "pipeline" && state == "queued" {
		if cancelled || time.Now().After(c.Deadline) {
			if _, err = tx.Exec(ctx, "UPDATE execution_operations SET state='cancelled',note='Cancelled or expired before dispatch.' WHERE id=$1", c.Id); err != nil {
				return err
			}
			return tx.Commit(ctx)
		}
		var ready bool
		if err = tx.QueryRow(ctx, "SELECT NOT revoked AND COALESCE(last_seen>now()-interval '45 seconds',false) AND NOT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND id<>$2 AND state NOT IN ('succeeded','failed','cancelled') AND (state<>'queued' OR kind<>'pipeline')) FROM servers WHERE id=$1", server, c.Id).Scan(&ready); err != nil {
			return err
		}
		if !ready {
			if err = tx.Commit(ctx); err != nil {
				return err
			}
			return river.JobSnooze(2 * time.Second)
		}
	}
	// Dispatch is durable and idempotent; losing the worker cannot mint a new command or generation.
	if state == "queued" {
		if _, err = tx.Exec(ctx, "UPDATE execution_operations SET state='dispatched' WHERE id=$1", job.Args.ID); err != nil {
			return err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return river.JobSnooze(2 * time.Second)
}
