package pipeline

import (
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestBuildBudgetProtectsMemoryAndLifetime(t *testing.T) {
	b := BuildConfig{ConnectionID: uuid.New(), RunnerID: uuid.New(), Branch: "main", Context: ".", Dockerfile: "Dockerfile", Architecture: "amd64"}
	if err := b.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []BuildBudget{
		{CPUs: 0, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 1024, Minutes: 30},
		{CPUs: 9, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 1024, Minutes: 30},
		{CPUs: 2, MemoryMiB: 512, SourceMiB: 256, StateMiB: 256, Minutes: 30},
		{CPUs: 2, MemoryMiB: 8193, SourceMiB: 256, StateMiB: 1024, Minutes: 30},
		{CPUs: 2, MemoryMiB: 2048, SourceMiB: 63, StateMiB: 1024, Minutes: 30},
		{CPUs: 2, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 127, Minutes: 30},
		{CPUs: 2, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 1024, Minutes: 31},
		{CPUs: 2, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 1024, Minutes: 0},
	} {
		b.Budget = &invalid
		if b.Validate() == nil {
			t.Fatalf("accepted unsafe or unsupported limits: %+v", invalid)
		}
	}
	limits := BuildBudget{CPUs: 1, MemoryMiB: 512, SourceMiB: 64, StateMiB: 352, Minutes: 1}
	b.Budget = &limits
	if err := b.Validate(); err != nil {
		t.Fatal("valid tight budget rejected", err)
	}
}

func TestWatchPathsAreLiteralRepositoryPrefixes(t *testing.T) {
	b := BuildConfig{ConnectionID: uuid.New(), RunnerID: uuid.New(), Branch: "main", Context: ".", Dockerfile: "Dockerfile", Architecture: "amd64", WatchPaths: []string{"apps/web", "pnpm-lock.yaml"}}
	for name, want := range map[string]bool{"apps/web": true, "apps/web/src/page.tsx": true, "pnpm-lock.yaml": true, "apps/web-other/index.ts": false, "docs/apps/web": false, "apps/api/main.go": false, "pnpm-lock.yaml.old": false} {
		if b.Watches(name) != want {
			t.Errorf("watch %q: want %v", name, want)
		}
	}
	for _, paths := range [][]string{{"../web"}, {"/apps/web"}, {"apps/web/"}, {"apps/*"}, {"apps/web", "apps/web"}, {strings.Repeat("a", 241)}} {
		b.WatchPaths = paths
		if b.Validate() == nil {
			t.Errorf("accepted ambiguous/invalid watch paths: %q", paths)
		}
	}
	b.WatchPaths = nil
	if !b.Watches("any/path") {
		t.Fatal("empty filter did not watch every change")
	}
}
