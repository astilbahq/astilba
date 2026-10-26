package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func noActiveUpgrade(ctx context.Context, tx pgx.Tx, server uuid.UUID) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supported_upgrades WHERE server_id=$1 AND status->>'phase' NOT IN ('planned','rejected','complete','returned'))`, server).Scan(&active)
	if err == nil && active {
		return domain.Conflict
	}
	return err
}
func (s *Service) Upgrades(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]upgrade.View, error) {
	out := []upgrade.View{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		if _, err := loadVisualEnvironment(ctx, tx, ws, pid, env); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, "SELECT plan,status FROM supported_upgrades WHERE workspace_id=$1 AND environment_id=$2 ORDER BY id LIMIT 100", ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var plan, status []byte
			var v upgrade.View
			if err = rows.Scan(&plan, &status); err != nil {
				return err
			}
			if err = json.Unmarshal(plan, &v.Plan); err != nil {
				return err
			}
			if err = json.Unmarshal(status, &v.Status); err != nil {
				return err
			}
			out = append(out, v)
		}
		return rows.Err()
	})
	return out, err
}
func (s *Service) CreateUpgrade(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, in api.UpgradeRequest) (upgrade.View, error) {
	var out upgrade.View
	err := s.scope(ctx, p, ws, true, true, true, "upgrade.plan", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Server == nil || v.Applied == nil || *v.Applied != in.BaseRevisionId || v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		if err = s.deploymentServer(ctx, tx, ws, *v.Server); err != nil {
			return err
		}
		if err = noActiveUpgrade(ctx, tx, *v.Server); err != nil {
			return err
		}
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT capabilities FROM servers WHERE id=$1", *v.Server).Scan(&raw); err != nil {
			return err
		}
		var cap api.AgentCapabilities
		if json.Unmarshal(raw, &cap) != nil || cap.Upgrades == nil || !*cap.Upgrades {
			return domain.Invalid("server.capabilities.upgrades")
		}
		policy, err := loadBackupPolicy(ctx, tx, ws, env, in.PolicyId)
		if err != nil {
			return err
		}
		var planID uuid.UUID
		if err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&planID); err != nil {
			return err
		}
		source, err := readPlan(ctx, tx, ws, env, planID)
		if err != nil {
			return err
		}
		if err = requireServiceOutputs(ctx, tx, ws, *v.Server, source.Specification); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, *v.Server, source); err != nil {
			return err
		}
		if policy.Plan.Hash != source.Hash || policy.ResourceID != in.ResourceId || policy.Method != "postgres-logical" {
			return domain.Invalid("policy")
		}
		out.Plan = upgrade.Plan{ID: uuid.New(), Recipe: upgrade.Recipe, PolicyID: policy.ID, ResourceID: in.ResourceId, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: in.RetainDays, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Source: source}
		if out.Plan.Validate() != nil {
			return domain.Invalid("upgrade.source")
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM supported_upgrades WHERE workspace_id=$1", ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		out.Status = upgrade.Status{Phase: "planned", Serving: "old", KeepUntil: out.Plan.ExpiresAt, Note: "Review downtime, capacity, backup destination and recovery boundary before starting."}
		raw, _ = json.Marshal(out.Plan)
		status, _ := json.Marshal(out.Status)
		_, err = tx.Exec(ctx, "INSERT INTO supported_upgrades(id,workspace_id,environment_id,server_id,plan,status,author_id) VALUES($1,$2,$3,$4,$5,$6,$7)", out.Plan.ID, ws, env, source.ServerID, raw, status, p.Account.ID)
		if err != nil {
			return err
		}
		target := out.Plan.Target()
		var initial string
		for _, a := range target.Specification.Applications {
			if a.ID == out.Plan.ResourceID {
				initial = deployment.Hash(a.Postgres)
			}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_init_hash) VALUES($1,$2,$3,$4,$5)", ws, env, source.UnitID, out.Plan.TargetVolumeID, initial); err != nil {
			return err
		}
		return nil
	})
	return out, err
}
func (s *Service) UpgradeAction(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.UpgradeActionRequest) (api.Operation, error) {
	var out api.Operation
	action := "upgrade.action"
	if in.Action == "return" || in.Action == "reconcile" {
		action = "upgrade.recover"
	}
	err := s.scope(ctx, p, ws, true, true, true, action, id, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		var planRaw, statusRaw []byte
		if err = tx.QueryRow(ctx, "SELECT plan,status FROM supported_upgrades WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, id).Scan(&planRaw, &statusRaw); err != nil {
			return err
		}
		var plan upgrade.Plan
		var status upgrade.Status
		if json.Unmarshal(planRaw, &plan) != nil || json.Unmarshal(statusRaw, &status) != nil {
			return domain.Conflict
		}
		if err = s.deploymentServer(ctx, tx, ws, plan.Source.ServerID); err != nil {
			return err
		}
		if err = requireReplicas(ctx, tx, ws, plan.Source.ServerID, plan.Source); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, "SELECT status FROM supported_upgrades WHERE id=$1 FOR UPDATE", id).Scan(&statusRaw); err != nil {
			return err
		}
		if err = json.Unmarshal(statusRaw, &status); err != nil {
			return err
		}
		var prior []byte
		err = tx.QueryRow(ctx, "SELECT command FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, in.RequestKey).Scan(&prior)
		if err == nil {
			var c api.ExecutionCommand
			var cmd upgrade.Command
			if json.Unmarshal(prior, &c) != nil || c.Kind != "upgrade" || c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Plan.ID != id || cmd.Action != string(in.Action) {
				return domain.Conflict
			}
			out, err = scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, in.RequestKey))
			return err
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if status.Phase != in.ExpectedPhase || !status.Allows(string(in.Action)) {
			return domain.Conflict
		}
		var active bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))", plan.Source.ServerID).Scan(&active); err != nil {
			return err
		}
		if active {
			return domain.Conflict
		}
		if in.Action == "prepare" {
			if err = noActiveUpgrade(ctx, tx, plan.Source.ServerID); err != nil {
				return err
			}
			if time.Now().After(plan.ExpiresAt) || v.Applied == nil || *v.Applied != plan.Source.RevisionID {
				return domain.Conflict
			}
			var current uuid.UUID
			if err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&current); err != nil {
				return err
			}
			if current != plan.Source.ID {
				return domain.Conflict
			}
		}
		policy, err := loadBackupPolicy(ctx, tx, ws, env, plan.PolicyID)
		if err != nil {
			return err
		}
		if policy.Plan.Hash != plan.Source.Hash {
			return domain.Conflict
		}
		var dest backup.Destination
		if err = tx.QueryRow(ctx, "SELECT configuration FROM backup_destinations WHERE workspace_id=$1 AND id=$2", ws, policy.DestinationID).Scan(&prior); err != nil {
			return err
		}
		if err = json.Unmarshal(prior, &dest); err != nil {
			return err
		}
		cmd := upgrade.Command{Action: string(in.Action), Plan: plan, Installation: backup.Installation{Policy: policy, Destination: dest}}
		raw, _ := json.Marshal(cmd)
		var generation int64
		if err = tx.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation", ws, env).Scan(&generation); err != nil {
			return err
		}
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.Source.ServerID, EnvironmentId: env, Generation: generation, PlanHash: cmd.Digest(), ResourceId: id, Kind: "upgrade", Action: "inspect", Deadline: time.Now().Add(2 * time.Hour), Upgrade: rawMessage(raw)}
		out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c)
		if err != nil {
			return err
		}
		status.Phase = "running"
		status.Note = "Upgrade action queued: " + string(in.Action)
		raw, _ = json.Marshal(status)
		_, err = tx.Exec(ctx, "UPDATE supported_upgrades SET status=$2 WHERE id=$1", id, raw)
		return err
	})
	return out, err
}
func (s *Service) hydrateUpgrade(ctx context.Context, c *api.ExecutionCommand) error {
	var cmd upgrade.Command
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Digest() != c.PlanHash {
		return domain.Conflict
	}
	// Reuse the backup credential boundary and the visual secret boundary; only
	// the assigned server receives either set of execution values.
	b := backup.Command{Action: "check", Installation: cmd.Installation}
	raw, _ := json.Marshal(b)
	copy := *c
	copy.Kind = "backup"
	if cmd.Installation.Policy.GeneralSchedule() {
		copy.Kind = "schedule"
	}
	copy.ResourceId = cmd.Plan.PolicyID
	copy.PlanHash = b.Digest()
	copy.Backup = rawMessage(raw)
	if err := s.hydrateBackup(ctx, &copy); err != nil {
		return err
	}
	if err := json.Unmarshal(*copy.Backup, &b); err != nil {
		return err
	}
	cmd.Installation = b.Installation
	copy = *c
	payload := deployment.Payload{Action: "deploy", Plan: cmd.Plan.Source}
	raw, _ = json.Marshal(payload)
	copy.Visual = rawMessage(raw)
	if err := s.hydrateVisual(ctx, &copy); err != nil {
		return err
	}
	if err := json.Unmarshal(*copy.Visual, &payload); err != nil {
		return err
	}
	cmd.Secrets = payload.Secrets
	raw, _ = json.Marshal(cmd)
	if len(raw) > 450000 {
		return errors.New("upgrade payload too large")
	}
	c.Upgrade = rawMessage(raw)
	return nil
}
func (s *Service) upgradeReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if r.Upgrade == nil {
		if Terminal(r.State) {
			return domain.Invalid("upgrade")
		}
		return nil
	}
	var cmd upgrade.Command
	var next, old upgrade.Status
	var raw []byte
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Digest() != c.PlanHash || json.Unmarshal(*r.Upgrade, &next) != nil {
		return domain.Invalid("upgrade")
	}
	if cmd.Plan.Source.ServerID != c.ServerId || cmd.Plan.Source.EnvironmentID != c.EnvironmentId || cmd.Plan.ID != c.ResourceId {
		return domain.Forbidden
	}
	if err := tx.QueryRow(ctx, "SELECT status FROM supported_upgrades WHERE id=$1 AND server_id=$2 FOR UPDATE", c.ResourceId, c.ServerId).Scan(&raw); err != nil {
		return err
	}
	if json.Unmarshal(raw, &old) != nil {
		return domain.Conflict
	}
	if old.WritesPossible && !next.WritesPossible || next.Phase == "returned" && next.WritesPossible || next.Phase == "ready" && (next.WritesPossible || !backup.SnapshotID(next.Snapshot)) || next.Phase == "complete" && (!next.WritesPossible || next.Serving != "new") || len(next.Note) > 1024 {
		return domain.Invalid("upgrade.status")
	}
	if next.Phase == "complete" || next.Phase == "returned" {
		if r.State != "succeeded" || r.Observation == nil {
			return domain.Invalid("upgrade.observation")
		}
		var observed deployment.Observation
		if json.Unmarshal(*r.Observation, &observed) != nil || !observed.Applied {
			return domain.Invalid("upgrade.observation")
		}

		plan := cmd.Plan.Target()
		if next.Phase == "complete" {
			if err := s.saveUpgradeTarget(ctx, tx, cmd, c.Id); err != nil {
				return err
			}
		}
		if next.Phase == "returned" {
			plan = cmd.Plan.Source
		}
		payload := deployment.Payload{Action: "deploy", Plan: plan}
		body, _ := json.Marshal(payload)
		copy := c
		copy.Kind = "visual"
		copy.PlanHash = payload.Digest()
		copy.Visual = rawMessage(body)
		if err := s.visualReport(ctx, tx, copy, r); err != nil {
			return err
		}
		if next.Phase == "complete" {
			if _, err := tx.Exec(ctx, "UPDATE execution_operations SET visual_plan_id=$2 WHERE id=$1", c.Id, plan.ID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, "UPDATE environments SET draft_revision_id=$4 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3", cmd.Installation.Policy.WorkspaceID, c.EnvironmentId, cmd.Plan.Source.RevisionID, plan.RevisionID); err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE backup_policies SET configuration=jsonb_set(configuration,'{enabled}','false') WHERE workspace_id=$1 AND environment_id=$2`, cmd.Installation.Policy.WorkspaceID, c.EnvironmentId); err != nil {
				return err
			}
		}
	}
	raw, _ = json.Marshal(next)
	_, err := tx.Exec(ctx, "UPDATE supported_upgrades SET status=$2 WHERE id=$1", c.ResourceId, raw)
	return err
}

func (s *Service) saveUpgradeTarget(ctx context.Context, tx pgx.Tx, cmd upgrade.Command, operation uuid.UUID) error {
	target := cmd.Plan.Target()
	ws := cmd.Installation.Policy.WorkspaceID
	env := target.EnvironmentID
	var author uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT author_id FROM supported_upgrades WHERE id=$1", cmd.Plan.ID).Scan(&author); err != nil {
		return err
	}
	raw, _ := json.Marshal(target.Specification)
	_, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(canonical)
	if _, err = db.New(tx).InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: target.RevisionID, AuthorID: author, Specification: canonical, ContentHash: hash[:]}); err != nil {
		return err
	}
	raw, _ = json.Marshal(target)
	if _, err = tx.Exec(ctx, "INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,candidate,operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", target.ID, ws, target.ServerID, env, target.UnitID, target.RevisionID, raw, operation); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, "INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)", target.ID, ws, env, raw)
	return err
}
