package execution

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// Recovery keeps every server either copy says was given a destination's keys: a binding only
// ever narrows to shared, never back to unbound or to another server.
func TestRecoveryKeepsWhatBothCopiesBindADestinationTo(t *testing.T) {
	a, b := uuid.New(), uuid.New()
	row := func(server *uuid.UUID, shared bool) portability.Row {
		out := portability.Row{}
		recoverySet(out, "server_id", server)
		recoverySet(out, "shared", shared)
		return out
	}
	type binding struct {
		server *uuid.UUID
		shared bool
	}
	for name, c := range map[string]struct{ prior, incoming, want binding }{
		"unbound in both":                     {binding{nil, false}, binding{nil, false}, binding{nil, false}},
		"bound only in the checkpoint":        {binding{nil, false}, binding{&a, false}, binding{&a, false}},
		"bound only in management":            {binding{&a, false}, binding{nil, false}, binding{&a, false}},
		"bound to one server in both":         {binding{&a, false}, binding{&a, false}, binding{&a, false}},
		"bound to different servers":          {binding{&a, false}, binding{&b, false}, binding{nil, true}},
		"shared in the checkpoint":            {binding{&a, false}, binding{nil, true}, binding{nil, true}},
		"shared in management":                {binding{nil, true}, binding{&b, false}, binding{nil, true}},
		"shared in management, unbound later": {binding{nil, true}, binding{nil, false}, binding{nil, true}},
	} {
		prior := row(c.prior.server, c.prior.shared)
		selected := row(c.prior.server, c.prior.shared)
		if err := recoveryDestinationBinding(selected, prior, row(c.incoming.server, c.incoming.shared)); err != nil {
			t.Fatal(name, err)
		}
		want := row(c.want.server, c.want.shared)
		if string(selected["server_id"]) != string(want["server_id"]) || string(selected["shared"]) != string(want["shared"]) {
			t.Errorf("%s: got %s/%s, want %s/%s", name, selected["server_id"], selected["shared"], want["server_id"], want["shared"])
		}
	}
}
