// SPDX-License-Identifier: AGPL-3.0-only

package gitcompose

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const baseSource = `# Preserve this document and its extension.
x-env: &shared
  API_TOKEN: ${API_TOKEN:?provide an explicit token}
x-note:
  file: untouched-by-image-edit
services:
  web:
    image: "registry.example/web:old" # base image
    environment: *shared
  worker:
    image: registry.example/worker:old
    environment: *shared
`

const overrideSource = "# café: keep Unicode and Windows line endings\r\nservices:\r\n  web:\r\n    image: 'registry.example/web:override' # selected image\r\n"

func sampleBundle() Bundle {
	return Bundle{Files: []SourceFile{{"compose.yaml", []byte(baseSource)}, {"compose.override.yaml", []byte(overrideSource)}}, Environment: map[string]string{"API_TOKEN": "synthetic-runtime-secret"}, Secrets: []string{"synthetic-runtime-secret"}}
}

func TestSourcePreservingImageEdits(t *testing.T) {
	t.Setenv("API_TOKEN", "ambient-value-must-not-be-used")
	b := sampleBundle()
	edited, err := EditImage(t.Context(), b, "web", "registry.example/web:new")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(edited.Files[0].Data, b.Files[0].Data) {
		t.Fatal("base source changed")
	}
	want := strings.Replace(overrideSource, "registry.example/web:override", "registry.example/web:new", 1)
	if string(edited.Files[1].Data) != want {
		t.Fatal("the edit changed bytes outside the selected scalar")
	}
	edited, err = EditImage(t.Context(), edited, "worker", "registry.example/worker:new")
	if err != nil {
		t.Fatal(err)
	}
	if string(edited.Files[0].Data) != strings.Replace(baseSource, "registry.example/worker:old", "registry.example/worker:new", 1) {
		t.Fatal("unrelated comments, aliases or extensions changed")
	}
	project, err := validateCompose(t.Context(), edited)
	if err != nil {
		t.Fatal(err)
	}
	if project.Services["web"].Environment["API_TOKEN"] == nil || *project.Services["web"].Environment["API_TOKEN"] != "synthetic-runtime-secret" {
		t.Fatal("explicit interpolation input was not used")
	}
	for _, f := range edited.Files {
		if bytes.Contains(f.Data, []byte("synthetic-runtime-secret")) {
			t.Fatal("runtime value entered source")
		}
	}
}

func TestAmbiguousEditsUseFileEditor(t *testing.T) {
	cases := map[string]string{
		"alias":            "x-image: &image registry.example/web:old\nservices:\n  web:\n    image: *image\n",
		"anchored value":   "services:\n  web:\n    image: &image registry.example/web:old\n",
		"anchored service": "services:\n  web: &web\n    image: registry.example/web:old\n  copy: *web\n",
		"merge":            "x-web: &web\n  image: registry.example/web:old\nservices:\n  web:\n    <<: *web\n",
		"interpolation":    "services:\n  web:\n    image: ${IMAGE:-registry.example/web:old}\n",
		"flow":             "services: {web: {image: registry.example/web:old}}\n",
		"multiline":        "services:\n  web:\n    image: >-\n      registry.example/web:old\n",
		"duplicate":        "services:\n  web:\n    image: registry.example/web:old\n    image: registry.example/web:other\n",
	}
	for name, source := range cases {
		t.Run(name, func(t *testing.T) {
			b := Bundle{Files: []SourceFile{{"compose.yaml", []byte(source)}}}
			_, err := EditImage(t.Context(), b, "web", "registry.example/web:new")
			var needsEditor *FileEditorError
			if !errors.As(err, &needsEditor) {
				t.Fatalf("expected a source-located file-editor result, got %v", err)
			}
			if string(b.Files[0].Data) != source {
				t.Fatal("refused edit mutated source")
			}
		})
	}
}

func TestExternalSourceCannotEscapeBundle(t *testing.T) {
	cases := []string{
		"include: https://example.invalid/compose.yaml\nservices: {}\n",
		"x-root: &root\n  include: https://example.invalid/compose.yaml\n<<: *root\nservices: {}\n",
		"services:\n  web:\n    image: alpine:3\n    env_file: /private/fixture-secret\n",
		"services:\n  web:\n    extends: {file: ../outside.yaml, service: web}\n",
		"services:\n  web:\n    image: alpine:3\nsecrets:\n  password:\n    file: /private/fixture-secret\n",
	}
	for _, source := range cases {
		_, err := EditImage(t.Context(), Bundle{Files: []SourceFile{{"compose.yaml", []byte(source)}}}, "web", "alpine:latest")
		var needsEditor *FileEditorError
		if !errors.As(err, &needsEditor) {
			t.Fatalf("expected external reference rejection, got %v", err)
		}
	}
}

// Runtime values must not enter source even as part of a valid image reference.
func TestRuntimeSecretCannotEnterSource(t *testing.T) {
	b := sampleBundle()
	b.Secrets = append(b.Secrets, "sentinel123", "")
	if _, err := EditImage(t.Context(), b, "web", "registry.example/app:sentinel123"); err == nil {
		t.Fatal("embedded runtime secret entered generated source")
	}
	b.Files[0].Data = append(append([]byte(nil), b.Files[0].Data...), []byte("# sentinel123\n")...)
	if _, err := EditImage(t.Context(), b, "web", "registry.example/app:new"); err == nil {
		t.Fatal("source containing runtime secret was accepted")
	}
	r := &Repository{Source: sampleBundle()}
	b = sampleBundle()
	b.Files[0].Data = []byte("# synthetic-runtime-secret\n")
	b.Secrets = nil
	if _, err := r.Prepare(t.Context(), b); err == nil {
		t.Fatal("manual draft bypassed original secret inputs")
	}
}
