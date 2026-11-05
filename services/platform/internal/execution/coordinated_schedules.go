package execution

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
	candidates, err := s.Store.Queries.DueCoordinatedBackupPolicies(ctx, now.UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	for _, c := range candidates {
		err = s.runCoordinatedSchedule(ctx, c.WorkspaceID, c.ServerID, c.ID, now)
		var problem *domain.Error
		if err != nil && !errors.As(err, &problem) && !errors.Is(err, pgx.ErrNoRows) && !errors.Is(err, store.SubscriptionRequired) {
			return err
		}
		// A blocked policy retains its cadence cursor, but must yield its scan
		// position so another workspace's due backup can still be considered.
		if err = s.Store.Queries.MarkBackupPolicyChecked(ctx, db.MarkBackupPolicyCheckedParams{WorkspaceID: c.WorkspaceID, ID: c.ID}); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) runCoordinatedSchedule(ctx context.Context, ws, server, id uuid.UUID, now time.Time) error {
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
	q := db.New(tx)
	// Match the server -> policy order used by authenticated schedule reports.
	if err = s.deploymentServer(ctx, tx, ws, server); err != nil {
		return err
	}
	row, err := q.LockCoordinatedBackupPolicy(ctx, db.LockCoordinatedBackupPolicyParams{WorkspaceID: ws, ServerID: server, ID: id})
	if err != nil {
		return err
	}
	var p backup.Policy
	var status backup.Status
	if json.Unmarshal(row.Configuration, &p) != nil || json.Unmarshal(row.Status, &status) != nil || p.Validate() != nil {
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
	destination, err := q.BackupDestinationConfiguration(ctx, db.BackupDestinationConfigurationParams{WorkspaceID: ws, ID: p.DestinationID})
	if err != nil {
		return err
	}
	if err = json.Unmarshal(destination, &b.Installation.Destination); err != nil {
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
	raw, _ := stablejson.Marshal(status)
	if err = q.SaveBackupPolicyStatus(ctx, db.SaveBackupPolicyStatusParams{Status: raw, WorkspaceID: ws, ID: id}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
