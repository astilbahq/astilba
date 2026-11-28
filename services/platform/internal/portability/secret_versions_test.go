package portability

import (
	"testing"

	"github.com/google/uuid"
)

// Every ID a carried row writes counts, in any column and any shape, but an audit event's doesn't.
func TestNamedIDs(t *testing.T) {
	alone, nested, prefixed := uuid.New(), uuid.New(), uuid.New()
	raw := []byte(`{"specification":{"variables":[{"secretVersionId":"` + alone.String() + `"}]},"command":"{\"plan\":{\"id\":\"` + nested.String() + `\"}}","note":"secret:` + prefixed.String() + `"}`)
	named := map[uuid.UUID]struct{}{}
	collectNamedIDs("execution_operations", raw, named)
	if len(named) != 3 {
		t.Fatal("named", named)
	}
	for _, id := range []uuid.UUID{alone, nested, prefixed} {
		if _, ok := named[id]; !ok {
			t.Fatal("an ID a row names wasn't found", id)
		}
	}
	audit := map[uuid.UUID]struct{}{}
	collectNamedIDs("audit_events", []byte(`{"resource_id":"`+alone.String()+`"}`), audit)
	if len(audit) != 0 {
		t.Fatal("an audit event kept the version it records")
	}
}
