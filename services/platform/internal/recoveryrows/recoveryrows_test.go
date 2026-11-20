package recoveryrows

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
)

func row(sealed []byte) []byte {
	return []byte(`{"id":"0b7c1c2e-0000-4000-8000-000000000001","workspace_id":"0b7c1c2e-0000-4000-8000-000000000002","name":"TOKEN","encrypted_value":"\\x` + hex.EncodeToString(sealed) + `","created_at":"2026-11-15T00:00:00Z"}`)
}

// A row's content hash survives its value's key moving, and only that: a different value, or the
// same value sealed again, hashes differently.
func TestContentHashIgnoresOnlyTheKeyWrap(t *testing.T) {
	ring, err := cryptobox.New("content")
	if err != nil {
		t.Fatal(err)
	}
	identity := "pipeline:build-secret:a:b"
	v1, err := ring.Seal([]byte("same"), identity)
	if err != nil {
		t.Fatal(err)
	}
	moved, err := cryptobox.MoveUnder(ring, nil, v1, identity, bytes.Repeat([]byte{7}, 32), cryptobox.ScopeWorkspace, 1)
	if err != nil {
		t.Fatal(err)
	}
	again, err := ring.Seal([]byte("same"), identity)
	if err != nil {
		t.Fatal(err)
	}
	stored, converted, resealed := row(v1), row(moved), row(again)
	if Hash(stored) == Hash(converted) {
		t.Fatal("the stored form ignores the key wrap")
	}
	if ContentHash("build_secret_versions", stored) != ContentHash("build_secret_versions", converted) {
		t.Fatal("moving the value's key changed its content hash")
	}
	if ContentHash("build_secret_versions", stored) == ContentHash("build_secret_versions", resealed) {
		t.Fatal("two seals of an equal value share a content hash")
	}
	// Tables without sealed values, and values that are not envelopes, hash as stored.
	if ContentHash("build_artifacts", stored) != Hash(stored) {
		t.Fatal("a table without sealed values")
	}
	erased := []byte(`{"id":"x","encrypted_value":null}`)
	if ContentHash("secret_versions", erased) != Hash(erased) {
		t.Fatal("an erased value")
	}
}
