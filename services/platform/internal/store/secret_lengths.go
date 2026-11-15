package store

import (
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/jackc/pgx/v5/pgtype"
)

// SecretLengths are a value's stored lengths for a new secret version. A version written
// without them stores none, and is measured later rather than taken to be empty.
func SecretLengths(value []byte) (plain, json pgtype.Int4) {
	p, j := domain.SecretLengths(value)
	return pgtype.Int4{Int32: int32(p), Valid: true}, pgtype.Int4{Int32: int32(j), Valid: true}
}
