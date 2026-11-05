package cryptobox

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestEnvelopeIdentityAndRotation(t *testing.T) {
	old, e := New("old")
	if e != nil {
		t.Fatal(e)
	}
	value := bytes.Repeat([]byte("private value"), 20000)
	sealed, e := old.Seal(value, "workspace-a:version-1")
	if e != nil {
		t.Fatal(e)
	}
	wrong, _ := New("old")
	for _, c := range []struct {
		ring     *Keyring
		identity string
	}{{wrong, "workspace-a:version-1"}, {old, "workspace-b:version-1"}, {old, "workspace-a:version-2"}} {
		if _, e := c.ring.Open(sealed, c.identity); e == nil {
			t.Fatal("accepted wrong key or identity")
		}
	}
	next, _ := New("next")
	next.Keys["old"] = old.Keys["old"]
	rotated, e := next.Rewrap(sealed, "workspace-a:version-1")
	if e != nil {
		t.Fatal(e)
	}
	delete(next.Keys, "old")
	plain, e := next.Open(rotated, "workspace-a:version-1")
	if e != nil || !bytes.Equal(plain, value) {
		t.Fatal("rotation lost data", e)
	}
	var a, b envelope
	_ = json.Unmarshal(sealed, &a)
	_ = json.Unmarshal(rotated, &b)
	if !bytes.Equal(a.Data, b.Data) {
		t.Fatal("rotation changed data ciphertext")
	}
	b.Data[len(b.Data)-1] ^= 1
	bad, _ := json.Marshal(b)
	if _, e := next.Open(bad, "workspace-a:version-1"); e == nil {
		t.Fatal("accepted corrupt data")
	}
}

func TestParseRefusesAmbiguousKeyrings(t *testing.T) {
	ring, err := New("key")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ring)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = Parse(raw); err != nil {
		t.Fatal("a keyring written by an earlier release must stay readable:", err)
	}
	for name, altered := range map[string]string{
		"case variant": strings.Replace(string(raw), `"active"`, `"ACTIVE"`, 1),
		"duplicate":    strings.Replace(string(raw), `"active"`, `"active":"other","active"`, 1),
	} {
		if altered == string(raw) {
			t.Fatal("fixture changed shape")
		}
		if _, err = Parse([]byte(altered)); err == nil {
			t.Fatalf("accepted a %s", name)
		}
	}
}

// Hand-built keyrings stay usable: jq --rawfile keeps a key file's final newline.
func TestParseAcceptsLineBreaksInKeyValues(t *testing.T) {
	ring, err := New("key")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(ring)
	if err != nil {
		t.Fatal(err)
	}
	wrapped := strings.Replace(string(raw), `="`, `=\n"`, 1)
	if wrapped == string(raw) {
		t.Fatal("fixture changed shape")
	}
	parsed, err := Parse([]byte(wrapped))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(parsed.Keys["key"], ring.Keys["key"]) {
		t.Fatal("key bytes changed")
	}
}
