// Package custody holds the keys that open stored values. Only the secret broker and start-up
// wiring may use it, which the secretkeys analyzer enforces: everything else asks the broker,
// which records each use first.
package custody

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"
	"slices"

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

// Classes are every key class, in the order status reports them.
var Classes = []Class{ClassServe, ClassExport, ClassRotate, ClassInstallation}

// ServingDecrypts reports whether the serving process may unwrap under a class. Under rotate it
// may only wrap: only an operator unwraps there.
func ServingDecrypts(class Class) bool { return class != ClassRotate }

type Scope string

const (
	ScopeWorkspace    Scope = "workspace"
	ScopeInstallation Scope = "installation"
	ScopeToken        Scope = "token"
)

// purposes are what each scope's keys are wrapped for, and the class each wrapping is made
// under. Export and recovery have a class of their own, so an operator can switch them off for
// the whole installation; under rotate, serving only wraps, so only an operator moves keys.
var purposes = map[Scope]map[string]Class{
	ScopeWorkspace:    {"seal": ClassServe, "deliver": ClassServe, "operate": ClassServe, "inspect": ClassServe, "copy": ClassServe, "export": ClassExport, "recover": ClassExport, "rotate": ClassRotate},
	ScopeInstallation: {"seal": ClassInstallation, "authenticate": ClassInstallation, "rotate": ClassRotate},
	ScopeToken:        {"token": ClassInstallation, "rotate": ClassRotate},
}

// Purposes lists what a scope's keys are wrapped for, in a fixed order.
func Purposes(scope Scope) []string {
	out := make([]string, 0, len(purposes[scope]))
	for purpose := range purposes[scope] {
		out = append(out, purpose)
	}
	slices.Sort(out)
	return out
}

// ClassFor is the class a scope's key is wrapped under for a purpose, and false for a purpose the
// scope's keys are never wrapped for.
func ClassFor(scope Scope, purpose string) (Class, bool) {
	class, ok := purposes[scope][purpose]
	return class, ok
}

// keyCheckLabel is what a key check value is computed over: a fixed label, so the value shows
// which key a wrapping holds without saying anything about the key.
const keyCheckLabel = "astilba custody key check v1"

// NewKey returns a new random 256-bit key and its key check value.
func NewKey() (key, check []byte, err error) {
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, nil, err
	}
	return key, KeyCheck(key), nil
}

// KeyCheck is HMAC-SHA256 of a fixed label under key. Comparing it after an unwrap shows the
// wrapping held the key its generation was created with, before any value is opened with it.
func KeyCheck(key []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(keyCheckLabel))
	return mac.Sum(nil)
}

// CheckKey reports whether key matches a stored key check value.
func CheckKey(key, check []byte) bool { return hmac.Equal(KeyCheck(key), check) }

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
	// ErrInvalid is a wrapping that does not open under its class and context: another key made
	// it, it was made for another context, or it has been changed.
	ErrInvalid = errors.New("the wrapping does not match its key or context")
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
		return Result{}, ErrInvalid
	}
	return Result{Key: key}, nil
}

func (f *File) Check(context.Context) error { return f.ring.Validate() }

func (f *File) Keyring() *cryptobox.Keyring { return f.ring }

// canaryContext binds a canary to the installation alone.
func canaryContext(installation uuid.UUID) Context {
	return Context{Installation: installation, Scope: ScopeInstallation, Purpose: "canary"}
}

// NewCanary wraps a new random key under a class for the installation. Opening it later shows the
// backend still holds that class's key, and that the database belongs to the installation.
func NewCanary(ctx context.Context, b Backend, class Class, installation uuid.UUID) ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	blob, _, err := b.Wrap(ctx, class, key, canaryContext(installation))
	return blob, err
}

// CheckCanary opens a canary. ErrInvalid means the backend's key for the class did not make it, or
// it was made for another installation.
func CheckCanary(ctx context.Context, b Backend, class Class, installation uuid.UUID, blob []byte) error {
	_, err := b.Unwrap(ctx, class, blob, canaryContext(installation))
	return err
}

// RewrapCanary wraps a canary's key again under the class's current key.
func RewrapCanary(ctx context.Context, b Backend, class Class, installation uuid.UUID, blob []byte) ([]byte, error) {
	return Rewrap(ctx, b, class, blob, canaryContext(installation))
}

// Rewrap wraps a wrapped key again under the class's current key, for the same context.
func Rewrap(ctx context.Context, b Backend, class Class, blob []byte, c Context) ([]byte, error) {
	opened, err := b.Unwrap(ctx, class, blob, c)
	if err != nil {
		return nil, err
	}
	wrapped, _, err := b.Wrap(ctx, class, opened.Key, c)
	clear(opened.Key)
	return wrapped, err
}
