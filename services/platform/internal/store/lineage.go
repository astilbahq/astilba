package store

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
)

// EndLineage ends a browser session lineage in the caller's transaction: every token that has
// renewed it since its sign-in, so a logout or a sign-out from another session ends what a
// concurrent renewal made of it. The lineage lock orders this with that renewal: one that
// committed first has its new token deleted here, and one that comes after finds its old token
// gone and refuses. A later sign-in stays signed in, in a lineage of its own, even one completing
// a pending sign-in this delete ended. An authenticator enrolment the lineage began ends with it,
// unless another transaction holds the account's row, when BeginMFA lets go of it once it finds
// the lineage signed out. Only an ending that has one to free writes to accounts, so a logout
// never waits for a hold on that table, such as a custody conversion's.
func EndLineage(ctx context.Context, q *db.Queries, lineage uuid.UUID) error {
	if err := q.LockSessionLineage(ctx, lineage); err != nil {
		return err
	}
	enrolling, err := q.LockLineageEnrolments(ctx, lineage)
	if err != nil {
		return err
	}
	if len(enrolling) > 0 {
		if err = q.EndEnrolments(ctx, enrolling); err != nil {
			return err
		}
	}
	return q.DeleteSessionLineage(ctx, lineage)
}
