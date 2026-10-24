package composeproject

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func fixture(source string) Bundle {
	return Bundle{Namespace: uuid.New(), Entrypoints: []string{"compose.yaml"}, Files: []File{{Path: "compose.yaml", Content: source}}}
}
func TestExplicitInputsAndSourcePreservingEdit(t *testing.T) {
	t.Setenv("NOT_SUPPLIED", "ambient-value")
	source := "# café\nx-shared: &defaults\n  restart: unless-stopped\nservices:\n  web:\n    <<: *defaults\n    image: 'busybox:1.37.0' # keep me\n    env_file: settings.env\n    environment:\n      VALUE: ${INPUT:-default}\n      AMBIENT: ${NOT_SUPPLIED:-isolated}\n    volumes:\n      - ./config.txt:/config.txt:ro\n      - data:/data\nvolumes:\n  data: {}\nx-extra:\n  text: preserved\n"
	b := fixture(source)
	b.Files = append(b.Files, File{Path: "settings.env", Content: "FILE_VALUE=from-file\n"}, File{Path: "config.txt", Content: "example\n"})
	p, err := Load(context.Background(), b, map[string]string{"INPUT": "supplied"})
	if err != nil {
		t.Fatal(err)
	}
	env := p.Model.Services["web"].Environment
	if *env["VALUE"] != "supplied" || *env["AMBIENT"] != "isolated" || *env["FILE_VALUE"] != "from-file" {
		t.Fatal("interpolation escaped explicit inputs")
	}
	if _, err = EditImage(context.Background(), b, map[string]string{}, "web", "busybox:latest"); err == nil {
		t.Fatal("merged service was treated as an unambiguous edit")
	}
	b.Files[0].Content = strings.Replace(source, "    <<: *defaults\n", "    restart: unless-stopped\n", 1)
	next, err := EditImage(context.Background(), b, map[string]string{}, "web", "busybox:latest")
	if err != nil {
		t.Fatal(err)
	}
	if next.Files[0].Content != strings.Replace(b.Files[0].Content, "'busybox:1.37.0'", "'busybox:latest'", 1) || next.Files[1] != b.Files[1] || next.Files[2] != b.Files[2] {
		t.Fatal("edit changed unrelated source bytes")
	}
}
func TestBundleRejectsEscapesAndUnsupportedSemantics(t *testing.T) {
	for name, source := range map[string]string{
		"include escape":  "include: [../secret.yaml]\nservices: {}\n",
		"remote include":  "include: ['https://example.test/app.yaml']\nservices: {}\n",
		"ambient file":    "services:\n  web:\n    image: busybox:1.37.0\n    env_file: /etc/environment\n",
		"host mount":      "services:\n  web:\n    image: busybox:1.37.0\n    volumes: [/var/run/docker.sock:/var/run/docker.sock]\n",
		"build":           "services:\n  web:\n    build: .\n",
		"external volume": "services:\n  web:\n    image: busybox:1.37.0\nvolumes:\n  data:\n    external: true\n",
		"unknown feature": "services:\n  web:\n    image: busybox:1.37.0\n    use_api_socket: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Load(context.Background(), fixture(source), nil)
			if err == nil {
				t.Fatal("unsafe/unsupported input accepted")
			}
			d := ParseDiagnostic(err)
			if d.Path == "" || d.Line < 1 || d.Column < 1 {
				t.Fatal("missing source location", d)
			}
		})
	}
	b := fixture("services: {}\n")
	b.Files[0].Path = "../compose.yaml"
	if b.Validate() == nil {
		t.Fatal("bundle traversal accepted")
	}
}
func TestLocalIncludesAndElevatedPlanInputs(t *testing.T) {
	b := fixture("include: [parts/worker.yaml]\nservices:\n  web:\n    image: busybox:1.37.0\n    privileged: true\n    ports: ['127.0.0.1:18080:8080']\n")
	b.Files = append(b.Files, File{Path: "parts/worker.yaml", Content: "services:\n  worker:\n    image: busybox:1.37.0\n    env_file: worker.env\n"}, File{Path: "parts/worker.env", Content: "MODE=worker\n"})
	p, err := Load(context.Background(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Model.Services) != 2 || len(p.Permissions) != 2 || len(p.Ports) != 1 || *p.Model.Services["worker"].Environment["MODE"] != "worker" {
		t.Fatal("included source or permission requirements lost")
	}
	b.Files[1].Content = "services:\n  web:\n    image: busybox:latest\n"
	if _, err = Load(context.Background(), b, nil); err == nil {
		t.Fatal("conflicting included service silently merged")
	}
}

func TestBundledFilesResolveOnceAfterSourceMerge(t *testing.T) {
	b := fixture("services:\n  web:\n    image: busybox:1.37.0\n    environment: {MODE: explicit}\n")
	b.Entrypoints = append(b.Entrypoints, "override.yaml")
	b.Files = append(b.Files, File{Path: "override.yaml", Content: "services:\n  web:\n    env_file: vars.env\n    label_file: labels.env\n"}, File{Path: "vars.env", Content: "MODE=from-file\nLITERAL='cash$HOME'\nEXPANDED=${INPUT}\n"}, File{Path: "labels.env", Content: "example.label='cash$HOME'\n"})
	p, err := Load(context.Background(), b, map[string]string{"HOME": "expanded", "INPUT": "cash$HOME"})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Model.Services["web"]
	if *s.Environment["MODE"] != "explicit" || *s.Environment["LITERAL"] != "cash$HOME" || *s.Environment["EXPANDED"] != "cash$HOME" || s.Labels["example.label"] != "cash$HOME" {
		t.Fatalf("file merge/interpolation changed values: %+v", s)
	}
}

func TestRestoreVolumeRenamePreservesSource(t *testing.T) {
	source := "# recovery fixture\nservices:\n  web:\n    image: busybox:1.37.0\n    volumes:\n      - 'data:/data' # retained comment\nvolumes:\n  data: {} # storage\nx-note: untouched\n"
	b := fixture(source)
	next, err := RenameVolume(context.Background(), b, map[string]string{}, "data", "restored-fixture")
	if err != nil {
		t.Fatal(err)
	}
	want := strings.ReplaceAll(strings.ReplaceAll(source, "'data:/data'", "'restored-fixture:/data'"), "  data: {}", "  restored-fixture: {}")
	if next.Files[0].Content != want {
		t.Fatal("volume cutover changed unrelated source bytes")
	}
	aliased := fixture("services:\n  web:\n    image: busybox:1.37.0\n    volumes: &mounts [data:/data]\n  other:\n    image: busybox:1.37.0\n    volumes: *mounts\nvolumes:\n  data: {}\n")
	if _, err = RenameVolume(context.Background(), aliased, map[string]string{}, "data", "restored-fixture"); err == nil {
		t.Fatal("ambiguous aliased mounts were silently rewritten")
	}
}
