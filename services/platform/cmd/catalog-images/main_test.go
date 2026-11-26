package main

import (
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"go.yaml.in/yaml/v3"
)

const (
	nginx = "nginx:1.30.6-alpine3.24@sha256:901e593c95dc85f5a729f5765516321366ef4ac401821431d408897e95287d0a"
	kaneo = "ghcr.io/usekaneo/kaneo:2.49.0@sha256:cd1826b1aba3ce736015cadf6f6c975d923aac00f4b6aaa7aedb29bc722dae49"
	kuma  = "louislam/uptime-kuma:2.5.12-slim-rootless@sha256:212d2131952eded9b2508620455f15f5c2c85c262aafd23b97a7447c53e1d1bc"
)

func TestRunPrintsTheImagesNotPulled(t *testing.T) {
	var out strings.Builder
	if err := run(&out, []string{kuma, nginx, kaneo}, []string{nginx}); err != nil {
		t.Fatal(err)
	}
	if want := kuma + "\n" + kaneo + "\n"; out.String() != want {
		t.Errorf("printed %q, want %q", out.String(), want)
	}
}

// The pull checks bytes against the digest, and the pin report finds a pin by its tag.
func TestRunRefusesAnImageWithoutTagAndDigest(t *testing.T) {
	digest := "@sha256:901e593c95dc85f5a729f5765516321366ef4ac401821431d408897e95287d0a"
	for _, image := range []string{
		"nginx",
		"nginx:1.30.6-alpine3.24",
		"nginx" + digest,
		"docker.io/library/nginx:1.30.6-alpine3.24" + digest,
		"nginx:1.30.6-alpine3.24@sha256:901e",
		" " + nginx,
	} {
		t.Run(image, func(t *testing.T) {
			var out strings.Builder
			if run(&out, []string{image}, nil) == nil {
				t.Error("printed a catalog image without a tag and a digest")
			}
			if run(&out, []string{nginx}, []string{image}) == nil {
				t.Error("accepted a pulled image without a tag and a digest")
			}
			if out.Len() > 0 {
				t.Errorf("printed %q before refusing", out.String())
			}
		})
	}
}

type workflowStep struct {
	Name string            `yaml:"name"`
	ID   string            `yaml:"id"`
	If   string            `yaml:"if"`
	Run  string            `yaml:"run"`
	Uses string            `yaml:"uses"`
	With map[string]string `yaml:"with"`
}

// The Host visual lane deploys every catalog entry, and pulls each image before its journey so
// that a registry failing for a moment cannot fail the lane. It passes its fixture images to this
// command and pulls what it prints.
func TestHostVisualLanePullsEveryCatalogImage(t *testing.T) {
	raw, err := os.ReadFile("../../../../.github/workflows/ci.yml")
	if err != nil {
		t.Fatal(err)
	}
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []workflowStep    `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		t.Fatal(err)
	}
	host := workflow.Jobs["host"]
	step := func(name string) workflowStep {
		i := slices.IndexFunc(host.Steps, func(s workflowStep) bool { return s.Name == name })
		if i < 0 {
			t.Fatalf("the host job has no step named %q", name)
		}
		return host.Steps[i]
	}
	visual := "matrix.lane == 'visual'"
	list, fixtures, pull := step("List catalog images"), step("Pull pinned fixture images"), step("Pull catalog images")
	if list.If != visual || pull.If != visual || !strings.Contains(list.Run, "go -C services/platform run ./cmd/catalog-images $FIXTURE_IMAGES)") {
		t.Errorf("the visual lane must list its catalog images with: go -C services/platform run ./cmd/catalog-images $FIXTURE_IMAGES")
	}
	if fixtures.If != "" || fixtures.With["images"] != "${{ env.FIXTURE_IMAGES }}" {
		t.Errorf("every lane must pull ${{ env.FIXTURE_IMAGES }}, found if %q, images %q", fixtures.If, fixtures.With["images"])
	}
	if want := "${{ steps." + list.ID + ".outputs.images }}"; list.ID == "" || pull.With["images"] != want {
		t.Errorf("the catalog pull must read %s, found %q", want, pull.With["images"])
	}
	if fixtures.Uses != "./.github/actions/pull-images" || pull.Uses != fixtures.Uses {
		t.Errorf("both pulls must use ./.github/actions/pull-images, found %q and %q", fixtures.Uses, pull.Uses)
	}

	pulled := strings.Fields(host.Env["FIXTURE_IMAGES"])
	var out strings.Builder
	if err := run(&out, catalog.Images(), pulled); err != nil {
		t.Fatal(err)
	}
	listed := strings.Fields(out.String())
	for _, image := range listed {
		if slices.Contains(pulled, image) {
			t.Errorf("%s is pulled twice", image)
		}
	}
	for _, image := range catalog.Images() {
		if !slices.Contains(pulled, image) && !slices.Contains(listed, image) {
			t.Errorf("the visual lane does not pull %s", image)
		}
	}
}
