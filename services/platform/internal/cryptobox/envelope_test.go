package cryptobox

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
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

// Only the environment scope names a holder, and it always does; anything else this release does
// not know is refused.
func TestEnvelopeV2RefusesMisplacedAndUnknownFields(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 32)
	sealed, err := SealUnder(key, ScopeWorkspace, 1, []byte("value"), "id")
	if err != nil {
		t.Fatal(err)
	}
	for name, blob := range map[string]string{
		"workspace holder":           strings.Replace(string(sealed), `"version":2`, `"holder":"11111111-1111-4111-8111-111111111111","version":2`, 1),
		"environment without holder": strings.Replace(string(sealed), `"scope":"workspace"`, `"scope":"environment"`, 1),
		"unknown member":             strings.Replace(string(sealed), `"version":2`, `"keyId":"k","version":2`, 1),
		"generation 0":               strings.Replace(string(sealed), `"generation":1`, `"generation":0`, 1),
		"version 3":                  strings.Replace(string(sealed), `"version":2`, `"version":3`, 1),
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
	if _, err = SealUnder(key, ScopeEnvironment, 1, []byte("value"), "id"); !errors.Is(err, ErrKey) {
		t.Fatal("sealed under the environment scope without an environment", err)
	}
	if _, err = SealUnderEnvironment(key, uuid.Nil, 1, []byte("value"), "id"); !errors.Is(err, ErrKey) {
		t.Fatal("sealed under no environment's key", err)
	}
	if _, err = MoveUnder(nil, key, sealed, "id", key, ScopeEnvironment, 1); !errors.Is(err, ErrKey) {
		t.Fatal("moved under the environment scope without an environment", err)
	}
}

// An environment's value opens only under that environment's key, and its wrapping names the
// environment, so it cannot be presented as another environment's. Moving a value onto it keeps
// the value's data, and its content digest with it.
func TestEnvironmentEnvelopeIsBoundToItsEnvironment(t *testing.T) {
	workspaceKey, environmentKey := bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{9}, 32)
	env, other := uuid.MustParse("22222222-2222-4222-8222-222222222222"), uuid.MustParse("33333333-3333-4333-8333-333333333333")
	sealed, err := SealUnderEnvironment(environmentKey, env, 1, []byte("value"), "secret:v1:a:b")
	if err != nil {
		t.Fatal(err)
	}
	header, err := ReadHeader(sealed)
	if err != nil || header != (Header{Version: 2, Scope: ScopeEnvironment, Generation: 1, Holder: env}) {
		t.Fatal(header, err)
	}
	if value, err := OpenUnder(environmentKey, sealed, "secret:v1:a:b"); err != nil || string(value) != "value" {
		t.Fatal(string(value), err)
	}
	if _, err = OpenUnder(workspaceKey, sealed, "secret:v1:a:b"); !errors.Is(err, ErrKey) {
		t.Fatal("opened under the workspace key", err)
	}
	for name, changed := range map[string]string{
		"holder":     strings.Replace(string(sealed), env.String(), other.String(), 1),
		"generation": strings.Replace(string(sealed), `"generation":1`, `"generation":2`, 1),
		"scope":      strings.Replace(strings.Replace(string(sealed), `"scope":"environment"`, `"scope":"workspace"`, 1), `,"holder":"`+env.String()+`"`, "", 1),
	} {
		if changed == string(sealed) {
			t.Fatal(name, "unchanged")
		}
		if _, err = OpenUnder(environmentKey, []byte(changed), "secret:v1:a:b"); !errors.Is(err, ErrKey) {
			t.Fatal("a changed", name, "opened", err)
		}
	}
	under, err := SealUnder(workspaceKey, ScopeWorkspace, 1, []byte("moved"), "secret:v1:a:c")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := MoveUnderEnvironment(nil, workspaceKey, under, "secret:v1:a:c", environmentKey, env, 1)
	if err != nil {
		t.Fatal(err)
	}
	if value, err := OpenUnder(environmentKey, moved, "secret:v1:a:c"); err != nil || string(value) != "moved" {
		t.Fatal(string(value), err)
	}
	if !bytes.Equal(dataOf(t, moved), dataOf(t, under)) {
		t.Fatal("moving the value changed its data")
	}
	back, err := MoveUnder(nil, environmentKey, moved, "secret:v1:a:c", workspaceKey, ScopeWorkspace, 2)
	if err != nil || !bytes.Equal(dataOf(t, back), dataOf(t, under)) {
		t.Fatal("moved back", err)
	}
}

func dataOf(t *testing.T, blob []byte) []byte {
	t.Helper()
	var env struct {
		Data []byte `json:"data"`
	}
	if err := json.Unmarshal(blob, &env); err != nil {
		t.Fatal(err)
	}
	return env.Data
}

// An environment's key is wrapped under its workspace's key, bound to the workspace, the
// environment, its generation and the workspace key's generation.
func TestEnvironmentKeyWrappingIsBound(t *testing.T) {
	workspaceKey, environmentKey := bytes.Repeat([]byte{7}, 32), bytes.Repeat([]byte{9}, 32)
	ws, env := uuid.MustParse("11111111-1111-4111-8111-111111111111"), uuid.MustParse("22222222-2222-4222-8222-222222222222")
	wrapped, err := WrapEnvironmentKey(workspaceKey, ws, env, 1, 3, environmentKey)
	if err != nil {
		t.Fatal(err)
	}
	if opened, err := OpenEnvironmentKey(workspaceKey, wrapped, ws, env, 1, 3); err != nil || !bytes.Equal(opened, environmentKey) {
		t.Fatal(err)
	}
	for name, open := range map[string]func() ([]byte, error){
		"workspace":            func() ([]byte, error) { return OpenEnvironmentKey(workspaceKey, wrapped, env, env, 1, 3) },
		"environment":          func() ([]byte, error) { return OpenEnvironmentKey(workspaceKey, wrapped, ws, ws, 1, 3) },
		"generation":           func() ([]byte, error) { return OpenEnvironmentKey(workspaceKey, wrapped, ws, env, 2, 3) },
		"workspace generation": func() ([]byte, error) { return OpenEnvironmentKey(workspaceKey, wrapped, ws, env, 1, 2) },
		"workspace key":        func() ([]byte, error) { return OpenEnvironmentKey(environmentKey, wrapped, ws, env, 1, 3) },
	} {
		if _, err := open(); !errors.Is(err, ErrKey) {
			t.Error("opened with another", name, err)
		}
	}
}

// The database tells converted values from the rest by their first bytes (custody_value_converted,
// in the custody conversion migration), so every envelope starts with its version.
func TestEnvelopesStartWithTheirVersion(t *testing.T) {
	ring, err := New("prefix")
	if err != nil {
		t.Fatal(err)
	}
	v1, err := ring.Seal([]byte("value"), "secret:v1:a:b")
	if err != nil {
		t.Fatal(err)
	}
	v2, err := SealUnder(bytes.Repeat([]byte{7}, 32), ScopeWorkspace, 1, []byte("value"), "secret:v1:a:b")
	if err != nil {
		t.Fatal(err)
	}
	moved, err := MoveUnder(ring, nil, v1, "secret:v1:a:b", bytes.Repeat([]byte{7}, 32), ScopeWorkspace, 1)
	if err != nil {
		t.Fatal(err)
	}
	environment, err := SealUnderEnvironment(bytes.Repeat([]byte{9}, 32), uuid.New(), 1, []byte("value"), "secret:v1:a:b")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(v1, []byte(`{"version":1,`)) || !bytes.HasPrefix(v2, []byte(`{"version":2,`)) || !bytes.HasPrefix(moved, []byte(`{"version":2,`)) || !bytes.HasPrefix(environment, []byte(`{"version":2,`)) {
		t.Fatalf("%.16s %.16s %.16s %.16s", v1, v2, moved, environment)
	}
}
