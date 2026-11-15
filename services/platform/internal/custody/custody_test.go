package custody

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

// Conformance: a wrapping opens only under the class and context it was made for.
func TestFileWrapsUnderItsContext(t *testing.T) {
	ring, err := cryptobox.New("test")
	if err != nil {
		t.Fatal(err)
	}
	f, err := NewFile(ring)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	key := bytes.Repeat([]byte{7}, 32)
	c := Context{Installation: uuid.New(), Scope: ScopeWorkspace, Workspace: uuid.New(), Generation: 1, Purpose: "deliver"}
	blob, _, err := f.Wrap(ctx, ClassServe, key, c)
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.Unwrap(ctx, ClassServe, blob, c)
	if err != nil || !bytes.Equal(got.Key, key) {
		t.Fatalf("unwrap: %v", err)
	}
	other := c
	other.Workspace = uuid.New()
	for name, try := range map[string]func() error{
		"class":      func() error { _, err := f.Unwrap(ctx, ClassExport, blob, c); return err },
		"workspace":  func() error { _, err := f.Unwrap(ctx, ClassServe, blob, other); return err },
		"generation": func() error { o := c; o.Generation = 2; _, err := f.Unwrap(ctx, ClassServe, blob, o); return err },
		"purpose":    func() error { o := c; o.Purpose = "export"; _, err := f.Unwrap(ctx, ClassServe, blob, o); return err },
	} {
		if err := try(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.Check(ctx); err != nil {
		t.Fatal(err)
	}
}

// A canary opens only for the key and installation that made it, and a rewrap keeps its key.
func TestCanaryBindsKeyAndInstallation(t *testing.T) {
	ctx := context.Background()
	ring, _ := cryptobox.New("one")
	f, err := NewFile(ring)
	if err != nil {
		t.Fatal(err)
	}
	installation := uuid.New()
	blob, err := NewCanary(ctx, f, ClassServe, installation)
	if err != nil {
		t.Fatal(err)
	}
	if err = CheckCanary(ctx, f, ClassServe, installation, blob); err != nil {
		t.Fatal(err)
	}
	other, _ := cryptobox.New("one")
	g, _ := NewFile(other)
	for name, err := range map[string]error{
		"installation": CheckCanary(ctx, f, ClassServe, uuid.New(), blob),
		"class":        CheckCanary(ctx, f, ClassExport, installation, blob),
		"keyring":      CheckCanary(ctx, g, ClassServe, installation, blob),
	} {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: %v", name, err)
		}
	}
	rewrapped, err := RewrapCanary(ctx, f, ClassServe, installation, blob)
	if err != nil || bytes.Equal(rewrapped, blob) {
		t.Fatalf("rewrap: %v", err)
	}
	if err = CheckCanary(ctx, f, ClassServe, installation, rewrapped); err != nil {
		t.Fatal(err)
	}
}

// Each purpose is wrapped under its class: export and recovery under their own, rotate under the
// one serving only wraps with, and the installation's keys under the installation class.
func TestPurposesAndClasses(t *testing.T) {
	for _, c := range []struct {
		scope   Scope
		purpose string
		class   Class
	}{
		{ScopeWorkspace, "seal", ClassServe}, {ScopeWorkspace, "deliver", ClassServe}, {ScopeWorkspace, "export", ClassExport},
		{ScopeWorkspace, "recover", ClassExport}, {ScopeWorkspace, "rotate", ClassRotate}, {ScopeInstallation, "authenticate", ClassInstallation},
		{ScopeInstallation, "seal", ClassInstallation}, {ScopeInstallation, "rotate", ClassRotate}, {ScopeToken, "token", ClassInstallation}, {ScopeToken, "rotate", ClassRotate},
	} {
		if class, ok := ClassFor(c.scope, c.purpose); !ok || class != c.class {
			t.Errorf("%s %s: %s %v", c.scope, c.purpose, class, ok)
		}
	}
	for _, c := range [][2]string{{"workspace", "authenticate"}, {"installation", "deliver"}, {"token", "seal"}} {
		if _, ok := ClassFor(Scope(c[0]), c[1]); ok {
			t.Errorf("%s keys wrapped for %s", c[0], c[1])
		}
	}
	if len(Purposes(ScopeWorkspace)) != 8 || len(Purposes(ScopeInstallation)) != 3 || len(Purposes(ScopeToken)) != 2 {
		t.Fatal(Purposes(ScopeWorkspace), Purposes(ScopeInstallation), Purposes(ScopeToken))
	}
}

func TestKeyCheckValues(t *testing.T) {
	key, check, err := NewKey()
	if err != nil || len(key) != 32 || len(check) != 32 || !CheckKey(key, check) {
		t.Fatal(err)
	}
	other, _, _ := NewKey()
	if CheckKey(other, check) {
		t.Fatal("another key matched")
	}
}
