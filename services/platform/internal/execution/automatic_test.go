package execution

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestAutomaticChangesRequireReviewForStatefulChangesAndNewPermissions(t *testing.T) {
	namespace := uuid.New()
	original := "services:\n  db:\n    image: postgres:16\n    volumes: [data:/var/lib/postgresql/data]\n  web:\n    image: nginx:1\n    ports: [127.0.0.1:9011:80]\nvolumes:\n  data: {}\n"
	parse := func(content string) domain.Specification {
		t.Helper()
		raw, _ := json.Marshal(domain.Specification{Version: 1, Applications: []domain.Application{}, Compose: &composeproject.Bundle{Namespace: namespace, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}}}, Variables: []domain.Variable{}})
		spec, _, err := domain.ParseSpec(raw)
		if err != nil {
			t.Fatalf("%#v", err)
		}
		return spec
	}
	previous := parse(original)
	t.Run("omitted binding does not inherit explicit IPv4 approval", func(t *testing.T) {
		ipv4 := parse(strings.ReplaceAll(original, "127.0.0.1", "0.0.0.0"))
		defaults := parse(strings.ReplaceAll(original, "127.0.0.1:", ""))
		if _, err := automaticChanges(ipv4, defaults); err == nil {
			t.Fatal("daemon default interfaces inherited explicit IPv4-only approval")
		}
	})

	for _, tc := range []struct {
		name, content string
		allowed       bool
	}{
		{"stateless image update with reviewed port", strings.ReplaceAll(original, "nginx:1", "nginx:2"), true},
		{"stateful major image update", strings.ReplaceAll(original, "postgres:16", "postgres:17"), false},
		{"public exposure of reviewed loopback port", strings.ReplaceAll(original, "127.0.0.1", "0.0.0.0"), false},
		{"new published port", strings.ReplaceAll(original, "9011", "9012"), false},
		{"new stateful service", strings.ReplaceAll(original, "  web:", "  other:\n    image: postgres:17\n    volumes: [data:/data]\n  web:"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			carry, err := automaticChanges(previous, parse(tc.content))
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%v error=%v", tc.allowed, err)
			}
			if tc.allowed && !carry {
				t.Fatal("existing reviewed permissions were not carried forward")
			}
		})
	}
}

func TestAutomaticStatefulEffectiveInputsAndResolvedImages(t *testing.T) {
	namespace := uuid.New()
	parse := func(content, config string) domain.Specification {
		t.Helper()
		raw, _ := json.Marshal(domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: namespace, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: content}, {Path: "postgresql.conf", Content: config}}}})
		spec, _, err := domain.ParseSpec(raw)
		if err != nil {
			t.Fatal(err)
		}
		return spec
	}
	compose := "services:\n  db:\n    image: postgres:latest\n    volumes: [data:/var/lib/postgresql/data, './postgresql.conf:/etc/postgresql.conf:ro']\n  web:\n    image: nginx:1\nvolumes:\n  data: {}\n"
	previous := parse(compose, "setting=old\n")
	next := parse(compose, "setting=new\n")
	if _, err := automaticChanges(previous, next); err == nil {
		t.Fatal("stateful bundled config changed without review")
	}
	configCompose := strings.ReplaceAll(compose, ", './postgresql.conf:/etc/postgresql.conf:ro'", "")
	configCompose = strings.Replace(configCompose, "  web:", "    configs: [db_config]\n  web:", 1) + "configs:\n  db_config:\n    file: postgresql.conf\n"
	if _, err := automaticChanges(parse(configCompose, "old"), parse(configCompose, "new")); err == nil {
		t.Fatal("top-level stateful config file changed without review")
	}
	id := previous.Applications[0].ID.String()
	if err := automaticStatefulImages(previous, map[string]string{id: "postgres@sha256:old"}, map[string]string{id: "postgres@sha256:new"}); err == nil {
		t.Fatal("mutable stateful tag changed resolved image without review")
	}
	if err := automaticStatefulImages(previous, map[string]string{id: "postgres@sha256:old"}, map[string]string{id: "postgres@sha256:old"}); err != nil {
		t.Fatal("unchanged resolved stateful image blocked", err)
	}
	app := domain.Application{ID: uuid.New(), Name: "db", Source: domain.ApplicationSource{Kind: "image"}, Mounts: []domain.VolumeMount{{VolumeID: uuid.New(), Target: "/data"}}}
	old, newValue := "old", "new"
	visual := domain.Specification{Applications: []domain.Application{app}, Variables: []domain.Variable{{Name: "PGDATA", Value: &old}}}
	changed := visual
	changed.Variables = []domain.Variable{{Name: "PGDATA", Value: &newValue}}
	if _, err := automaticChanges(visual, changed); err == nil {
		t.Fatal("effective global stateful environment changed without review")
	}
}
