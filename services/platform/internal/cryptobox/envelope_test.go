package cryptobox

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestEnvelopeV2OpensOnlyUnderItsKeyScopeAndGeneration(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := SealUnder(key, ScopeWorkspace, 3, []byte("value"), "secret:v1:a:b")
	if err != nil {
		t.Fatal(err)
	}
	header, err := ReadHeader(sealed)
	if err != nil || header != (Header{Version: 2, Scope: ScopeWorkspace, Generation: 3}) {
		t.Fatal(header, err)
	}
	if value, err := OpenUnder(key, sealed, "secret:v1:a:b"); err != nil || string(value) != "value" {
		t.Fatal(string(value), err)
	}
	other := bytes.Repeat([]byte{8}, 32)
	if _, err = OpenUnder(other, sealed, "secret:v1:a:b"); !errors.Is(err, ErrKey) {
		t.Fatal("opened under another key", err)
	}
	if _, err = OpenUnder(key, sealed, "secret:v1:a:c"); !errors.Is(err, ErrKey) {
		t.Fatal("opened under another identity", err)
	}
	// The scope and generation are bound into the data key's wrapping.
	for _, changed := range []string{
		strings.Replace(string(sealed), `"generation":3`, `"generation":4`, 1),
		strings.Replace(string(sealed), `"scope":"workspace"`, `"scope":"installation"`, 1),
	} {
		if changed == string(sealed) {
			t.Fatal("nothing changed")
		}
		if _, err = OpenUnder(key, []byte(changed), "secret:v1:a:b"); !errors.Is(err, ErrKey) {
			t.Fatal("a changed header opened", err)
		}
	}
	// The keyring does not open it, nor does a v2 reader open v1.
	ring, _ := New("k")
	if _, err = ring.Open(sealed, "secret:v1:a:b"); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	legacy, _ := ring.Seal([]byte("value"), "secret:v1:a:b")
	if _, err = OpenUnder(key, legacy, "secret:v1:a:b"); !errors.Is(err, ErrKey) {
		t.Fatal(err)
	}
	if header, err = ReadHeader(legacy); err != nil || header != (Header{Version: 1, KeyID: "k"}) {
		t.Fatal(header, err)
	}
}

// The fields kept for per-environment keys are refused until the release that reads them, as is
// anything else this release does not know.
func TestEnvelopeV2RefusesReservedAndUnknownFields(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := SealUnder(key, ScopeWorkspace, 1, []byte("value"), "id")
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string]string{
		"holder":            strings.Replace(string(sealed), `"version":2`, `"holder":"11111111-1111-4111-8111-111111111111","version":2`, 1),
		"environment scope": strings.Replace(string(sealed), `"scope":"workspace"`, `"scope":"environment"`, 1),
		"unknown member":    strings.Replace(string(sealed), `"version":2`, `"keyId":"k","version":2`, 1),
		"generation 0":      strings.Replace(string(sealed), `"generation":1`, `"generation":0`, 1),
		"version 3":         strings.Replace(string(sealed), `"version":2`, `"version":3`, 1),
	} {
		if blob == string(sealed) {
			t.Fatal(name, "unchanged")
		}
		if _, err := ReadHeader([]byte(blob)); !errors.Is(err, ErrKey) {
			t.Error(name, "read", err)
		}
		if _, err := OpenUnder(key, []byte(blob), "id"); !errors.Is(err, ErrKey) {
			t.Error(name, "opened", err)
		}
	}
	if _, err = SealUnder(key, "environment", 1, []byte("value"), "id"); !errors.Is(err, ErrKey) {
		t.Fatal("sealed under a reserved scope", err)
	}
}
