package execution

import (
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

// recoveryDestinationBinding keeps what both copies of a destination say about the servers given
// its keys, since each server either names was given them: one bound to a server stays bound, and
// one the copies bind to different servers, or either calls shared, is shared.
func recoveryDestinationBinding(selected, prior, incoming portability.Row) error {
	held, err := recoveryValue[*uuid.UUID](prior, "server_id")
	if err != nil {
		return err
	}
	next, err := recoveryValue[*uuid.UUID](incoming, "server_id")
	if err != nil {
		return err
	}
	wasShared, err := recoveryValue[bool](prior, "shared")
	if err != nil {
		return err
	}
	nowShared, err := recoveryValue[bool](incoming, "shared")
	if err != nil {
		return err
	}
	server, shared := held, wasShared || nowShared
	switch {
	case server == nil:
		server = next
	case next != nil && *next != *server:
		shared = true
	}
	if shared {
		server = nil
	}
	recoverySet(selected, "server_id", server)
	recoverySet(selected, "shared", shared)
	return nil
}
