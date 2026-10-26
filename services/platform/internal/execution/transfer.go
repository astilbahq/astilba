package execution

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"reflect"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
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
	rows, e := tx.Query(ctx, `SELECT r.unit_id,p.plan FROM visual_runtime r JOIN visual_plans p ON p.id=r.plan_id AND p.workspace_id=r.workspace_id WHERE r.workspace_id=$1 AND r.server_id=$2`, ws, server)
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
		out.Units[id.String()] = p.Hash
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
	var ws uuid.UUID
	var expected int64
	var awaiting bool
	e = tx.QueryRow(ctx, `SELECT workspace_id,controller_generation,awaiting_transfer FROM servers WHERE id=$1 AND NOT revoked AND (machine_id IS NULL OR machine_id=$4) AND ((enrollment_hash=$2 AND enrollment_expires>now() AND credential_hash IS NULL) OR credential_hash=$3) FOR UPDATE`, in.ServerId, hash(in.Token), hash(in.Credential), in.MachineId).Scan(&ws, &expected, &awaiting)
	if e != nil {
		if errors.Is(e, pgx.ErrNoRows) {
			e = domain.Unauthenticated
		}
		return out, e
	}
	var previewOnly bool
	var fresh bool
	if e = tx.QueryRow(ctx, "SELECT preview_only,machine_id IS NULL FROM servers WHERE id=$1", in.ServerId).Scan(&previewOnly, &fresh); e != nil {
		return out, e
	}
	if previewOnly && fresh && in.Preflight.ExistingContainers > 0 {
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
		if !reflect.DeepEqual(inventory, *in.Inventory) {
			return out, domain.Conflict
		}
	}
	caps, _ := json.Marshal(in.Capabilities)
	pre, _ := json.Marshal(in.Preflight)
	e = tx.QueryRow(ctx, `UPDATE servers SET credential_hash=$2,enrollment_hash=NULL,enrollment_expires=NULL,machine_id=$3,capabilities=$4,preflight=$5,last_seen=now(),awaiting_transfer=false WHERE id=$1 RETURNING role,rotation_requested,preview_only`, in.ServerId, hash(in.Credential), in.MachineId, caps, pre).Scan(&out.Role, &out.RotationRequested, &previewOnly)
	if e != nil {
		return out, e
	}
	if previewOnly {
		out.PreviewOnly = &previewOnly
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
	e = s.scope(ctx, p, ws, true, true, true, "workspace.transfer", id, func(tx pgx.Tx) error {
		if e := s.Store.ManagedServerAdmission(ctx, tx, ws, id); e != nil {
			return e
		}
		return tx.QueryRow(ctx, `UPDATE servers SET enrollment_hash=$3,enrollment_expires=$4,credential_hash=NULL,previous_hash=NULL,previous_until=NULL,poll_id=NULL,poll_until=NULL,revoked=false WHERE workspace_id=$1 AND id=$2 AND awaiting_transfer RETURNING controller_generation`, ws, id, hash(token), out.ExpiresAt).Scan(&out.Generation)
	})
	return out, e
}
