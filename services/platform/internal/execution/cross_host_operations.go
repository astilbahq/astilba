package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

func (s *Service) insertCoordination(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, action string, p deployment.Plan) (api.Operation, error) {
	units := p.Distributed.Units
	if action == "observe" {
		revoked, err := revokedParticipants(ctx, tx, ws, p.ActiveUnits())
		if err != nil {
			return api.Operation{}, err
		}
		units = withoutRevoked(p.ActiveUnits(), revoked)
	}
	return s.insertCoordinationCommand(ctx, tx, ws, key, coordinationCommand{Action: action, Plan: p}, units)
}
func (s *Service) insertCoordinationCommand(ctx context.Context, tx pgx.Tx, ws, key uuid.UUID, command coordinationCommand, units []deployment.DistributedUnit) (api.Operation, error) {
	p := command.Plan
	for _, u := range units {
		var idle bool
		if err := tx.QueryRow(ctx, `SELECT NOT EXISTS(SELECT 1 FROM execution_operations WHERE server_id=$1 AND state NOT IN ('succeeded','failed','cancelled')) AND NOT EXISTS(SELECT 1 FROM cross_host_reservations WHERE server_id=$1) AND NOT EXISTS(SELECT 1 FROM supported_upgrades WHERE server_id=$1 AND status->>'phase' NOT IN ('planned','rejected','complete','returned'))`, u.Plan.ServerID).Scan(&idle); err != nil {
			return api.Operation{}, err
		}
		if !idle {
			return api.Operation{}, domain.Conflict
		}
	}
	var generation int64
	if err := tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, p.EnvironmentID).Scan(&generation); err != nil {
		return api.Operation{}, err
	}
	raw, _ := stablejson.Marshal(command)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, Generation: generation, Kind: "crosshost", Action: "inspect", ResourceId: p.UnitID, PlanHash: Hash(command), Deadline: time.Now().Add(30 * time.Minute), Crosshost: rawMessage(raw)}
	op, err := s.insert(ctx, tx, ws, key, nil, c)
	if err != nil {
		return op, err
	}
	phase := "resolve"
	if command.Action == "deploy" {
		phase = "checkpoint"
	}
	if command.Action == "observe" {
		phase = "observe"
	}
	if command.Maintenance != nil {
		phase = "maintenance-checkpoint"
	}
	if _, err = tx.Exec(ctx, `INSERT INTO cross_host_operations(id,workspace_id,environment_id,plan_id,phase) VALUES($1,$2,$3,$4,$5)`, op.Id, ws, p.EnvironmentID, p.ID, phase); err != nil {
		return op, err
	}
	for _, u := range units {
		cpu, mem := u.CPUMillis, u.MemoryMiB
		if command.Action == "resolve" || command.Action == "observe" || command.Maintenance != nil {
			cpu = 0
			mem = 0
		}
		if _, err = tx.Exec(ctx, `INSERT INTO cross_host_reservations(server_id,workspace_id,parent_id,cpu_millis,memory_mib) VALUES($1,$2,$3,$4,$5)`, u.Plan.ServerID, ws, op.Id, cpu, mem); err != nil {
			return op, err
		}
	}
	return op, nil
}
func (s *Service) crossHostOperationAdmission(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) error {
	var reservation uuid.UUID
	err := tx.QueryRow(ctx, `SELECT parent_id FROM cross_host_reservations WHERE workspace_id=$1 AND server_id=$2`, ws, c.ServerId).Scan(&reservation)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err == nil && (c.ParentId == nil || *c.ParentId != reservation) {
		if c.Kind == "network" && recoveryCommand(c) {
			return nil
		}
		return domain.Conflict
	}
	if c.ParentId != nil {
		var valid bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND environment_id=$3 AND parent_id IS NULL AND kind='crosshost' AND state NOT IN ('succeeded','failed','cancelled'))`, ws, *c.ParentId, c.EnvironmentId).Scan(&valid); err != nil {
			return err
		}
		if !valid || reservation != *c.ParentId {
			return domain.Forbidden
		}
		if c.Kind != "crosshost" && c.Kind != "network" {
			allowed, err := s.maintenanceEffectAllowed(ctx, tx, ws, c)
			if err != nil {
				return err
			}
			if !allowed {
				return domain.Forbidden
			}
		}
	}
	return nil
}
func phaseKey(parent uuid.UUID, phase string, unit uuid.UUID) uuid.UUID {
	return uuid.NewSHA1(parent, []byte("crosshost:"+phase+":"+unit.String()))
}
func (s *Service) insertCrossHostChild(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent api.ExecutionCommand, p deployment.Plan, phase string, u deployment.DistributedUnit) error {
	key := phaseKey(parent.Id, phase, u.Plan.UnitID)
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND parent_id=$3)`, ws, key, parent.Id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return nil
	}
	var generation int64
	if err := tx.QueryRow(ctx, `UPDATE environments SET execution_generation=execution_generation+1 WHERE workspace_id=$1 AND id=$2 RETURNING execution_generation`, ws, p.EnvironmentID).Scan(&generation); err != nil {
		return err
	}
	c := api.ExecutionCommand{Id: key, ServerId: u.Plan.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: u.Plan.UnitID, Generation: generation, Kind: "crosshost", Action: "inspect", Deadline: time.Now().Add(30 * time.Minute), ParentId: &parent.Id}
	if phase != "resolve" && phase != "observe" {
		if parent.RecoveryCheckpoint == nil {
			return domain.Invalid("recovery.checkpoint")
		}
		c.RecoveryCheckpoint = parent.RecoveryCheckpoint
	}
	if phase == "links" || phase == "restrict" || phase == "abort-restrict" {
		if err := tx.QueryRow(ctx, `UPDATE servers SET update_generation=update_generation+1 WHERE id=$1 RETURNING update_generation`, c.ServerId).Scan(&c.Generation); err != nil {
			return err
		}
		manifest := u.Plan.Local.Network
		action := "configure"
		if phase != "links" {
			network, err := readNetwork(ctx, tx, ws)
			if err != nil {
				return err
			}
			found := false
			for _, m := range network.Members {
				if m.ServerID != u.Plan.ServerID {
					continue
				}
				var ready bool
				manifest, ready, err = networkManifest(ctx, tx, ws, network, m)
				if err != nil {
					return err
				}
				if !ready {
					return domain.Conflict
				}
				found = true
			}
			if !found {
				return domain.Conflict
			}
			action = "restrict"
		}
		n := hostnetwork.Command{Action: action, NetworkID: manifest.NetworkID, MembershipID: manifest.MembershipID, Manifest: &manifest}
		raw, _ := stablejson.Marshal(n)
		c.Kind = "network"
		c.ResourceId = n.NetworkID
		c.Network = rawMessage(raw)
		c.PlanHash = hostnetwork.Hash(n)
	} else {
		command := deployment.CrossHostCommand{ParentID: parent.Id, GroupHash: p.Hash, Phase: phase, Unit: u}
		raw, _ := stablejson.Marshal(command)
		c.Crosshost = rawMessage(raw)
		c.PlanHash = command.Digest()
	}
	// Internal child identities do not share a caller-controlled request key.
	_, err := s.insert(ctx, tx, ws, uuid.New(), nil, c)
	return err
}
func phaseUnits(p deployment.Plan, phase string) []deployment.DistributedUnit {
	if phase == "observe" {
		return p.ActiveUnits()
	}
	if phase == "hook" {
		for _, u := range p.Distributed.Units {
			if u.Plan.Release != nil {
				return []deployment.DistributedUnit{u}
			}
		}
		return nil
	}
	if phase == "checkpoint" || phase == "publish" || phase == "probe" {
		for _, u := range p.Distributed.Units {
			if u.Plan.Local.Primary {
				return []deployment.DistributedUnit{u}
			}
		}
		return nil
	}
	return p.Distributed.Units
}
func (s *Service) coordinate(ctx context.Context, tx pgx.Tx, job *river.Job[jobArgs], ws uuid.UUID, c api.ExecutionCommand, state api.ExecutionState, cancelled bool) error {
	var command coordinationCommand
	if c.Crosshost == nil || hostnetwork.Decode(*c.Crosshost, &command) != nil || Hash(command) != c.PlanHash || command.Plan.Distributed == nil {
		return domain.Invalid("crosshost")
	}
	p := command.Plan
	if blocked, err := s.recoveryGate(ctx, tx, ws, c, command); err != nil {
		return err
	} else if blocked {
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	if (command.Action == "deploy" || command.Maintenance != nil) && c.RecoveryCheckpoint == nil {
		if cancelled {
			if command.Maintenance == nil {
				if err := s.retireCrossHostGrants(ctx, tx, ws, p, true); err != nil {
					return err
				}
			}
			return completeBeforeEffects(ctx, tx, job, ws, c.Id, command.Maintenance, "cancelled", "Cancelled before recovery metadata or any host effects were admitted.")
		}
		var phase string
		if err := tx.QueryRow(ctx, `SELECT phase FROM cross_host_operations WHERE id=$1`, c.Id).Scan(&phase); err != nil {
			return err
		}
		if phase != "checkpoint" && phase != "maintenance-checkpoint" {
			return domain.Invalid("recovery.checkpoint")
		}
		units := p.Distributed.Units
		if command.Maintenance != nil {
			units = p.ActiveUnits()
		}
		descriptor, err := s.createRecoveryBase(ctx, tx, ws, c, p, units)
		if errors.Is(err, portability.ErrArchive) || errors.Is(err, errRecoverySize) {
			if command.Maintenance == nil {
				if err := s.retireCrossHostGrants(ctx, tx, ws, p, true); err != nil {
					return err
				}
			}
			return completeBeforeEffects(ctx, tx, job, ws, c.Id, command.Maintenance, "failed", "Recovery metadata exceeds the supported checkpoint limits; no host was reserved or changed.")
		}
		if err != nil {
			return err
		}
		raw, _ := stablejson.Marshal(descriptor)
		c.RecoveryCheckpoint = rawMessage(raw)
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{recoveryCheckpoint}',$2::jsonb,true) WHERE id=$1`, c.Id, raw); err != nil {
			return err
		}
	}
	if command.Maintenance != nil {
		return s.coordinateMaintenance(ctx, tx, job, ws, c, command, cancelled)
	}
	var phase string
	var receiptsRaw []byte
	if err := tx.QueryRow(ctx, `SELECT phase,receipts FROM cross_host_operations WHERE id=$1 FOR UPDATE`, c.Id).Scan(&phase, &receiptsRaw); err != nil {
		return err
	}
	receipts := map[string]deployment.CrossHostReceipt{}
	if json.Unmarshal(receiptsRaw, &receipts) != nil {
		return errors.New("invalid coordinated receipts")
	}
	beforePublication := phase == "checkpoint" || phase == "observe" || phase == "resolve" || phase == "reserve" || phase == "links" || phase == "prepare" || phase == "hook" || phase == "probe"
	if beforePublication {
		// Admission must also stop parents between committed phases, before a child exists.
		if err := s.commandAdmission(ctx, tx, ws, c); errors.Is(err, store.SubscriptionRequired) {
			cancelled = true
			if _, err = tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE id=$1`, c.Id); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	if cancelled && beforePublication {
		var pending bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM execution_operations WHERE parent_id=$1 AND state NOT IN ('succeeded','failed','cancelled'))`, c.Id).Scan(&pending); err != nil {
			return err
		}
		if pending {
			if _, err := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE parent_id=$1 AND state NOT IN ('succeeded','failed','cancelled')`, c.Id); err != nil {
				return err
			}

			if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state='running',note='Waiting for dispatched host outcomes before owned candidate cleanup.' WHERE id=$1`, c.Id); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return river.JobSnooze(2 * time.Second)
		} else if phase != "checkpoint" && phase != "resolve" && phase != "observe" {
			phase = "abort"
			if _, err := tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, c.Id, phase); err != nil {
				return err
			}
		} else {
			if phase == "checkpoint" {
				if err := s.retireCrossHostGrants(ctx, tx, ws, command.Plan, true); err != nil {
					return err
				}
				if err := s.closeUnreservedCheckpoint(ctx, tx, ws, c, command); err != nil {
					return err
				}
			}
			return completeCoordination(ctx, tx, job, c.Id, "cancelled", "Resolution cancelled before changing workloads.")
		}
	}
	recoveryGeneration, err := reserveRecoveryGeneration(ctx, tx, ws, c, phase)
	if err != nil {
		return err
	}
	revoked, err := revokedParticipants(ctx, tx, ws, p.Distributed.Units)
	if err != nil {
		return err
	}
	stopping, err := revokedStopsDeployment(ctx, tx, ws, c.Id, p, phase, revoked)
	if err != nil {
		return err
	}
	if stopping {
		if err := db.New(tx).CancelHeldPreparation(ctx, db.CancelHeldPreparationParams{WorkspaceID: ws, ParentID: &c.Id}); err != nil {
			return err
		}
	}
	all := true
	uncertain := false
	failed := stopping
	children := []uuid.UUID{}
	for _, u := range phaseUnits(p, phase) {
		if revoked[u.Plan.ServerID] {
			continue
		}
		if !stopping {
			if err := s.insertCrossHostChild(ctx, tx, ws, c, p, phase, u); err != nil {
				return err
			}
		}
		var child api.ExecutionState
		err := tx.QueryRow(ctx, `SELECT state FROM execution_operations WHERE workspace_id=$1 AND id=$2 AND parent_id=$3 AND server_id=$4`, ws, phaseKey(c.Id, phase, u.Plan.UnitID), c.Id, u.Plan.ServerID).Scan(&child)
		if stopping && errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return err
		}
		all = all && Terminal(child)
		uncertain = uncertain || child == "needs_attention"
		failed = failed || child == "failed" || child == "cancelled"
		children = append(children, phaseKey(c.Id, phase, u.Plan.UnitID))
	}
	if err := s.stageRecoveryPhase(ctx, tx, ws, c, command, phase, children, recoveryGeneration); err != nil {
		return err
	}
	if uncertain {
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state='needs_attention',note='A host outcome is uncertain. Existing pools and host reservations are retained; reconcile the same child operation.' WHERE id=$1`, c.Id); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	if !all {
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state='running',note=$2 WHERE id=$1`, c.Id, "Cross-host phase: "+phase); err != nil {
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
		return river.JobSnooze(2 * time.Second)
	}
	if failed {
		if phase == "checkpoint" || phase == "resolve" || phase == "observe" {
			if phase == "checkpoint" {
				if err := s.retireCrossHostGrants(ctx, tx, ws, p, true); err != nil {
					return err
				}
				if stopping {
					if err := s.closeUnreservedCheckpoint(ctx, tx, ws, c, command); err != nil {
						return err
					}
				}
			}
			if stopping {
				return completeCoordination(ctx, tx, job, c.Id, "failed", revokedBeforeChangesNote)
			}
			return completeCoordination(ctx, tx, job, c.Id, "failed", "A placement host could not resolve the frozen inputs.")
		}
		if phase == "reserve" || phase == "links" || phase == "prepare" || phase == "hook" || phase == "probe" || stopping {
			phase = "abort"
		} else {
			if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state='needs_attention',note='Cross-host publication or cleanup requires reconciliation. No unknown outcome was treated as failure.' WHERE id=$1`, c.Id); err != nil {
				return err
			}
			if err := tx.Commit(ctx); err != nil {
				return err
			}
			return river.JobSnooze(2 * time.Second)
		}
	} else {
		switch phase {
		case "checkpoint":
			phase = "reserve"
		case "observe":
			if err := s.recordCrossHostObservations(ctx, tx, ws, uuid.Nil, p, receipts, withoutRevoked(p.ActiveUnits(), revoked), "observe", false); err != nil {
				return err
			}
			if len(revoked) > 0 {
				return completeCoordination(ctx, tx, job, c.Id, "succeeded", "Every placement that can still answer was inspected. "+revokedServersNote)
			}
			return completeCoordination(ctx, tx, job, c.Id, "succeeded", "Every active placement was inspected; resources and the applied revision are unchanged.")
		case "resolve":
			for i := range p.Distributed.Units {
				u := &p.Distributed.Units[i]
				r, ok := receipts["resolve/"+u.Plan.UnitID.String()]
				if !ok {
					return errors.New("missing resolution receipt")
				}
				u.Plan.Images = r.Observation.Images
				u.Plan.ExpectedState = r.Observation.StateHash
				u.Plan = u.Plan.WithHash()
			}
			p = p.WithHash()
			if err := p.Validate(true); err != nil {
				return err
			}
			raw, _ := stablejson.Marshal(p)
			if _, err := tx.Exec(ctx, `INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4)`, p.ID, ws, p.EnvironmentID, raw); err != nil {
				return err
			}
			return completeCoordination(ctx, tx, job, c.Id, "succeeded", "Every placement's immutable images and observed state are ready for review.")
		case "reserve":
			phase = "links"
		case "links":
			phase = "prepare"
		case "prepare":
			if p.ReleaseOptions != nil && len(p.ReleaseOptions.Command) > 0 {
				phase = "hook"
			} else {
				phase = "probe"
			}
		case "hook":
			phase = "probe"
		case "probe":
			phase = "publish"
		case "publish":
			phase = "retire"
		case "retire":
			if err := s.retireCrossHostGrants(ctx, tx, ws, p, false); err != nil {
				return err
			}
			phase = "restrict"
		case "restrict":
			phase = "complete"
		case "abort":
			if err := s.retireCrossHostGrants(ctx, tx, ws, p, true); err != nil {
				return err
			}
			phase = "abort-restrict"
		case "abort-restrict":
			outcome := "failed"
			if cancelled {
				outcome = "cancelled"
			}
			if len(revoked) > 0 {
				return completeCoordination(ctx, tx, job, c.Id, outcome, revokedDeploymentNote)
			}
			return completeCoordination(ctx, tx, job, c.Id, outcome, "Candidate pools were removed with owned cleanup receipts; previous serving pools are retained.")
		case "complete":
			if err := s.finalizeCrossHostUnits(ctx, tx, ws, c.Id, p, receipts, withoutRevoked(p.Distributed.Units, revoked)); err != nil {
				return err
			}
			if len(revoked) > 0 {
				return completeCoordination(ctx, tx, job, c.Id, "succeeded", "The remaining hosts confirmed this placement. "+revokedServersNote)
			}
			return completeCoordination(ctx, tx, job, c.Id, "succeeded", "All placement and ingress receipts are confirmed; previous pools were retired.")
		default:
			return errors.New("invalid cross-host phase")
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE cross_host_operations SET phase=$2 WHERE id=$1`, c.Id, phase); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return river.JobSnooze(time.Second)
}
func completeCoordination(ctx context.Context, tx pgx.Tx, job *river.Job[jobArgs], id uuid.UUID, state, note string) error {
	if _, err := tx.Exec(ctx, `DELETE FROM cross_host_reservations WHERE parent_id=$1`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE execution_operations SET state=$2,note=$3 WHERE id=$1`, id, state, note); err != nil {
		return err
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
