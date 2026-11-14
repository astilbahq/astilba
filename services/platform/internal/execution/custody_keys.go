package execution

import (
	"context"
	"log/slog"
	"time"

	"github.com/riverqueue/river"
)

// custodyKeysArgs makes the keys that workspaces and the installation do not have yet: those of
// workspaces created before keys existed, or by an older release after a rollback. It runs at
// start and then periodically on every instance; instances running it at once each skip what
// another is making.
type custodyKeysArgs struct{}

func (custodyKeysArgs) Kind() string { return "platform_custody_keys_v1" }

type custodyKeysWorker struct {
	river.WorkerDefaults[custodyKeysArgs]
	service *Service
}

// custodyKeysTime leaves a run room within the 10-second job timeout to finish the key it is
// making.
const custodyKeysTime = 6 * time.Second

func (w *custodyKeysWorker) Work(ctx context.Context, _ *river.Job[custodyKeysArgs]) error {
	deadline := time.Now().Add(custodyKeysTime)
	more, err := w.service.Store.Broker.InitKeys(ctx, func() bool { return time.Now().Before(deadline) })
	if err != nil {
		return err
	}
	if !more {
		return nil
	}
	if progress, err := w.service.Store.Queries.KeyProgress(ctx); err == nil {
		slog.Info("creating keys", "workspacesWithKeys", progress.WithKeys, "workspaces", progress.Workspaces, "installationKey", progress.Installation, "tokenKey", progress.Token)
	}
	return river.JobSnooze(time.Second)
}
