package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestSourceBundleRejectsFilesystemEscapeAndMutation(t *testing.T) {
	journal := &Journal{dir: t.TempDir()}
	engine := Engine{Journal: journal}
	plan := deployment.Plan{ID: uuid.New(), UnitID: uuid.New(), Specification: domain.Specification{Compose: &composeproject.Bundle{Files: []composeproject.File{{Path: "nested/config.txt", Content: "reviewed"}}}}}
	outside := t.TempDir()
	root := engine.sourceDirectory(plan)
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "nested")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.prepareSourceFiles(plan); err == nil {
		t.Fatal("followed a bundle directory symlink outside its root")
	}
	if _, err := os.Stat(filepath.Join(outside, "config.txt")); !os.IsNotExist(err) {
		t.Fatal("wrote outside the bundle")
	}
	if err := os.Remove(filepath.Join(root, "nested")); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.prepareSourceFiles(plan); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested/config.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := engine.prepareSourceFiles(plan); err == nil {
		t.Fatal("accepted modified frozen source on retry")
	}
}

func TestSourceBundleRecoversBeforeAtomicPublication(t *testing.T) {
	engine := Engine{Journal: &Journal{dir: t.TempDir()}, Point: func(point string) {
		if point == "compose-source-written" {
			panic("interruption")
		}
	}}
	p := deployment.Plan{ID: uuid.New(), UnitID: uuid.New(), Specification: domain.Specification{Compose: &composeproject.Bundle{Files: []composeproject.File{{Path: "config.txt", Content: "complete reviewed contents"}}}}}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("interruption not reached")
			}
		}()
		_, _ = engine.prepareSourceFiles(p)
	}()
	engine.Point = nil
	root, err := engine.prepareSourceFiles(p)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(root, "config.txt"))
	if err != nil || string(content) != "complete reviewed contents" {
		t.Fatal("retry did not publish complete source", err)
	}
}
