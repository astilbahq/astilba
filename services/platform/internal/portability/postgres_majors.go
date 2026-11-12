package portability

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/google/uuid"
)

// backfillPostgresMajors records the PostgreSQL major of each data volume an archive from before
// version 7 brought without one, after an import or a recovery merge has set each environment's
// applied revision: the applied revision decides a volume it mounts, and a supported upgrade's
// target takes the major every such upgrade targeted.
func backfillPostgresMajors(ctx context.Context, q *db.Queries, ws uuid.UUID) error {
	if err := q.BackfillPostgresMajors(ctx, ws); err != nil {
		return err
	}
	return q.BackfillUpgradeTargetMajors(ctx, ws)
}
