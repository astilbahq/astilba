package cryptobox

import (
	"bytes"
	"encoding/json"
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
