package store

import (
	"context"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// SpecificationFits is whether a specification is within domain.SpecificationLimit both as sent, its
// canonical JSON, and as specification_revisions' size check measures it, PostgreSQL's jsonb text.
// The jsonb text puts a space after the ':' and ',' between JSON members, so a specification of many
// small fields can fit the first and not the second.
func SpecificationFits(ctx context.Context, q *db.Queries, specification []byte) (bool, error) {
	if len(specification) > domain.SpecificationLimit {
		return false, nil
	}
	size, err := q.SpecificationTextSize(ctx, specification)
	if err != nil {
		return false, err
	}
	return int(size) <= domain.SpecificationLimit, nil
}

// InsertRevision stores a specification revision. Every path that writes one comes here, except a
// workspace import, which copies rows an export already stored within the check. A
// specification over the size limit is refused as invalid before the insert rather than left to the
// table's check: a failed check aborts the transaction, and some callers carry on in it after a
// refused revision, such as an import that records why it failed. The check stays as the backstop.
func InsertRevision(ctx context.Context, q *db.Queries, arg db.InsertRevisionParams) (db.SpecificationRevision, error) {
	fits, err := SpecificationFits(ctx, q, arg.Specification)
	if err != nil {
		return db.SpecificationRevision{}, err
	}
	if !fits {
		return db.SpecificationRevision{}, domain.Invalid("specification")
	}
	return q.InsertRevision(ctx, arg)
}
