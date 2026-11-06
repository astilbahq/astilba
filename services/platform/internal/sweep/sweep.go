// Package sweep holds the rule for background passes that span every workspace: one
// workspace's data or held lock can cost it its turn, and each other workspace no more
// than that turn's deadline. Sweep capacity is still shared, so many failing workspaces
// add up.
//
// A pass claims each item by moving its scan cursor in a statement of its own before
// doing any work, so a failure, a timeout or a crash cannot leave the item at the head
// of the scan. It then runs the item through Item and carries on whatever the result.
package sweep

import (
	"context"
	"log/slog"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/diagnostic"
	"github.com/google/uuid"
)

// Item runs one workspace's share of a pass under its own deadline and logs a failure
// against that workspace. Its error is for the caller to record, not to stop the pass.
// An item cut short because the whole pass ended says nothing about the workspace, so
// callers check ctx before recording, and it is not logged.
func Item(ctx context.Context, name string, workspace uuid.UUID, limit time.Duration, work func(context.Context) error) error {
	item, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	err := work(item)
	if err != nil && ctx.Err() == nil {
		slog.Error("background sweep item failed", "sweep", name, "workspace", workspace, diagnostic.Failure(err))
	}
	return err
}
