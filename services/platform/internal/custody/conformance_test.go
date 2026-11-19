package custody_test

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/custody"
	"github.com/astilbahq/astilba/services/platform/internal/custody/custodytest"
)

// The file keyring passes the conformance suite every backend passes, alone and paired with the
// keyring as a key service is.
func TestFileConformance(t *testing.T) {
	ring, err := cryptobox.New("conformance")
	if err != nil {
		t.Fatal(err)
	}
	file, err := custody.NewFile(ring)
	if err != nil {
		t.Fatal(err)
	}
	tamper := func(blob []byte) []byte { blob[len(blob)-2] ^= 1; return blob }
	t.Run("alone", func(t *testing.T) { custodytest.Conformance(t, file, custodytest.Options{Tamper: tamper}) })
	paired, err := custody.WithKeyring(file, ring)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("paired", func(t *testing.T) { custodytest.Conformance(t, paired, custodytest.Options{Tamper: tamper}) })
}
