package secretbroker

import (
	"context"
	"strconv"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// DestroyEnvironmentKeys destroys an environment's keys in q, the transaction that purges the
// environment or ends its preview, and records each in the installation's key history. Every
// copy of a value sealed under them, wherever it is kept, no longer opens. A writer holding a key
// to store a value under it finishes first, and none stores one after. An environment with no key
// has nothing to destroy.
func (b *Broker) DestroyEnvironmentKeys(ctx context.Context, q *db.Queries, workspace, environment uuid.UUID, reason string) error {
	generations, err := q.DestroyEnvironmentKeys(ctx, db.DestroyEnvironmentKeysParams{WorkspaceID: workspace, EnvironmentID: environment})
	if err != nil {
		return err
	}
	run := uuid.New()
	for _, generation := range generations {
		if err = b.event(ctx, q, keyEvent{run: run, kind: "environment-key-destroyed", reason: reason, scope: custody.ScopeWorkspace, workspace: &workspace, details: map[string]string{
			"environment": environment.String(), "generation": strconv.FormatInt(generation, 10),
		}}); err != nil {
			return err
		}
	}
	return nil
}

// DestroyEndedEnvironmentKeys destroys, in q, the keys of a workspace's environments that are
// purging or purged, or are previews whose every request has ended, while any is still active.
// Recovery runs it after merging, since a checkpoint can bring a purge or end a preview that
// management's database has not seen, such as one restored from a backup taken before.
func (b *Broker) DestroyEndedEnvironmentKeys(ctx context.Context, q *db.Queries, workspace uuid.UUID) error {
	ended, err := q.EndedEnvironmentKeys(ctx, workspace)
	if err != nil {
		return err
	}
	for _, e := range ended {
		reason := "preview ended by recovery"
		if e.Purged {
			reason = "environment purge taken by recovery"
		}
		if err = b.DestroyEnvironmentKeys(ctx, q, workspace, e.EnvironmentID, reason); err != nil {
			return err
		}
	}
	return nil
}
