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

// secretAccessExpiryArgs removes secret access records older than the installation's retention
// period, and blanks the operator's name and reason in key events as old, a batch at a time.
type secretAccessExpiryArgs struct{}

func (secretAccessExpiryArgs) Kind() string { return "platform_secret_access_expiry_v1" }

type secretAccessExpiryWorker struct {
	river.WorkerDefaults[secretAccessExpiryArgs]
	service *Service
}

// secretAccessExpiryBatch bounds one statement's work; a run takes batches until one comes back
// short, so a backlog clears over a few runs rather than in one long transaction.
const secretAccessExpiryBatch = 1000

func (w *secretAccessExpiryWorker) Work(ctx context.Context, _ *river.Job[secretAccessExpiryArgs]) error {
	q := w.service.Store.Queries
	for _, step := range []func(context.Context, int32) (int32, error){q.ExpireSecretAccess, q.BlankKeyEventOperators} {
		for range 5 {
			n, err := step(ctx, secretAccessExpiryBatch)
			if err != nil {
				return err
			}
			if n < secretAccessExpiryBatch {
				break
			}
		}
	}
	return nil
}
