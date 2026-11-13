package portability

import (
	"strings"
	"testing"
)

const identityCommand = `{"id":"7b0d7c1e-1d0f-4c55-9a0c-2f0d4c6a8e11","serverId":"0b4b2f9e-6d1e-4a1a-8a3e-5e2d7c9f0a21","environmentId":"3c2a1b0d-9e8f-4a7b-8c6d-5e4f3a2b1c09","resourceId":"5a4b3c2d-1e0f-4a9b-8c7d-6e5f4a3b2c18","parentId":null,"kind":"visual","action":"inspect","generation":7,"image":"","planHash":"abc","deadline":"2026-11-12T14:00:00.5+01:00","release":null,"cancelRequested":false,"controllerGeneration":3,"visual":{"action":"deploy","plan":{"b":1,"a":[1.50,2]}}}`

func identity(t *testing.T, raw string) string {
	t.Helper()
	id, err := CommandIdentity([]byte(raw), CommandIdentityVersion)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// Identity is computed from the stored command. Sealed evidence is checked by later releases, so
// the value for a given stored command never changes, and a release that adds a command field
// doesn't change the identity of a command stored before it.
func TestCommandIdentityIsStableAcrossReleases(t *testing.T) {
	base := identity(t, identityCommand)
	if base != "v2:ace57b7d6e11c99cace887be33d96f82194b3dadf71ade413bfc0b122ed35fa9" {
		t.Fatal("identity version 2 changed for a stored command:", base)
	}
	same := map[string]string{
		"a field a later release adds":          strings.Replace(identityCommand, `"kind"`, `"futureField":{"x":1},"kind"`, 1),
		"cancellation":                          strings.Replace(identityCommand, `"cancelRequested":false`, `"cancelRequested":true`, 1),
		"controller transfer":                   strings.Replace(identityCommand, `"controllerGeneration":3`, `"controllerGeneration":9`, 1),
		"an attached checkpoint":                strings.Replace(identityCommand, `"release":null`, `"release":null,"recoveryCheckpoint":{"version":1}`, 1),
		"a null written as absent":              strings.Replace(identityCommand, `"parentId":null,`, ``, 1),
		"the deadline in another zone":          strings.Replace(identityCommand, `2026-11-12T14:00:00.5+01:00`, `2026-11-12T13:00:00.500Z`, 1),
		"key order and spacing of a stored row": `{ "visual": {"plan":{"a":[1.50,2],"b":1},"action":"deploy"}, ` + strings.TrimPrefix(strings.Replace(identityCommand, `,"visual":{"action":"deploy","plan":{"b":1,"a":[1.50,2]}}`, ``, 1), `{`),
	}
	for name, raw := range same {
		if got := identity(t, raw); got != base {
			t.Errorf("%s changed the identity", name)
		}
	}
	different := map[string]string{
		"generation":       strings.Replace(identityCommand, `"generation":7`, `"generation":8`, 1),
		"plan hash":        strings.Replace(identityCommand, `"planHash":"abc"`, `"planHash":"abd"`, 1),
		"deadline":         strings.Replace(identityCommand, `14:00:00.5`, `14:00:00.6`, 1),
		"payload":          strings.Replace(identityCommand, `"b":1`, `"b":2`, 1),
		"number precision": strings.Replace(identityCommand, `1.50`, `1.5`, 1),
		"parent":           strings.Replace(identityCommand, `"parentId":null`, `"parentId":"7b0d7c1e-1d0f-4c55-9a0c-2f0d4c6a8e12"`, 1),
	}
	for name, raw := range different {
		if got := identity(t, raw); got == base {
			t.Errorf("a different %s kept the identity", name)
		}
	}
}

func TestCommandIdentityRefusesWhatItCannotRead(t *testing.T) {
	for name, raw := range map[string]string{
		"not an object":   `[1]`,
		"null":            `null`,
		"duplicate key":   `{"id":"a","id":"b"}`,
		"invalid instant": `{"deadline":"tomorrow"}`,
	} {
		if _, err := CommandIdentity([]byte(raw), CommandIdentityVersion); err == nil {
			t.Errorf("%s was identified", name)
		}
	}
	if _, err := CommandIdentity([]byte(identityCommand), 1); err == nil {
		t.Error("an unknown identity version was used")
	}
}
