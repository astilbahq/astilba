package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"reflect"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func generation(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
func transferInventory(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) (api.ControllerInventory, error) {
	out := api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	rows, e := tx.Query(ctx, `SELECT r.unit_id,p.plan FROM visual_runtime r
	 JOIN visual_plans p ON p.id=r.plan_id AND p.workspace_id=r.workspace_id
	 WHERE r.workspace_id=$1 AND r.server_id=$2
	 AND NOT EXISTS(SELECT 1 FROM cross_host_runtime c WHERE c.workspace_id=r.workspace_id AND c.unit_id=r.unit_id)
	 UNION ALL SELECT unit_id,inventory->'plan' FROM cross_host_runtime WHERE workspace_id=$1 AND server_id=$2`, ws, server)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		var p deployment.Plan
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return out, e
		}
		if e = json.Unmarshal(raw, &p); e != nil {
			rows.Close()
			return out, e
		}
		out.Units[id.String()] = p.PrimaryPlan().Hash
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	rows, e = tx.Query(ctx, "SELECT id,status FROM backup_policies WHERE workspace_id=$1 AND server_id=$2", ws, server)
	if e != nil {
		return out, e
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var raw []byte
		var s backup.Status
		if e = rows.Scan(&id, &raw); e != nil {
			return out, e
		}
		if e = json.Unmarshal(raw, &s); e != nil {
			return out, e
		}
		if s.PolicyID != uuid.Nil {
			out.Schedules[id.String()] = s.Generation
		}
	}
	return out, rows.Err()
}
func (s *Service) enroll(ctx context.Context, in api.AgentEnrollment) (api.AgentStatus, error) {
	var out api.AgentStatus
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	locked, e := q.LockAgentEnrollment(ctx, db.LockAgentEnrollmentParams{ServerID: in.ServerId, EnrollmentHash: hash(in.Token), CredentialHash: hash(in.Credential), MachineID: &in.MachineId})
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = domain.Unauthenticated
		}
		return out, e
	}
	ws, expected, awaiting := locked.WorkspaceID, locked.ControllerGeneration, locked.AwaitingTransfer
	if locked.PreviewOnly && locked.Fresh && in.Preflight.ExistingContainers > 0 {
		return out, domain.Invalid("preview.cleanServerRequired")
	}
	if generation(in.ControllerGeneration) != expected {
		return out, domain.Unauthenticated
	}
	if awaiting {
		if in.Inventory == nil {
			return out, domain.Invalid("inventory")
		}
		if in.Inventory != nil {
			if e = s.acceptPreviewCleanupProofs(ctx, tx, ws, in.ServerId, in.Inventory.PreviewCleanups); e != nil {
				return out, e
			}
			in.Inventory.PreviewCleanups = nil
		}
		inventory, e := transferInventory(ctx, tx, ws, in.ServerId)
		if e != nil {
			return out, e
		}
		stagedServer := db.StagedRecoveryInventoryParams{WorkspaceID: ws, ServerID: in.ServerId, MachineID: in.MachineId, ControllerGeneration: expected}
		staged, err := q.StagedRecoveryInventory(ctx, stagedServer)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return out, err
		}
		if err == nil {
			var stagedInventory api.ControllerInventory
			if json.Unmarshal(staged, &stagedInventory) != nil {
				return out, domain.Invalid("recovery.inventory")
			}
			inventory = stagedInventory
		}
		actual := *in.Inventory
		if inventory.Recovery != nil || actual.Recovery != nil {
			var expectedAuthority, actualAuthority deployment.RecoveryAuthority
			if inventory.Recovery == nil || actual.Recovery == nil || hostnetwork.Decode(*inventory.Recovery, &expectedAuthority) != nil || hostnetwork.Decode(*actual.Recovery, &actualAuthority) != nil || expectedAuthority != actualAuthority {
				return out, domain.Conflict
			}
			inventory.Recovery, actual.Recovery = nil, nil
		}
		if !reflect.DeepEqual(inventory, actual) {
			return out, domain.Conflict
		}
		if err == nil {
			if err = q.VerifyStagedRecoveryServer(ctx, db.VerifyStagedRecoveryServerParams(stagedServer)); err != nil {
				return out, err
			}
		}
	}
	caps, _ := json.Marshal(in.Capabilities)
	pre, _ := json.Marshal(in.Preflight)
	enrolled, e := q.CompleteAgentEnrollment(ctx, db.CompleteAgentEnrollmentParams{WorkspaceID: ws, ServerID: in.ServerId, CredentialHash: hash(in.Credential), MachineID: &in.MachineId, Capabilities: caps, Preflight: pre})
	if e != nil {
		return out, e
	}
	out.Role, out.RotationRequested = api.ServerRole(enrolled.Role), enrolled.RotationRequested
	if enrolled.PreviewOnly {
		out.PreviewOnly = &enrolled.PreviewOnly
	}
	return out, tx.Commit(ctx)
}

func (s *Service) PrepareTransfer(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.TransferTicket, error) {
	out := api.TransferTicket{ServerId: id, ExpiresAt: time.Now().Add(10 * time.Minute)}
	token, e := cryptobox.Token()
	if e != nil {
		return out, e
	}
	out.Token = token
	e = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true, Recovery: true}, "workspace.transfer", id, func(tx pgx.Tx) error {
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, id); e != nil {
			return e
		}
		generation, e := db.New(tx).PrepareServerTransfer(ctx, db.PrepareServerTransferParams{WorkspaceID: ws, ID: id, EnrollmentHash: hash(token), EnrollmentExpires: &out.ExpiresAt})
		out.Generation = generation
		return e
	})
	return out, e
}
