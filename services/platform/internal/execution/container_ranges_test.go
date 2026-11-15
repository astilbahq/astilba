package execution

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

// A staging's record of its routes is read strictly: anything but route IDs to allowlists of
// masked prefixes, within a route's bounds, leaves its routes unknown, which planning reviews.
func TestStagedRoutesAreReadStrictly(t *testing.T) {
	route := uuid.New().String()
	many := `{`
	for i := range maxStagedRoutes + 1 {
		if i > 0 {
			many += ","
		}
		many += `"` + uuid.New().String() + `":["203.0.113.7/32"]`
	}
	many += `}`
	for _, c := range []struct {
		name, recorded          string
		routes                  int
		unresolved, unconfirmed bool
	}{
		{"routes", `{"` + route + `":["203.0.113.7/32","2001:db8::/48"]}`, 1, false, false},
		{"no routes", `{}`, 0, false, false},
		{"a clone's unconfirmed copy", unconfirmedStaging, 0, false, true},
		{"not an identity", `{"route":["203.0.113.7/32"]}`, 0, true, false},
		{"an identity not written as one", `{"` + strings.ToUpper(route) + `":["203.0.113.7/32"]}`, 0, true, false},
		{"host bits", `{"` + route + `":["203.0.113.7/24"]}`, 0, true, false},
		{"IPv4-mapped", `{"` + route + `":["::ffff:10.0.0.0/104"]}`, 0, true, false},
		{"no allowlist", `{"` + route + `":[]}`, 0, true, false},
		{"too many entries", `{"` + route + `":[` + strings.Repeat(`"203.0.113.7/32",`, 32) + `"203.0.113.8/32"]}`, 0, true, false},
		{"too many routes", many, 0, true, false},
		{"another shape", `["` + route + `"]`, 0, true, false},
	} {
		got := readStagingRoutes([]byte(c.recorded), nil, nil)
		if len(got.staged) != c.routes || got.unresolved != c.unresolved || got.unconfirmed != c.unconfirmed {
			t.Errorf("%s: %+v", c.name, got)
		}
	}
	// A staging from before the record is worked out from its revision, which must be readable.
	if got := readStagingRoutes(nil, []byte(`{}`), nil); !got.unresolved {
		t.Error("a staging without its revision was read as having no routes")
	}
}
