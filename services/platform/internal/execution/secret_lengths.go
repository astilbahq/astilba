package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
	"github.com/riverqueue/river"
)

// secretLengthsArgs stores the lengths of secret versions written before lengths were stored,
// or by an older release while rolled back to it, so that planning need not open them.
type secretLengthsArgs struct{}

func (secretLengthsArgs) Kind() string { return "platform_secret_lengths_v1" }

type secretLengthsWorker struct {
	river.WorkerDefaults[secretLengthsArgs]
	service *Service
}

// secretLengthsBatch is one page of versions; a run takes pages until it has gone through every
// version once, and snoozes to continue when its time runs short.
const secretLengthsBatch = 200

// secretLengthsTime leaves a run room within the 10-second job timeout to finish a page.
const secretLengthsTime = 6 * time.Second

func (w *secretLengthsWorker) Work(ctx context.Context, _ *river.Job[secretLengthsArgs]) error {
	more, err := w.service.MeasureSecretLengths(ctx)
	if err == nil && more {
		return river.JobSnooze(time.Second)
	}
	return err
}

// MeasureSecretLengths stores the lengths of versions that have none, until it has gone
// through each once or its time runs short. It reports whether versions remain to look at.
func (s *Service) MeasureSecretLengths(ctx context.Context) (bool, error) {
	deadline := time.Now().Add(secretLengthsTime)
	var after db.UnmeasuredSecretVersionsRow
	for time.Now().Before(deadline) {
		rows, err := s.Store.Queries.UnmeasuredSecretVersions(ctx, db.UnmeasuredSecretVersionsParams{AfterWorkspace: after.WorkspaceID, AfterID: after.ID, Batch: secretLengthsBatch})
		if err != nil || len(rows) == 0 {
			return false, err
		}
		after = rows[len(rows)-1]
		byWorkspace := map[uuid.UUID][]db.UnmeasuredSecretVersionsRow{}
		for _, row := range rows {
			byWorkspace[row.WorkspaceID] = append(byWorkspace[row.WorkspaceID], row)
		}
		for ws, versions := range byWorkspace {
			// One workspace that can't be measured now, such as one past its budget, leaves its
			// versions to planning and to the next run, and holds up no other.
			if err = s.measureSecretVersions(ctx, ws, versions); err != nil {
				slog.Warn("secret lengths not stored", "workspace", ws, "versions", len(versions), "error", err)
			}
		}
		if len(rows) < secretLengthsBatch {
			return false, nil
		}
	}
	return true, nil
}

// measureSecretVersions opens one workspace's versions, under a record naming each, to store
// their lengths. A version that can't be opened is left for planning to refuse, and the others
// are still measured; the record says the session failed.
func (s *Service) measureSecretVersions(ctx context.Context, ws uuid.UUID, versions []db.UnmeasuredSecretVersionsRow) error {
	items := make([]secretbroker.Item, len(versions))
	for i, v := range versions {
		items[i] = secretbroker.Item{Kind: secretbroker.SecretVersion, ID: v.ID}
	}
	keys, err := s.Store.Broker.Begin(ctx, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Inspect, Principal: secretbroker.System("secret-lengths"), Items: items})
	if err != nil {
		return err
	}
	var failed []error
	for _, v := range versions {
		plain, err := keys.Open(secretbroker.Item{Kind: secretbroker.SecretVersion, ID: v.ID}, v.EncryptedValue)
		if err != nil {
			failed = append(failed, err)
			continue
		}
		n, encoded := domain.SecretLengths(plain)
		clear(plain)
		if err = s.Store.Queries.MeasureSecretVersion(ctx, db.MeasureSecretVersionParams{WorkspaceID: ws, ID: v.ID, PlaintextBytes: int32(n), JsonBytes: int32(encoded)}); err != nil {
			failed = append(failed, err)
		}
	}
	err = errors.Join(failed...)
	keys.Close(ctx, secretbroker.Done(err))
	return err
}
