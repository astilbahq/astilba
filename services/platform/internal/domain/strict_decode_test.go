package domain

import (
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// The API validator refuses these names too, but ParseSpec also reads operator
// supplied specifications that never pass through it.
func TestParseSpecRefusesAmbiguousNames(t *testing.T) {
	const valid = `{"version":1,"applications":[],"variables":[],"routes":[]}`
	if _, _, err := ParseSpec([]byte(valid)); err != nil {
		t.Fatal(err)
	}
	for name, raw := range map[string]string{
		"case variant": strings.Replace(valid, `"version"`, `"VERSION"`, 1),
		"duplicate":    strings.Replace(valid, `"version":1`, `"version":2,"version":1`, 1),
	} {
		if _, _, err := ParseSpec([]byte(raw)); err == nil {
			t.Fatalf("accepted a %s", name)
		}
	}
}

func composeSpec(metadata string) []byte {
	content := "services:\n  web:\n    image: registry.example.test/web@sha256:" + strings.Repeat("a", 64) + "\nx-astilba:\n  routes:\n    - name: public\n      service: web\n      protocol: http\n      domain: app.example.test\n      port: 8080\n      " + metadata + "\n"
	raw, _ := stablejson.Marshal(map[string]any{"version": 1, "applications": []any{}, "variables": []any{}, "compose": composeproject.Bundle{Namespace: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}})
	return raw
}

// Revisions keep Compose YAML verbatim and every read parses it again, so what the
// first release accepted must keep loading, and new spellings must not start loading
// where an older controller would refuse them.
func TestStoredComposeMetadataKeepsItsReleasedRules(t *testing.T) {
	for _, metadata := range []string{"tls: 'off'", "TLS: 'off'", "TLS: managed\n      tls: 'off'"} {
		spec, _, err := ParseSpec(composeSpec(metadata))
		if err != nil {
			t.Fatalf("%s: %v", metadata, err)
		}
		if len(spec.Routes) != 1 || spec.Routes[0].TLS != "off" {
			t.Fatalf("%s: read %+v", metadata, spec.Routes)
		}
	}
	for _, metadata := range []string{"tls: 'off'\n      bogus: 1", "T_L_S: 'off'", "tls: 'off'\n      allow_cidrs: []"} {
		if _, _, err := ParseSpec(composeSpec(metadata)); err == nil {
			t.Fatalf("accepted %s", metadata)
		}
	}
}

// A rule added after a file was saved must not stop that file being read, or planning,
// automatic deploys, runtime views and recovery would all stop for its environment. A new
// draft is refused instead, with what to change and where.
func TestNewComposeRulesRefuseNewDraftsNotStoredFiles(t *testing.T) {
	content := "services:\n  web:\n    image: registry.example.test/web@sha256:" + strings.Repeat("a", 64) + "\n    logging:\n      driver: syslog\n"
	raw, _ := stablejson.Marshal(map[string]any{"version": 1, "applications": []any{}, "variables": []any{}, "compose": composeproject.Bundle{Namespace: uuid.MustParse("11111111-1111-4111-8111-111111111111"), Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}})
	spec, _, err := ParseSpec(raw)
	if err != nil {
		t.Fatal(err)
	}
	err = ValidateNewDraft(spec, Specification{})
	var refusal *Error
	if !errors.As(err, &refusal) || !strings.Contains(refusal.Message, "change the logging driver") || len(refusal.Fields) != 1 || refusal.Fields[0] != "compose.yaml:5:7" {
		t.Fatalf("a new draft got %#v", err)
	}
}
