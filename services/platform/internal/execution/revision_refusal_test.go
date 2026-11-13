package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/jackc/pgx/v5/pgconn"
)

// A background writer stops only on a refusal that every retry would repeat, and its note names
// none of the configuration. Anything else is left to fail the job, so a later attempt can pass.
func TestRefusalStopsOnlyWhatARetryWouldRepeat(t *testing.T) {
	for _, c := range []struct {
		err  error
		stop bool
	}{
		{domain.SpecificationTooLarge, true},
		{fmt.Errorf("saving: %w", domain.SpecificationTooLarge), true},
		{domain.Invalid("specification.applications.source.image"), true},
		{&composeproject.Diagnostic{Path: "secret-name.yaml", Line: 3, Column: 7, Message: "image is interpolated from SECRET_VALUE"}, true},
		{&pgconn.PgError{Code: "22021", Message: "invalid byte sequence: SECRET_VALUE"}, true},
		{fmt.Errorf("saving: %w", &pgconn.PgError{Code: "22P05"}), true},
		{&pgconn.PgError{Code: "23505"}, false},
		{domain.Conflict, false},
		{domain.Forbidden, false},
		{context.Canceled, false},
		{errors.New("connection reset"), false},
	} {
		note := refusal(c.err)
		if (note != "") != c.stop {
			t.Errorf("%v: refusal note %q", c.err, note)
		}
		for _, configuration := range []string{"secret-name", "SECRET_VALUE", "source.image"} {
			if c.stop && strings.Contains(note, configuration) {
				t.Errorf("%v: the note %q quotes the configuration", c.err, note)
			}
		}
	}
}
