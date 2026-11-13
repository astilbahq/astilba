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
		if err := try(); !errors.Is(err, ErrDenied) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if err := f.Check(ctx); err != nil {
		t.Fatal(err)
	}
}
