// Package brokertest gives unit tests a secret broker that keeps its records in memory.
package brokertest

import (
	"context"
	"sync"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
)

// Records is what a test broker recorded.
type Records struct {
	mu       sync.Mutex
	Accesses []db.InsertSecretAccessParams
	Outcomes []db.InsertSecretAccessOutcomeParams
}

// New returns a broker under a new keyring, in which every account is an owner of every
// workspace and every server belongs to it.
func New(t testing.TB) (*secretbroker.Broker, *Records) {
	t.Helper()
	ring, err := cryptobox.New("test")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := custody.NewFile(ring)
	if err != nil {
		t.Fatal(err)
	}
	r := &Records{}
	return secretbroker.New(r, backend, nil), r
}

func (r *Records) SecretAccessMember(context.Context, db.SecretAccessMemberParams) (string, error) {
	return "owner", nil
}
func (r *Records) SecretAccessServer(context.Context, db.SecretAccessServerParams) (bool, error) {
	return true, nil
}
func (r *Records) InsertSecretAccess(_ context.Context, arg db.InsertSecretAccessParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Accesses = append(r.Accesses, arg)
	return nil
}
func (r *Records) InsertSecretAccessOutcome(_ context.Context, arg db.InsertSecretAccessOutcomeParams) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Outcomes = append(r.Outcomes, arg)
	return nil
}
func (r *Records) AbandonSecretAccess(context.Context, float64) (int64, error) { return 0, nil }
