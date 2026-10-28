package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrPollLeaseHeld distinguishes temporary poll contention from command conflicts.
// HTTP clients still receive the ordinary conflict response.
var ErrPollLeaseHeld = fmt.Errorf("poll lease held: %w", domain.Conflict)

// A database lease bounds overlapping polls across management replicas.
func (s *Service) Poll(ctx context.Context, a Identity) (*api.ExecutionCommand, error) {
	id := uuid.New()
	n, err := s.Store.Pool.Exec(ctx, `UPDATE servers SET poll_id=$2,poll_until=now()+interval '30 seconds' WHERE id=$1 AND (poll_until IS NULL OR poll_until<now())`, a.ID, id)
	if err != nil {
		return nil, err
	}
	if n.RowsAffected() != 1 {
		return nil, ErrPollLeaseHeld
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _ = s.Store.Pool.Exec(cleanup, "UPDATE servers SET poll_id=NULL,poll_until=NULL WHERE id=$1 AND poll_id=$2", a.ID, id)
	}()
	deadline := time.NewTimer(PollDuration)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		// Check revocation/expiry after each wait, not only at request entry.
		if _, err = s.Authenticate(ctx, a.ID, a.Token); err != nil {
			return nil, err
		}
		var raw []byte
		var cancel bool
		var state string
		err = s.Store.Pool.QueryRow(ctx, `SELECT command,cancel_requested,state FROM execution_operations WHERE server_id=$1 AND NOT(kind='crosshost' AND parent_id IS NULL) AND state IN ('dispatched','running','needs_attention') AND NOT EXISTS(SELECT 1 FROM cross_host_recoveries r WHERE r.parent_id=execution_operations.parent_id AND NOT r.opened AND COALESCE(command->'crosshost'->>'phase','')<>'recovery-open') AND (parent_id IS NULL OR command->'recoveryCheckpoint' IS NULL OR command->'crosshost'->>'phase' IN ('checkpoint','checkpoint-closed') OR EXISTS(SELECT 1 FROM cross_host_checkpoints cp WHERE cp.parent_id=execution_operations.parent_id AND cp.id::text=command->'recoveryCheckpoint'->>'checkpointId' AND cp.acknowledged)) ORDER BY (command->'crosshost'->>'phase'='checkpoint') DESC NULLS LAST, (kind='network' AND COALESCE(command->'network'->>'action','')='restrict') DESC,created_at LIMIT 1`, a.ID).Scan(&raw, &cancel, &state)
		if err == nil {
			var c api.ExecutionCommand
			if err = json.Unmarshal(raw, &c); err != nil {
				return nil, err
			}
			if (a.Role == "build" && c.Kind != "update" && c.Kind != "pipeline") || (a.Role == "deployment" && c.Kind == "pipeline") {
				return nil, domain.Forbidden
			}
			if s.Store.Hosted {
				tx, e := s.Store.Pool.Begin(ctx)
				if e != nil {
					return nil, e
				}
				e = s.commandAdmission(ctx, tx, a.Workspace, c)
				_ = tx.Rollback(ctx)
				if e != nil {
					if !errors.Is(e, store.SubscriptionRequired) {
						return nil, e
					}
					cancel = true
				}
			}
			if !cancel {
				tx, e := s.Store.Pool.Begin(ctx)
				if e != nil {
					return nil, e
				}
				allowed, e := s.previewCommandAllowed(ctx, tx, a.Workspace, c, state == "dispatched")
				if e == nil && allowed {
					allowed, e = s.maintenanceEffectAllowed(ctx, tx, a.Workspace, c)
				}
				_ = tx.Rollback(ctx)
				if e != nil {
					return nil, e
				}
				if !allowed {
					cancel = true
				}
			}
			c.CancelRequested = cancel
			// Existing agents compare hydrated visual payloads byte-for-byte.
			// Other command kinds have always accepted credential-free cancellation.
			if cancel && (c.Kind != "visual" || a.CredentialFreeVisualCancel) {
				return &c, nil
			}
			if c.Kind == "upgrade" {
				if err = s.hydrateUpgrade(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "backup" || c.Kind == "schedule" {
				if err = s.hydrateBackup(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "pipeline" {
				if err = s.hydratePipeline(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "crosshost" {
				if err = s.hydrateCrossHost(ctx, &c); err != nil {
					return nil, err
				}
			}
			if c.Kind == "visual" {
				if err = s.hydrateVisual(ctx, &c); err != nil {
					return nil, err
				}
			}
			return &c, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, nil
		case <-tick.C:
		}
	}
}
func (s *Service) Report(ctx context.Context, a Identity, r api.AgentReport) error {
	if r.Sequence < 1 || r.Sequence > 1<<53 || len(r.Note) > 1024 || len(r.Steps) > 16 {
		return domain.Invalid("report")
	}
	if r.State != "running" && r.State != "needs_attention" && !Terminal(r.State) {
		return domain.Invalid("state")
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if r.Network != nil {
		if _, err = tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, a.Workspace); err != nil {
			return err
		}
	}
	// Lock identity before operation, matching operator admission. Revocation cannot race acceptance.
	var valid bool
	err = tx.QueryRow(ctx, `SELECT NOT revoked AND NOT awaiting_transfer AND (credential_hash=$2 OR (previous_hash=$2 AND previous_until>now())) FROM servers WHERE id=$1 FOR UPDATE`, a.ID, hash(a.Token)).Scan(&valid)
	if err != nil {
		return err
	}
	if !valid {
		return domain.Unauthenticated
	}
	var raw []byte
	var seq int64
	var state api.ExecutionState
	err = tx.QueryRow(ctx, `SELECT command,report_sequence,state FROM execution_operations WHERE id=$1 AND server_id=$2 FOR UPDATE`, r.Id, a.ID).Scan(&raw, &seq, &state)
	if err != nil {
		return err
	}
	var c api.ExecutionCommand
	if err = json.Unmarshal(raw, &c); err != nil {
		return err
	}
	if c.Generation != r.Generation || c.PlanHash != r.PlanHash || (a.Role == "build" && c.Kind != "update" && c.Kind != "pipeline") || (a.Role == "deployment" && c.Kind == "pipeline") {
		return domain.Forbidden
	}
	if r.Sequence <= seq {
		return tx.Commit(ctx)
	}
	if Terminal(state) {
		return domain.Conflict
	}
	if c.Kind != "pipeline" && r.Pipeline != nil {
		return domain.Invalid("pipeline")
	}
	if c.Kind == "pipeline" {
		if err = s.pipelineReport(ctx, tx, c, &r); err != nil {
			return err
		}
	}
	if c.Kind != "upgrade" && r.Upgrade != nil {
		return domain.Invalid("upgrade")
	}
	if c.Kind == "upgrade" {
		if err = s.upgradeReport(ctx, tx, c, &r); err != nil {
			return err
		}
	}
	if c.Kind != "visual" && c.Kind != "upgrade" && r.Observation != nil {
		return domain.Invalid("observation")
	}
	if c.Kind == "visual" {
		if err = s.visualReport(ctx, tx, c, &r); err != nil {
			return err
		}
	}
	if err = s.crossHostReport(ctx, tx, c, &r); err != nil {
		return err
	}
	if err = s.networkReport(ctx, tx, c, &r); err != nil {
		return err
	}
	steps, _ := json.Marshal(r.Steps)
	_, err = tx.Exec(ctx, `UPDATE execution_operations SET state=$2,report_sequence=$3,steps=$4,note=$5 WHERE id=$1`, r.Id, r.State, r.Sequence, steps, r.Note)
	if err != nil {
		return err
	}
	if Terminal(r.State) {
		if err = s.enqueuePreviewForEnvironment(ctx, tx, a.Workspace, c.EnvironmentId); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
