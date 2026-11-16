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
