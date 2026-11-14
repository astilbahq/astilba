package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func noActiveUpgrade(ctx context.Context, tx pgx.Tx, server uuid.UUID) error {
	return noOtherUpgrade(ctx, tx, server, uuid.Nil)
}
func noOtherUpgrade(ctx context.Context, tx pgx.Tx, server, allowed uuid.UUID) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supported_upgrades WHERE server_id=$1 AND id<>$2 AND status->>'phase' NOT IN ('planned','rejected','complete','returned'))`, server, allowed).Scan(&active)
	if err == nil && active {
		return domain.Conflict
	}
	return err
}
func (s *Service) Upgrades(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]upgrade.View, error) {
	out := []upgrade.View{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
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
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "upgrade.plan", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if err = requireActive(v.State); err != nil {
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
		var group *deployment.Plan
		if source.Distributed != nil {
			frozen := source
			group = &frozen
			source = source.PrimaryPlan()
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
		out.Plan = upgrade.Plan{Group: group, ID: uuid.New(), Recipe: upgrade.Recipe, PolicyID: policy.ID, ResourceID: in.ResourceId, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: in.RetainDays, ExpiresAt: time.Now().UTC().Add(10 * time.Minute), Source: source}
		if out.Plan.Validate() != nil {
			return domain.Invalid("upgrade.source")
		}
		// The target must be storable before the host changes: completion has no way to refuse it.
		stored, err := upgradeTargetSpecification(out.Plan.TargetGroup())
		if err != nil {
			return err
		}
		fits, err := store.SpecificationFits(ctx, db.New(tx), stored)
		if err != nil {
			return err
		}
		if !fits {
			return domain.SpecificationTooLarge
		}
		var count int
		if err = tx.QueryRow(ctx, "SELECT count(*) FROM supported_upgrades WHERE workspace_id=$1", ws).Scan(&count); err != nil {
			return err
		}
		if count >= 100 {
			return domain.Conflict
		}
		out.Status = upgrade.Status{Phase: "planned", Serving: "old", KeepUntil: out.Plan.ExpiresAt, Note: "Review downtime, capacity, backup destination and recovery boundary before starting."}
		raw, _ = stablejson.Marshal(out.Plan)
		status, _ := stablejson.Marshal(out.Status)
		_, err = tx.Exec(ctx, "INSERT INTO supported_upgrades(id,workspace_id,environment_id,server_id,plan,status,author_id) VALUES($1,$2,$3,$4,$5,$6,$7)", out.Plan.ID, ws, env, source.ServerID, raw, status, p.Account.ID)
		if err != nil {
			return err
		}
		target := out.Plan.Target()
		var initial string
		var major *string
		for _, a := range target.Specification.Applications {
			if a.ID == out.Plan.ResourceID {
				initial = deployment.Hash(a.Postgres)
				if version := domain.PostgresMajor(*a.Source.Image); version != "" {
					major = &version
				}
			}
		}
		if _, err = tx.Exec(ctx, "INSERT INTO managed_volumes(workspace_id,environment_id,unit_id,id,postgres_init_hash,postgres_major) VALUES($1,$2,$3,$4,$5,$6)", ws, env, source.UnitID, out.Plan.TargetVolumeID, initial, major); err != nil {
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
	// Planning hydrates the upgrade once, to refuse an effect too large or incompatible before
	// anything is reserved: an inspection, for the owner, asked for in the pass that reaches it.
	keys := s.passKeys(secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: p.Secrets(), Kinds: []secretbroker.Kind{secretbroker.SecretVersion, secretbroker.Registry, secretbroker.BackupPolicy, secretbroker.BackupDestination}})
	defer keys.close(ctx)
	err := withProviderCalls(ctx, func() error {
		return s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: in.Action == "return" || in.Action == "reconcile"}, action, id, func(tx pgx.Tx) error {
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
			if plan.Group != nil && in.Action != "prepare" {
				out, err = s.approveMaintenanceUpgrade(ctx, tx, ws, plan, status, in)
				return err
			}
			var prior []byte
			err = tx.QueryRow(ctx, "SELECT command FROM execution_operations WHERE workspace_id=$1 AND request_key=$2", ws, in.RequestKey).Scan(&prior)
			if err == nil {
				var c api.ExecutionCommand
				var cmd upgrade.Command
				if json.Unmarshal(prior, &c) != nil {
					return domain.Conflict
				}
				if c.Kind == "crosshost" && c.Crosshost != nil {
					var parent coordinationCommand
					if json.Unmarshal(*c.Crosshost, &parent) != nil || parent.Maintenance == nil {
						return domain.Conflict
					}
					c = parent.Maintenance.Effect.command("")
				}
				if c.Kind != "upgrade" || c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Plan.ID != id || cmd.Action != string(in.Action) {
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
			if err = tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state IN ('queued','dispatched','running','needs_attention'))", plan.Source.ServerID).Scan(&active); err != nil {
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
				if current != plan.Source.VisualPlanID() {
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
			if prior, err = db.New(tx).BackupDestinationConfiguration(ctx, db.BackupDestinationConfigurationParams{WorkspaceID: ws, ID: policy.DestinationID}); err != nil {
				return err
			}
			if err = json.Unmarshal(prior, &dest); err != nil {
				return err
			}
			cmd := upgrade.Command{Action: string(in.Action), Plan: plan, Installation: backup.Installation{Policy: policy, Destination: dest}}
			raw, _ := stablejson.Marshal(cmd)
			var generation int64
			if err = tx.QueryRow(ctx, "UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation", ws, env).Scan(&generation); err != nil {
				return err
			}
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: plan.Source.ServerID, EnvironmentId: env, Generation: generation, PlanHash: cmd.Digest(), ResourceId: id, Kind: "upgrade", Action: "inspect", Deadline: time.Now().Add(2 * time.Hour), Upgrade: rawMessage(raw)}
			if plan.Group != nil {
				// Hydrate once before any writer reservation so an oversized or
				// incompatible effect cannot strand a paused deployment.
				if err = s.hydrateUpgrade(ctx, keys, &c); err != nil {
					return err
				}
				out, err = s.insertUpgradeMaintenance(ctx, tx, ws, in.RequestKey, cmd)
			} else {
				out, err = s.insert(ctx, tx, ws, in.RequestKey, nil, c)
			}
			if err != nil {
				return err
			}
			status.Phase = "running"
			status.Note = "Upgrade action queued: " + string(in.Action)
			raw, _ = stablejson.Marshal(status)
			_, err = tx.Exec(ctx, "UPDATE supported_upgrades SET status=$2 WHERE id=$1", id, raw)
			return err
		})
	})
	return operationFor(p, out), err
}

// hydrateUpgrade fills an upgrade's backup access and secrets. With nil keys it makes the
// delivery's checks and opens nothing.
func (s *Service) hydrateUpgrade(ctx context.Context, keys valueKeys, c *api.ExecutionCommand) error {
	var cmd upgrade.Command
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Digest() != c.PlanHash {
		return domain.Conflict
	}
	// Reuse the backup credential boundary and the visual secret boundary; only
	// the assigned server receives either set of execution values.
	b := backup.Command{Action: "check", Installation: cmd.Installation}
	raw, _ := stablejson.Marshal(b)
	copy := *c
	copy.Kind = "backup"
	if cmd.Installation.Policy.GeneralSchedule() {
		copy.Kind = "schedule"
	}
	copy.ResourceId = cmd.Plan.PolicyID
	copy.PlanHash = b.Digest()
	copy.Backup = rawMessage(raw)
	if err := s.hydrateBackup(ctx, keys, &copy); err != nil {
		return err
	}
	if keys != nil {
		if err := json.Unmarshal(*copy.Backup, &b); err != nil {
			return err
		}
		cmd.Installation = b.Installation
	}
	copy = *c
	payload := deployment.Payload{Action: "deploy", Plan: cmd.Plan.Source}
	raw, _ = stablejson.Marshal(payload)
	copy.Visual = rawMessage(raw)
	if err := s.hydrateVisual(ctx, keys, &copy); err != nil {
		return err
	}
	if keys == nil {
		return nil
	}
	if err := json.Unmarshal(*copy.Visual, &payload); err != nil {
		return err
	}
	cmd.Secrets = payload.Secrets
	raw, _ = stablejson.Marshal(cmd)
	if len(raw) > 450000 {
		return errors.New("upgrade payload too large")
	}
	c.Upgrade = rawMessage(raw)
	return nil
}

// refusedUpgrade settles a plan whose command ended without a status. Agents do that
// when they cannot read or trust the plan, normally before admitting the command;
// steps show this attempt was admitted earlier. Only a prepare starts from a host with
// nothing to recover. WritesPossible stays as last reported: the controller can never
// lower it again, and the agent's own record refuses a return after new writes.
func refusedUpgrade(status upgrade.Status, action string, r *api.AgentReport) upgrade.Status {
	if action == "prepare" && len(r.Steps) == 0 {
		status.Phase = "rejected"
		status.Note = "The agent refused this upgrade before changing the host. The operation's note says why."
		return status
	}
	status.Phase = "attention"
	if len(r.Steps) > 0 {
		status.Serving = "unknown"
	}
	status.Note = "The agent ended this upgrade action without reporting an outcome. Inspect the host and the operation's note before choosing a recovery action."
	return status
}

// undeliveredUpgrade settles the plan of a command that will never reach the host,
// returning it to the phase the host still holds. Return and reconcile can start from
// more than one phase; attention keeps the recovery each one allows.
func undeliveredUpgrade(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	var cmd upgrade.Command
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil {
		return domain.Invalid("upgrade")
	}
	q := db.New(tx)
	raw, err := q.SupportedUpgradeStatus(ctx, db.SupportedUpgradeStatusParams{WorkspaceID: ws, ID: c.ResourceId})
	if err != nil {
		return err
	}
	var status upgrade.Status
	if json.Unmarshal(raw, &status) != nil {
		return domain.Conflict
	}
	if status.Phase != "running" {
		return nil
	}
	switch cmd.Action {
	case "prepare":
		status.Phase = "rejected"
	case "cutover":
		status.Phase = "ready"
	default:
		status.Phase = "attention"
	}
	status.Note = "This upgrade action stopped before changing anything on the host."
	raw, _ = stablejson.Marshal(status)
	return q.SaveSupportedUpgradeStatus(ctx, db.SaveSupportedUpgradeStatusParams{WorkspaceID: ws, ID: c.ResourceId, Status: raw})
}
func (s *Service) upgradeReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if r.Upgrade == nil && !Terminal(r.State) {
		return nil
	}
	var cmd upgrade.Command
	var next, old upgrade.Status
	var raw []byte
	if c.Upgrade == nil || json.Unmarshal(*c.Upgrade, &cmd) != nil || cmd.Digest() != c.PlanHash || r.Upgrade != nil && json.Unmarshal(*r.Upgrade, &next) != nil {
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
	if r.Upgrade == nil {
		// Success always states its outcome, and only a plan still at running awaits one.
		if r.State == "succeeded" {
			return domain.Invalid("upgrade")
		}
		if old.Phase != "running" {
			return nil
		}
		next = refusedUpgrade(old, cmd.Action, r)
	} else if old.WritesPossible && !next.WritesPossible || next.Phase == "returned" && next.WritesPossible || next.Phase == "ready" && (next.WritesPossible || !backup.SnapshotID(next.Snapshot)) || next.Phase == "complete" && (!next.WritesPossible || next.Serving != "new") || len(next.Note) > 1024 {
		return domain.Invalid("upgrade.status")
	}
	if (next.Phase == "complete" || next.Phase == "returned") && c.ParentId != nil {
		var observed deployment.Observation
		plan := cmd.Plan.Target()
		if next.Phase == "returned" {
			plan = cmd.Plan.Source
		}
		if cmd.Plan.Group == nil || r.State != "succeeded" || r.Observation == nil || json.Unmarshal(*r.Observation, &observed) != nil || !observed.Applied || observed.InfrastructureDrift || observed.ResourceDrift || plan.ValidateMaintenanceObservation(observed) != nil {
			return domain.Invalid("upgrade.observation")
		}
		for _, resource := range observed.Resources {
			if resource.ID == cmd.Plan.ResourceID && resource.State != "running" && resource.State != "healthy" {
				return domain.Invalid("upgrade.database")
			}
		}
		next.Note = "The primary database outcome is confirmed. Coordinated writer recovery remains pending."
	}
	if (next.Phase == "complete" || next.Phase == "returned") && c.ParentId == nil {
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
		body, _ := stablejson.Marshal(payload)
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
		}
	}
	raw, _ = stablejson.Marshal(next)
	_, err := tx.Exec(ctx, "UPDATE supported_upgrades SET status=$2 WHERE id=$1", c.ResourceId, raw)
	return err
}

// upgradeTargetSpecification is the canonical specification an upgrade stores as its target
// revision when it completes.
func upgradeTargetSpecification(target deployment.Plan) ([]byte, error) {
	raw, _ := stablejson.Marshal(target.Specification)
	_, canonical, err := domain.ParseSpec(raw)
	return canonical, err
}

// saveUpgradeTarget records the target revision once the host serves it, so a refusal here would
// leave the upgrade unrecordable. CreateUpgrade refuses the plan up front instead.
func (s *Service) saveUpgradeTarget(ctx context.Context, tx pgx.Tx, cmd upgrade.Command, operation uuid.UUID) error {
	target := cmd.Plan.TargetGroup()
	ws := cmd.Installation.Policy.WorkspaceID
	env := target.EnvironmentID
	var author uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT author_id FROM supported_upgrades WHERE id=$1", cmd.Plan.ID).Scan(&author); err != nil {
		return err
	}
	canonical, err := upgradeTargetSpecification(target)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(canonical)
	if _, err = store.InsertRevision(ctx, db.New(tx), db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: target.RevisionID, AuthorID: author, Specification: canonical, ContentHash: hash[:]}); err != nil {
		return err
	}
	raw, _ := stablejson.Marshal(target)
	if _, err = tx.Exec(ctx, "INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,candidate,operation_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", target.ID, ws, target.ServerID, env, target.UnitID, target.RevisionID, raw, operation); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)", target.ID, ws, env, raw); err != nil {
		return err
	}
	if cmd.Plan.Group != nil {
		if err = copyNetworkPlanAssociations(ctx, tx, ws, cmd.Plan.Group.ID, target.ID); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(ctx, "UPDATE environments SET draft_revision_id=$4 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3", ws, env, cmd.Plan.Source.RevisionID, target.RevisionID); err != nil {
		return err
	}
	return db.New(tx).DisableEnvironmentBackupPolicies(ctx, db.DisableEnvironmentBackupPoliciesParams{WorkspaceID: ws, EnvironmentID: env})
}
