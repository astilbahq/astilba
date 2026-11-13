package store

import (
	"errors"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// Text PostgreSQL cannot store is refused, not answered as a failure, and the cause is kept for
// the log.
func TestNormalizeRefusesUnstorableText(t *testing.T) {
	for _, code := range []string{"22021", "22P05"} {
		cause := &pgconn.PgError{Code: code}
		err := Normalize(cause)
		var refused *domain.Error
		var kept *pgconn.PgError
		if !errors.Is(err, domain.UnstorableText) || !errors.As(err, &refused) || refused.Code != "invalid_input" || !errors.As(err, &kept) || kept != cause {
			t.Errorf("%s was normalized to %v", code, err)
		}
	}
	if err := Normalize(&pgconn.PgError{Code: "22001"}); errors.Is(err, domain.UnstorableText) {
		t.Error("a value too long was answered as unstorable text")
	}
}
