// Package brokertest gives unit tests a secret broker that keeps its records and keys in memory.
package brokertest

import (
	"context"
	"sync"
	"testing"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/google/uuid"
)

// Records is what a test broker recorded, and the keys it made.
type Records struct {
	backend      custody.Legacy
	installation uuid.UUID

	mu       sync.Mutex
	Accesses []db.InsertSecretAccessParams
	Outcomes []db.InsertSecretAccessOutcomeParams
	keys     map[string][]byte
}

// New returns a broker under a new keyring, in which every account is an owner of every
// workspace, every server belongs to it, and every workspace and the installation have one
// active key generation, made when a session first asks for it.
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
	r := &Records{backend: backend, installation: uuid.New(), keys: map[string][]byte{}}
	return secretbroker.NewDetached(r, backend, r.installation, nil), r
}

func (r *Records) SecretAccessMember(context.Context, db.SecretAccessMemberParams) (string, error) {
	return "owner", nil
}
func (r *Records) SecretAccessServer(context.Context, db.SecretAccessServerParams) (bool, error) {
	return true, nil
}
func (r *Records) SecretAccessWorkspaceExists(context.Context, uuid.UUID) (bool, error) {
	return false, nil
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

// generation returns a holder's generation 1, wrapped for purpose: uuid.Nil is the installation's.
func (r *Records) generation(ctx context.Context, scope custody.Scope, holder uuid.UUID, purpose string) (kcv, wrapped []byte, err error) {
	name := string(scope) + ":" + holder.String()
	r.mu.Lock()
	key, ok := r.keys[name]
	if !ok {
		if key, _, err = custody.NewKey(); err != nil {
			r.mu.Unlock()
			return nil, nil, err
		}
		r.keys[name] = key
	}
	r.mu.Unlock()
	kcv = custody.KeyCheck(key)
	class, _ := custody.ClassFor(scope, purpose)
	wrapped, _, err = r.backend.Wrap(ctx, class, key, custody.Context{Installation: r.installation, Scope: scope, Workspace: holder, Generation: 1, Purpose: purpose})
	return kcv, wrapped, err
}

func (r *Records) WorkspaceKeyWrappings(ctx context.Context, arg db.WorkspaceKeyWrappingsParams) ([]db.WorkspaceKeyWrappingsRow, error) {
	kcv, wrapped, err := r.generation(ctx, custody.ScopeWorkspace, arg.WorkspaceID, arg.Purpose)
	if err != nil {
		return nil, err
	}
	return []db.WorkspaceKeyWrappingsRow{{Generation: 1, State: "active", Sealable: true, Kcv: kcv, Wrapped: wrapped}}, nil
}

// EnvironmentKeys is none: no value is sealed under an environment's key here.
func (r *Records) EnvironmentKeys(context.Context, uuid.UUID) ([]db.EnvironmentKeysRow, error) {
	return nil, nil
}

func (r *Records) InstallationKeyWrappings(ctx context.Context, arg db.InstallationKeyWrappingsParams) ([]db.InstallationKeyWrappingsRow, error) {
	kcv, wrapped, err := r.generation(ctx, custody.Scope(arg.Scope), uuid.Nil, arg.Purpose)
	if err != nil {
		return nil, err
	}
	return []db.InstallationKeyWrappingsRow{{Generation: 1, State: "active", Sealable: true, Kcv: kcv, Wrapped: wrapped}}, nil
}

// Fence stands for the transaction a test stores sealed values in: the generation it seals under
// stays sealable.
type Fence struct{}

func (Fence) ShareSealableWorkspaceKey(_ context.Context, arg db.ShareSealableWorkspaceKeyParams) (int64, error) {
	return arg.Generation, nil
}
func (Fence) ShareSealableInstallationKey(_ context.Context, arg db.ShareSealableInstallationKeyParams) (int64, error) {
	return arg.Generation, nil
}
func (Fence) WorkspaceHasKeys(context.Context, uuid.UUID) (bool, error) { return true, nil }
func (Fence) ShareEnvironmentKey(_ context.Context, arg db.ShareEnvironmentKeyParams) (int64, error) {
	return arg.Generation, nil
}
func (Fence) InstallationScopeHasKeys(context.Context, string) (bool, error) { return true, nil }
