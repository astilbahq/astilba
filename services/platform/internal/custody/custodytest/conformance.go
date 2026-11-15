// Package custodytest holds the conformance suite every custody backend passes: the file keyring
// always, and each key service against a real server.
package custodytest

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/google/uuid"
)

// Options says what a backend promises beyond the suite every backend passes.
type Options struct {
	// RequestIDs is a backend whose provider names each request, which the broker records.
	RequestIDs bool
	// Tamper changes a wrapping so that it no longer authenticates, keeping its shape.
	Tamper func(blob []byte) []byte
}

// Conformance checks a backend that may wrap and unwrap under every class.
func Conformance(t *testing.T, b custody.Backend, o Options) {
	t.Helper()
	ctx := t.Context()
	base := custody.Context{Installation: uuid.New(), Scope: custody.ScopeWorkspace, Workspace: uuid.New(), Generation: 3, Purpose: "deliver"}
	key, _, err := custody.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	wrap := func(class custody.Class, c custody.Context) []byte {
		t.Helper()
		blob, id, err := b.Wrap(ctx, class, key, c)
		if err != nil {
			t.Fatalf("wrap under %s: %v", class, err)
		}
		if o.RequestIDs && id == "" {
			t.Fatalf("wrap under %s returned no provider request ID", class)
		}
		if bytes.Contains(blob, key) {
			t.Fatalf("the wrapping under %s holds the key", class)
		}
		return blob
	}

	t.Run("round trip under every class", func(t *testing.T) {
		for _, class := range custody.Classes {
			blob := wrap(class, base)
			got, err := b.Unwrap(ctx, class, blob, base)
			if err != nil || !bytes.Equal(got.Key, key) {
				t.Fatalf("unwrap under %s: %v", class, err)
			}
			if o.RequestIDs && got.ProviderRequestID == "" {
				t.Fatalf("unwrap under %s returned no provider request ID", class)
			}
			again := wrap(class, base)
			if bytes.Equal(again, blob) {
				t.Fatalf("two wrappings under %s are the same bytes", class)
			}
		}
	})

	t.Run("every context field is bound", func(t *testing.T) {
		blob := wrap(custody.ClassServe, base)
		installation := custody.Context{Installation: uuid.Nil, Scope: custody.ScopeInstallation, Generation: 3, Purpose: "deliver"}
		for name, c := range map[string]custody.Context{
			"installation": with(base, func(c *custody.Context) { c.Installation = uuid.New() }),
			"scope":        with(base, func(c *custody.Context) { c.Scope = custody.ScopeToken }),
			"workspace":    with(base, func(c *custody.Context) { c.Workspace = uuid.New() }),
			"no workspace": with(installation, func(c *custody.Context) { c.Installation = base.Installation }),
			"generation":   with(base, func(c *custody.Context) { c.Generation = 4 }),
			"purpose":      with(base, func(c *custody.Context) { c.Purpose = "operate" }),
		} {
			if _, err := b.Unwrap(ctx, custody.ClassServe, blob, c); !errors.Is(err, custody.ErrInvalid) {
				t.Errorf("%s: %v", name, err)
			}
		}
	})

	t.Run("a wrapping opens only under its class", func(t *testing.T) {
		for _, from := range custody.Classes {
			blob := wrap(from, base)
			for _, to := range custody.Classes {
				if to == from {
					continue
				}
				if _, err := b.Unwrap(ctx, to, blob, base); !errors.Is(err, custody.ErrInvalid) {
					t.Errorf("%s wrapping under %s: %v", from, to, err)
				}
			}
		}
	})

	t.Run("a changed wrapping does not open", func(t *testing.T) {
		blob := wrap(custody.ClassServe, base)
		changed := []([]byte){nil, {}, []byte("not a wrapping"), blob[:len(blob)/2]}
		if o.Tamper != nil {
			changed = append(changed, o.Tamper(bytes.Clone(blob)))
		}
		for i, c := range changed {
			if _, err := b.Unwrap(ctx, custody.ClassServe, c, base); !errors.Is(err, custody.ErrInvalid) {
				t.Errorf("change %d: %v", i, err)
			}
		}
	})

	t.Run("rewrap keeps the key and the context", func(t *testing.T) {
		for _, class := range custody.Classes {
			blob := wrap(class, base)
			rewrapped, err := custody.Rewrap(ctx, b, class, blob, base)
			if err != nil {
				t.Fatalf("rewrap under %s: %v", class, err)
			}
			got, err := b.Unwrap(ctx, class, rewrapped, base)
			if err != nil || !bytes.Equal(got.Key, key) {
				t.Fatalf("unwrap the rewrapped %s wrapping: %v", class, err)
			}
			if _, err = b.Unwrap(ctx, class, rewrapped, with(base, func(c *custody.Context) { c.Generation = 9 })); !errors.Is(err, custody.ErrInvalid) {
				t.Fatalf("the rewrapped %s wrapping opened for another context: %v", class, err)
			}
		}
	})

	t.Run("a cancelled call returns at once", func(t *testing.T) {
		blob := wrap(custody.ClassServe, base)
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		start := time.Now()
		_, err := b.Unwrap(cancelled, custody.ClassServe, blob, base)
		if time.Since(start) > time.Second {
			t.Fatalf("a cancelled unwrap took %s", time.Since(start))
		}
		// A backend that needs no network may finish the call; one that does reports it could not.
		if err != nil && !errors.Is(err, custody.ErrUnavailable) {
			t.Fatalf("a cancelled unwrap: %v", err)
		}
	})

	t.Run("check", func(t *testing.T) {
		if err := b.Check(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

func with(c custody.Context, change func(*custody.Context)) custody.Context {
	change(&c)
	return c
}
