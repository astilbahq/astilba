package execution

import (
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// An acceptance where no key is missing erases nothing and is refused, so it cannot carry over
// to a later recovery; none, where none is missing, is no acceptance at all.
func TestRecoveryErasureNeedsAMissingKey(t *testing.T) {
	erase, values, err := recoveryErasure(portability.Archive{}, nil, nil)
	if err != nil || erase != nil || values == nil || len(values) != 0 {
		t.Fatal(erase, values, err)
	}
	var refused *RecoveryKeysMissing
	if _, _, err = recoveryErasure(portability.Archive{}, nil, []uuid.UUID{uuid.New()}); !errors.As(err, &refused) || !strings.Contains(err.Error(), "none is missing") {
		t.Fatal("an acceptance with nothing missing was taken", err)
	}
}
