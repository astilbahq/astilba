package execution

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

type maintenanceUnit struct {
	Unit   deployment.DistributedUnit `json:"unit"`
	Intent deployment.Maintenance     `json:"intent"`
}
type coordinatedMaintenance struct {
	Restore *deployment.Plan  `json:"restore,omitempty"`
	Effect  maintenanceEffect `json:"effect"`
	Units   []maintenanceUnit `json:"units"`
}

// Typed effects keep the parent's digest stable across JSONB normalization.
type maintenanceEffect struct {
	Backup  *backup.Command  `json:"backup,omitempty"`
	Upgrade *upgrade.Command `json:"upgrade,omitempty"`
}

func (m maintenanceEffect) command(action string) api.ExecutionCommand {
	c := api.ExecutionCommand{Action: "inspect"}
	if m.Backup != nil {
		b := *m.Backup
		c.Kind, c.ServerId, c.EnvironmentId, c.ResourceId, c.PlanHash = "backup", b.Installation.Policy.ServerID, b.Installation.Policy.EnvironmentID, b.Installation.Policy.ID, b.Digest()
		if b.Installation.Policy.GeneralSchedule() {
			c.Kind = "schedule"
		}
		raw, _ := json.Marshal(b)
		c.Backup = rawMessage(raw)
	} else if m.Upgrade != nil {
		u := *m.Upgrade
		if action != "" {
			u.Action = action
		}
		c.Kind, c.ServerId, c.EnvironmentId, c.ResourceId, c.PlanHash = "upgrade", u.Plan.Source.ServerID, u.Plan.Source.EnvironmentID, u.Plan.ID, u.Digest()
		raw, _ := json.Marshal(u)
		c.Upgrade = rawMessage(raw)
	}
	return c
}

func (s *Service) maintenanceEffectAllowed(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) (bool, error) {
	if c.ParentId == nil || c.Kind == "crosshost" || c.Kind == "network" {
		return true, nil
	}
	var phase string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT h.phase,o.command FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id WHERE h.workspace_id=$1 AND h.id=$2 AND o.state NOT IN ('succeeded','failed','cancelled')`, ws, *c.ParentId).Scan(&phase, &raw); err != nil {
		return false, err
	}
	var root api.ExecutionCommand
	var parent coordinationCommand
	if json.Unmarshal(raw, &root) != nil || root.Crosshost == nil || json.Unmarshal(*root.Crosshost, &parent) != nil || parent.Maintenance == nil || !strings.HasPrefix(phase, "maintenance-run:") {
		return false, nil
	}
	effect := parent.Maintenance.Effect.command(maintenanceAction(phase))
	return c.Id == phaseKey(root.Id, phase, parent.Plan.UnitID) && c.Kind == effect.Kind && c.PlanHash == effect.PlanHash && c.ServerId == effect.ServerId && c.EnvironmentId == effect.EnvironmentId && c.ResourceId == effect.ResourceId, nil
}

func (s *Service) insertBackupMaintenance(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, effect api.ExecutionCommand, b backup.Command) (api.Operation, error) {
	var planID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2`, ws, effect.EnvironmentId).Scan(&planID); err != nil {
		return api.Operation{}, err
	}
	p, err := readPlan(ctx, tx, ws, effect.EnvironmentId, planID)
	if err != nil {
		return api.Operation{}, err
	}
	if p.Validate(true) != nil || p.Distributed == nil || p.PrimaryPlan().Hash != b.Installation.Policy.Plan.Hash {
		return api.Operation{}, domain.Conflict
	}
	return s.insertMaintenance(ctx, tx, ws, key, p, maintenanceEffect{Backup: &b}, deployment.Maintenance{Kind: "backup", ResourceID: b.Installation.Policy.ID, RunID: b.RunID, EffectHash: b.Digest()}, nil)
}

func (s *Service) insertUpgradeMaintenance(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, u upgrade.Command) (api.Operation, error) {
	target := u.Plan.TargetGroup()
	identity := u
	identity.Action = ""
	return s.insertMaintenance(ctx, tx, ws, key, *u.Plan.Group, maintenanceEffect{Upgrade: &u}, deployment.Maintenance{Kind: "upgrade", ResourceID: u.Plan.ID, EffectHash: identity.Digest()}, &target)
}

func (s *Service) approveMaintenanceUpgrade(ctx context.Context, tx pgx.Tx, ws uuid.UUID, plan upgrade.Plan, status upgrade.Status, in api.UpgradeActionRequest) (api.Operation, error) {
	var parent uuid.UUID
	var action, expected string
	err := tx.QueryRow(ctx, `SELECT parent_id,action,expected_phase FROM cross_host_approvals WHERE workspace_id=$1 AND request_key=$2 AND upgrade_id=$3`, ws, in.RequestKey, plan.ID).Scan(&parent, &action, &expected)
	if err == nil {
		if action != string(in.Action) || expected != in.ExpectedPhase {
			return api.Operation{}, domain.Conflict
		}
		return scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND id=$2", ws, parent))
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return api.Operation{}, err
	}
	if status.Phase != in.ExpectedPhase || !status.Allows(string(in.Action)) {
		return api.Operation{}, domain.Conflict
	}
	var phase string
	var raw []byte
	err = tx.QueryRow(ctx, `SELECT h.id,h.phase,o.command FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id JOIN cross_host_reservations r ON r.parent_id=h.id AND r.server_id=$3 WHERE h.workspace_id=$1 AND h.environment_id=$2 AND o.state NOT IN ('succeeded','failed','cancelled') FOR UPDATE OF h,o`, ws, plan.Source.EnvironmentID, plan.Source.ServerID).Scan(&parent, &phase, &raw)
	if err != nil {
		return api.Operation{}, err
	}
	var c api.ExecutionCommand
	var frozen coordinationCommand
	if phase != "maintenance-owner" || json.Unmarshal(raw, &c) != nil || c.Crosshost == nil || json.Unmarshal(*c.Crosshost, &frozen) != nil || frozen.Maintenance == nil || frozen.Maintenance.Effect.Upgrade == nil || frozen.Maintenance.Effect.Upgrade.Plan.ID != plan.ID {
		return api.Operation{}, domain.Conflict
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cross_host_approvals(workspace_id,request_key,parent_id,upgrade_id,action,expected_phase) VALUES($1,$2,$3,$4,$5,$6)`, ws, in.RequestKey, parent, plan.ID, in.Action, in.ExpectedPhase); err != nil {
		return api.Operation{}, err
	}
	phase = "maintenance-effect:" + string(in.Action) + ":" + in.RequestKey.String()
	if _, err = tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, parent, phase); err != nil {
		return api.Operation{}, err
	}
	job, err := s.Queue.InsertTx(ctx, tx, jobArgs{ID: parent}, &river.InsertOpts{MaxAttempts: 100})
	if err != nil {
		return api.Operation{}, err
	}
	if _, err = tx.Exec(ctx, `UPDATE execution_operations SET state='running',cancel_requested=false,job_id=$2,note=$3 WHERE id=$1`, parent, job.Job.ID, "Owner-approved upgrade action: "+string(in.Action)); err != nil {
		return api.Operation{}, err
	}
	status.Phase, status.Note = "running", "Coordinated upgrade action queued: "+string(in.Action)
	raw, _ = json.Marshal(status)
	if _, err = tx.Exec(ctx, `UPDATE supported_upgrades SET status=$2 WHERE id=$1`, plan.ID, raw); err != nil {
		return api.Operation{}, err
	}
	return scanOperation(tx.QueryRow(ctx, "SELECT "+opColumns+" FROM execution_operations WHERE workspace_id=$1 AND id=$2", ws, parent))
}

func (s *Service) insertMaintenance(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, p deployment.Plan, effect maintenanceEffect, intent deployment.Maintenance, target *deployment.Plan) (api.Operation, error) {
	m := &coordinatedMaintenance{Effect: effect, Units: []maintenanceUnit{}}
	if intent.Kind == "restore" {
		m.Restore = target
	}
	for _, u := range p.ActiveUnits() {
		if err := s.deploymentServer(ctx, tx, ws, u.Plan.ServerID); err != nil {
			return api.Operation{}, err
		}
		if err := requireReplicas(ctx, tx, ws, u.Plan.ServerID, u.Plan); err != nil {
			return api.Operation{}, err
		}
		var inventory []byte
		if err := tx.QueryRow(ctx, `SELECT inventory FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3`, ws, p.EnvironmentID, u.Plan.UnitID).Scan(&inventory); err != nil {
			return api.Operation{}, err
		}
		var current struct {
			Plan        deployment.Plan        `json:"plan"`
			Observation deployment.Observation `json:"observation"`
		}
		if json.Unmarshal(inventory, &current) != nil || current.Plan.Hash != u.Plan.Hash || len(current.Observation.StateHash) != 64 {
			return api.Operation{}, domain.Conflict
		}
		local := intent
		local.ExpectedState = current.Observation.StateHash
		if target != nil {
			for _, candidate := range target.ActiveUnits() {
				if candidate.Plan.UnitID == u.Plan.UnitID {
					plan := candidate.Plan
					local.Target = &plan
				}
			}
			if local.Target == nil {
				return api.Operation{}, domain.Invalid("maintenance.target")
			}
		}
		m.Units = append(m.Units, maintenanceUnit{Unit: u, Intent: local})
	}
	return s.insertCoordinationCommand(ctx, tx, ws, key, coordinationCommand{Action: "maintenance", Plan: p, Maintenance: m}, p.ActiveUnits())
}

func maintenanceAction(phase string) string {
	parts := strings.Split(phase, ":")
	if len(parts) > 1 {
		return parts[1]
	}
	return ""
}

func maintenancePhaseUnits(m *coordinatedMaintenance, phase string) []maintenanceUnit {
	if phase == "maintenance-checkpoint" || phase == "maintenance-restore" || strings.HasPrefix(phase, "maintenance-effect:") || strings.HasPrefix(phase, "maintenance-run:") {
		for _, u := range m.Units {
			if u.Unit.Plan.Local.Primary {
				return []maintenanceUnit{u}
			}
		}
		return nil
	}
	return m.Units
}

func maintenanceChildPayload(parent api.ExecutionCommand, phase string, m *coordinatedMaintenance, u maintenanceUnit) api.ExecutionCommand {
	c := api.ExecutionCommand{ParentId: &parent.Id, ServerId: u.Unit.Plan.ServerID, EnvironmentId: parent.EnvironmentId, ResourceId: u.Unit.Plan.UnitID, Kind: "crosshost", Action: "inspect"}
	if strings.HasPrefix(phase, "maintenance-run:") {
		c = m.Effect.command(maintenanceAction(phase))

	} else {
		action := strings.TrimPrefix(phase, "maintenance-")
		if strings.HasPrefix(action, "complete-") {
			action = "complete"
		}
		effectAction := ""
		if strings.HasPrefix(action, "effect:") {
			effectAction, action = maintenanceAction(phase), "effect"
		}
		command := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: parent.PlanHash, Phase: action, Unit: u.Unit, Maintenance: &u.Intent, EffectAction: effectAction, UseTarget: phase == "maintenance-complete-target" || phase == "maintenance-resume-target"}
		raw, _ := json.Marshal(command)
		c.Crosshost, c.PlanHash = rawMessage(raw), command.Digest()
	}
	return c
}

func (s *Service) insertMaintenanceChild(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, phase string, m *coordinatedMaintenance, u maintenanceUnit) error {
	key := phaseKey(parent.Id, phase, u.Unit.Plan.UnitID)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND parent_id=$3)`, ws, key, parent.Id).Scan(&exists); err != nil || exists {
		return err
	}
	var generation int64
	if err := tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, parent.EnvironmentId).Scan(&generation); err != nil {
		return err
	}
	c := maintenanceChildPayload(parent, phase, m, u)
	c.Id, c.ParentId, c.Generation = key, &parent.Id, generation
	c.Deadline = time.Now().Add(30 * time.Minute)
	if c.Kind == "upgrade" {
		c.Deadline = time.Now().Add(2 * time.Hour)
	}
	if parent.RecoveryCheckpoint == nil {
		return domain.Invalid("recovery.checkpoint")
	}
	c.RecoveryCheckpoint = parent.RecoveryCheckpoint
	_, err := s.insert(ctx, tx, ws, uuid.New(), nil, c)
	return err
}

func (s *Service) coordinateMaintenance(ctx context.Context, tx pgx.Tx, job *river.Job[jobArgs], ws uuid.UUID, parent api.ExecutionCommand, command coordinationCommand, cancelled bool) error {
	m := command.Maintenance
	var phase string
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT phase,receipts FROM cross_host_operations WHERE id=$1 FOR UPDATE`, parent.Id).Scan(&phase, &raw); err != nil {
		return err
	}
	receipts := map[string]deployment.CrossHostReceipt{}
	if json.Unmarshal(raw, &receipts) != nil {
		return errors.New("invalid maintenance receipts")
	}
	wait := func(state, note string) error {
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state=$2,note=$3 WHERE id=$1`, parent.Id, state, note); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	if phase == "maintenance-owner" {
		var ready bool
		if m.Effect.Upgrade == nil {
			return domain.Invalid("maintenance.owner")
		}
		if err := tx.QueryRow(ctx, `SELECT status->>'phase'='ready' FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, ws, m.Effect.Upgrade.Plan.ID).Scan(&ready); err != nil {
			return err
		}
		state := "needs_attention"
		if ready {
			state = "running"
		}
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state=$2,note='Writers remain stopped. Review the upgrade status and explicitly choose cutover or the permitted recovery action.' WHERE id=$1`, parent.Id, state); err != nil {
			return err
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if cancelled && strings.HasPrefix(phase, "maintenance-run:") {
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE parent_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, parent.Id); err != nil {
			return err
		}
	}
	beforeEffect := phase == "maintenance-checkpoint" || phase == "maintenance-reserve" || phase == "maintenance-pause" || strings.HasPrefix(phase, "maintenance-effect:")
	if phase == "maintenance-restore" {
		var attempted bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND id=$2)`, parent.Id, phaseKey(parent.Id, phase, command.Plan.UnitID)).Scan(&attempted); err != nil {
			return err
		}
		beforeEffect = !attempted
	}
	if beforeEffect {
		effect := m.Effect.command(maintenanceAction(phase))
		if m.Restore != nil {
			effect = parent
		}
		if err := s.commandAdmission(ctx, tx, ws, effect); errors.Is(err, store.SubscriptionRequired) {
			cancelled = true
			if _, err = tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE id=$1`, parent.Id); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	initial := m.Effect.Upgrade == nil || maintenanceAction(phase) == "" || maintenanceAction(phase) == "prepare"
	if (cancelled || initial && time.Now().After(parent.Deadline)) && beforeEffect {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))`, parent.Id).Scan(&pending); err != nil {
			return err
		}
		if pending {
			if _, err := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE parent_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, parent.Id); err != nil {
				return err
			}
			return wait("running", "Waiting for known host outcomes before resuming maintenance writers.")
		}
		if phase == "maintenance-checkpoint" {
			if err := s.closeUnreservedCheckpoint(ctx, tx, ws, parent, command); err != nil {
				return err
			}
			return completeCoordination(ctx, tx, job, parent.Id, "cancelled", "Cancelled before host reservation or writer maintenance began.")
		}
		phase = "maintenance-resume-source"
		if m.Effect.Upgrade != nil {
			var attempted bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND kind='upgrade')`, parent.Id).Scan(&attempted); err != nil {
				return err
			}
			if attempted {
				phase = "maintenance-owner"
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, parent.Id, phase); err != nil {
			return err
		}
		if phase == "maintenance-owner" {
			return wait("needs_attention", "Cancellation retains stopped writers after the data boundary. Choose an explicit upgrade recovery action.")
		}
	}
	recoveryGeneration, err := reserveRecoveryGeneration(ctx, tx, ws, parent, phase)
	if err != nil {
		return err
	}
	all, failed := true, false
	children := []uuid.UUID{}
	for _, u := range maintenancePhaseUnits(m, phase) {
		if err := s.insertMaintenanceChild(ctx, tx, ws, parent, phase, m, u); err != nil {
			return err
		}
		var state api.ExecutionState
		if err := tx.QueryRow(ctx, `SELECT state FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND parent_id=$3 AND server_id=$4`, ws, phaseKey(parent.Id, phase, u.Unit.Plan.UnitID), parent.Id, u.Unit.Plan.ServerID).Scan(&state); err != nil {
			return err
		}
		if state == "needs_attention" {
			return wait("needs_attention", "Maintenance has an uncertain host outcome. Writers and reservations remain held for reconciliation.")
		}
		all = all && Terminal(state)
		failed = failed || state == "failed" || state == "cancelled"
		children = append(children, phaseKey(parent.Id, phase, u.Unit.Plan.UnitID))
	}
	if err := s.stageRecoveryPhase(ctx, tx, ws, parent, command, phase, children, recoveryGeneration); err != nil {
		return err
	}
	if !all {
		return wait("running", "Coordinated "+phase+"; the primary data effect waits for every required host receipt.")
	}
	if failed && phase == "maintenance-checkpoint" {
		return completeCoordination(ctx, tx, job, parent.Id, "failed", "The primary did not acknowledge recovery metadata; no host was reserved or paused.")
	}
	if failed && phase != "maintenance-reserve" && phase != "maintenance-pause" && !strings.HasPrefix(phase, "maintenance-run:") {
		return wait("needs_attention", "Maintenance writer recovery is incomplete. Reservations remain held.")
	}
	if failed && (phase == "maintenance-reserve" || phase == "maintenance-pause") {
		phase = "maintenance-resume-source"
	} else if strings.HasPrefix(phase, "maintenance-effect:") {
		phase = strings.Replace(phase, "maintenance-effect:", "maintenance-run:", 1)
	} else if strings.HasPrefix(phase, "maintenance-run:") && m.Effect.Upgrade != nil {
		var raw []byte
		var status upgrade.Status
		if err := tx.QueryRow(ctx, `SELECT status FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, ws, m.Effect.Upgrade.Plan.ID).Scan(&raw); err != nil {
			return err
		}
		if json.Unmarshal(raw, &status) != nil {
			return domain.Conflict
		}
		switch status.Phase {
		case "complete":
			phase = "maintenance-resume-target"
		case "returned", "rejected":
			phase = "maintenance-resume-source"
		default:
			phase = "maintenance-owner"
		}
	} else {
		switch phase {
		case "maintenance-checkpoint":
			phase = "maintenance-reserve"
		case "maintenance-reserve":
			phase = "maintenance-pause"
		case "maintenance-pause":
			phase = "maintenance-effect:backup"
			if m.Restore != nil {
				phase = "maintenance-restore"
			} else if m.Effect.Upgrade != nil {
				phase = "maintenance-effect:prepare"
			}
		case "maintenance-restore":
			phase = "maintenance-resume-target"
		case "maintenance-run:backup":
			phase = "maintenance-resume-source"
		case "maintenance-resume-source":
			phase = "maintenance-complete-source"
		case "maintenance-resume-target":
			phase = "maintenance-complete-target"
		case "maintenance-complete-source", "maintenance-complete-target":
			target := command.Plan
			if phase == "maintenance-complete-target" {
				if m.Restore != nil {
					target = *m.Restore
					if _, err := tx.Exec(ctx, `UPDATE backup_policies SET configuration=jsonb_set(configuration,'{enabled}','false') WHERE workspace_id=$1 AND environment_id=$2`, ws, parent.EnvironmentId); err != nil {
						return err
					}
				} else if m.Effect.Upgrade != nil {
					if err := s.saveUpgradeTarget(ctx, tx, *m.Effect.Upgrade, parent.Id); err != nil {
						return err
					}
					target = m.Effect.Upgrade.Plan.TargetGroup()
				} else {
					return domain.Invalid("maintenance.target")
				}
			}
			if err := s.finalizeCrossHostUnits(ctx, tx, ws, target, receipts, target.ActiveUnits()); err != nil {
				return err
			}
			var effectFailed bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND state IN ('failed','cancelled'))`, parent.Id).Scan(&effectFailed); err != nil {
				return err
			}
			if m.Effect.Upgrade != nil {
				var known bool
				if err := tx.QueryRow(ctx, `SELECT status->>'phase' IN ('complete','returned') FROM supported_upgrades WHERE workspace_id=$1 AND id=$2`, ws, m.Effect.Upgrade.Plan.ID).Scan(&known); err != nil {
					return err
				}
				if known {
					effectFailed = false
				} else {
					effectFailed = true
					if _, err := tx.Exec(ctx, `UPDATE supported_upgrades SET status=jsonb_set(jsonb_set(status,'{phase}','"rejected"'),'{note}','"Writer coordination ended before a database effect completed. The source is retained."') WHERE workspace_id=$1 AND id=$2`, ws, m.Effect.Upgrade.Plan.ID); err != nil {
						return err
					}
				}
			}
			outcome := "succeeded"
			if effectFailed {
				outcome = "failed"
			}
			if cancelled {
				outcome = "cancelled"
			}
			return completeCoordination(ctx, tx, job, parent.Id, outcome, "The maintenance outcome is recorded and every owned writer is reconciled.")
		default:
			return errors.New("invalid coordinated maintenance phase")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, parent.Id, phase); err != nil {
		return err
	}
	return wait("running", "Coordinated maintenance phase: "+phase)
}
