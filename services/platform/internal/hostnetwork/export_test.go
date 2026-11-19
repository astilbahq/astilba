package hostnetwork

import (
	"context"

	"github.com/google/uuid"
)

// OpenKey opens membership id's private key, as applying the network does, for tests outside the
// package that seal with the agent's key store.
func (h Host) OpenKey(ctx context.Context, id uuid.UUID) error {
	_, err := h.privateKey(ctx, id)
	return err
}
