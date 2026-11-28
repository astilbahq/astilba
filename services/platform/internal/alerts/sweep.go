package alerts

import (
	"context"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/sweep"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Sweep claims work before evaluation so an invalid source or a held workspace lock
// cannot starve the fleet.
func (s *Service) Sweep(ctx context.Context) error {
	for range 20 {
		var ws uuid.UUID
		err := s.Store.Pool.QueryRow(ctx, `UPDATE workspaces SET alert_scanned_at=$1 WHERE id=(SELECT id FROM workspaces WHERE alert_scanned_at<$1::timestamptz-interval '30 seconds' ORDER BY alert_scanned_at,id LIMIT 1 FOR UPDATE SKIP LOCKED) RETURNING id`, s.Now()).Scan(&ws)
		if errors.Is(err, pgx.ErrNoRows) {
			break
		}
		if err != nil {
			return err
		}
		_ = sweep.Item(ctx, "alerts", ws, 10*time.Second, func(item context.Context) error { return s.detect(item, ws) })
	}
	return nil
}
func (s *Service) detect(ctx context.Context, ws uuid.UUID) error {
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
	if err = s.operations(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.servers(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.backups(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.networks(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.recoveryWindow(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.dnsWithdrawalHeld(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.backupExposure(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.backupAccount(ctx, tx, ws); err != nil {
		return err
	}
	if err = s.backupPeerUsers(ctx, tx, ws); err != nil {
		return err
	}
	// Active incidents remain visible until observation establishes recovery or monitoring ends.
	if _, err = tx.Exec(ctx, `DELETE FROM operational_alerts WHERE id IN (SELECT id FROM operational_alerts WHERE workspace_id=$1 AND state<>'firing' AND (COALESCE(resolved_at,opened_at)<$2::timestamptz-interval '30 days' OR id IN (SELECT id FROM operational_alerts WHERE workspace_id=$1 AND state<>'firing' ORDER BY opened_at DESC,id DESC OFFSET 1000)) LIMIT 1000)`, ws, s.Now()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
