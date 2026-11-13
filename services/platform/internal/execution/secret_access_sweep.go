package execution

import (
	"context"

	"github.com/riverqueue/river"
)

// secretAccessSweepArgs marks secret access records whose session ended without saying how,
// because its process stopped, as abandoned.
type secretAccessSweepArgs struct{}

func (secretAccessSweepArgs) Kind() string { return "platform_secret_access_sweep_v1" }

type secretAccessSweepWorker struct {
	river.WorkerDefaults[secretAccessSweepArgs]
	service *Service
}

func (w *secretAccessSweepWorker) Work(ctx context.Context, _ *river.Job[secretAccessSweepArgs]) error {
	return w.service.Store.Broker.Sweep(ctx)
}
