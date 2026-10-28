package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

type coordinatedScheduleArgs struct{}

func (coordinatedScheduleArgs) Kind() string { return "platform_coordinated_schedule_v1" }

type coordinatedScheduleWorker struct {
	river.WorkerDefaults[coordinatedScheduleArgs]
	service *Service
}

func (w *coordinatedScheduleWorker) Work(ctx context.Context, _ *river.Job[coordinatedScheduleArgs]) error {
	return w.service.runCoordinatedSchedules(ctx, time.Now().UTC())
}

func (s *Service) runCoordinatedSchedules(ctx context.Context, now time.Time) error {
	rows, err := s.Store.Pool.Query(ctx, `SELECT workspace_id,server_id,id FROM backup_policies
	 WHERE configuration->>'coordinated'='true' AND configuration->>'enabled'='true'
	 AND status->>'enabled'='true' AND status->>'generation'=configuration->>'generation'
	 AND status->>'next'>'0001-01-01T00:00:00Z' AND status->>'next'<=$1
	 ORDER BY coordinated_checked_at,id LIMIT 100`, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	type candidate struct{ workspace, server, policy uuid.UUID }
	var candidates []candidate
	for rows.Next() {
		var c candidate
		if err = rows.Scan(&c.workspace, &c.server, &c.policy); err != nil {
			rows.Close()
			return err
		}
		candidates = append(candidates, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range candidates {
		err = s.runCoordinatedSchedule(ctx, c.workspace, c.server, c.policy, now)
		var problem *domain.Error
		if err != nil && !errors.As(err, &problem) && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, store.SubscriptionRequired) {
			return err
		}
		// A blocked policy retains its cadence cursor, but must yield its scan
		// position so another workspace's due backup can still be considered.
		if _, err = s.Store.Pool.Exec(ctx, `UPDATE backup_policies SET coordinated_checked_at=clock_timestamp() WHERE workspace_id=$1 AND id=$2`, c.workspace, c.policy); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runCoordinatedSchedule(ctx context.Context, ws, server, id uuid.UUID, now time.Time) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM workspaces WHERE id=$1 FOR UPDATE`, ws); err != nil {
		return err
	}
	// Match the server -> policy order used by authenticated schedule reports.
	if err = s.deploymentServer(ctx, tx, ws, server); err != nil {
		return err
	}
	var configuration, raw []byte
	if err = tx.QueryRow(ctx, `SELECT configuration,status FROM backup_policies WHERE workspace_id=$1 AND server_id=$2 AND id=$3 FOR UPDATE`, ws, server, id).Scan(&configuration, &raw); err != nil {
		return err
	}
	var p backup.Policy
	var status backup.Status
	if json.Unmarshal(configuration, &p) != nil || json.Unmarshal(raw, &status) != nil || p.Validate() != nil {
		return domain.Invalid("policy")
	}
	if !p.Coordinated || !p.RequiresQuiescence() || !p.Enabled || !status.Enabled || status.PolicyID != p.ID || status.Generation != p.Generation || status.Next.IsZero() || now.Before(status.Next) {
		return nil
	}
	for _, run := range status.Runs {
		if run.State == "running" || run.State == "needs_attention" {
			return domain.Conflict
		}
	}
	at, next, err := p.Schedule.Due(status.Next, now)
	if err != nil {
		return domain.Invalid("schedule.next")
	}
	b := backup.Command{Action: "backup", Installation: backup.Installation{Policy: p}, ScheduledAt: &at, RunID: backup.RunID(p, at)}
	if err = tx.QueryRow(ctx, `SELECT configuration FROM backup_destinations WHERE workspace_id=$1 AND id=$2`, ws, p.DestinationID).Scan(&raw); err != nil {
		return err
	}
	if err = json.Unmarshal(raw, &b.Installation.Destination); err != nil {
		return domain.Invalid("backup.destination")
	}
	if err = requireGeneralSchedule(ctx, tx, ws, server, p); err != nil {
		return err
	}
	c := (maintenanceEffect{Backup: &b}).command("")
	if err = s.commandAdmission(ctx, tx, ws, c); err != nil {
		return err
	}
	// Cursor and delivery are one transaction. A failed occurrence is recorded,
	// never silently retried as another snapshot; missed slots coalesce to one.
	if _, err = s.insertBackupMaintenance(ctx, tx, ws, uuid.New(), c, b); err != nil {
		return err
	}
	status.Next = next
	raw, _ = json.Marshal(status)
	if _, err = tx.Exec(ctx, `UPDATE backup_policies SET status=$2 WHERE workspace_id=$1 AND id=$3`, ws, raw, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
