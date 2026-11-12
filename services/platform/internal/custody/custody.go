// Package custody holds the keys that open stored values. Only the secret broker and start-up
// wiring may use it, which the secretkeys analyzer enforces: everything else asks the broker,
// which records each use first.
package custody

import (
	"context"
	"errors"
	"fmt"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

// Class names the provider key a wrapping is made under. Each class has its own key, so a
// provider's policy can treat them apart.
type Class string

const (
	ClassServe        Class = "serve"
	ClassExport       Class = "export"
	ClassRotate       Class = "rotate"
	ClassInstallation Class = "installation"
)

type Scope string

const (
	ScopeWorkspace    Scope = "workspace"
	ScopeInstallation Scope = "installation"
)

// Context is bound into every wrapping, so a blob opens only for the installation, workspace,
// generation and purpose it was made for.
type Context struct {
	Installation uuid.UUID
	Scope        Scope
	Workspace    uuid.UUID // zero for installation scope
	Generation   int64
	Purpose      string
}

func (c Context) String() string {
	return fmt.Sprintf("custody:v1:%s:%s:%s:%d:%s", c.Installation, c.Scope, c.Workspace, c.Generation, c.Purpose)
}

type Result struct {
	Key               []byte
	ProviderRequestID string
}

var (
	// ErrUnavailable is a backend that cannot be reached; trying again later may work.
	ErrUnavailable = errors.New("key service unavailable")
	// ErrDenied is a key the backend refuses to use, such as a disabled one: never retried.
	ErrDenied = errors.New("key service refused the key")
)

// Backend wraps and unwraps keys under provider keys it alone holds.
type Backend interface {
	ID() string
	Wrap(ctx context.Context, class Class, key []byte, c Context) (blob []byte, requestID string, err error)
	Unwrap(ctx context.Context, class Class, blob []byte, c Context) (Result, error)
	Check(ctx context.Context) error
}

// Legacy is a backend that also holds the installation keyring every value is sealed under
// today, before per-workspace keys exist.
type Legacy interface {
	Backend
	Keyring() *cryptobox.Keyring
}

// File is the self-hosted backend: the keyring file named by ASTILBA_KEYRING. Every class
// uses its active key, as there is only one.
type File struct {
	ring *cryptobox.Keyring
}

func NewFile(ring *cryptobox.Keyring) (*File, error) {
	if err := ring.Validate(); err != nil {
		return nil, err
	}
	return &File{ring}, nil
}

func (f *File) ID() string { return "file:primary" }

func (f *File) Wrap(_ context.Context, class Class, key []byte, c Context) ([]byte, string, error) {
	blob, err := f.ring.Seal(key, c.String()+":"+string(class))
	return blob, "", err
}

func (f *File) Unwrap(_ context.Context, class Class, blob []byte, c Context) (Result, error) {
	key, err := f.ring.Open(blob, c.String()+":"+string(class))
	if err != nil {
		return Result{}, ErrDenied
	}
	return Result{Key: key}, nil
}

func (f *File) Check(context.Context) error { return f.ring.Validate() }

func (f *File) Keyring() *cryptobox.Keyring { return f.ring }
