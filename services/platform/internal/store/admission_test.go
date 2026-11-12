package store

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

// A mode nobody handles must refuse, not skip the entitlement check.
func TestUnknownAdmissionIsRefused(t *testing.T) {
	for _, mode := range []admission{0, admitRevocation + 1} {
		if mode.admit(context.Background(), nil, uuid.New()) == nil {
			t.Fatalf("admission %d was admitted", mode)
		}
	}
	for _, mode := range []admission{admitBilling, admitRevocation} {
		if err := mode.admit(context.Background(), nil, uuid.New()); err != nil {
			t.Fatalf("admission %d read an entitlement it doesn't need: %v", mode, err)
		}
	}
}
