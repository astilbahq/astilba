package pipeline

import (
	"github.com/google/uuid"
	"testing"
)

func TestCacheCompatibilitySeparatesBuildInputs(t *testing.T) {
	c := Command{Connection: Connection{ID: uuid.New(), Repository: "team/app"}, Build: &BuildConfig{Branch: "main", Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{"MODE": "release"}, Secrets: map[string]uuid.UUID{"token": uuid.New()}}}
	key := c.CacheKey()
	c.Commit = "new commit"
	c.Build.Budget = &BuildBudget{CPUs: 4}
	if c.CacheKey() != key {
		t.Fatal("commit or scheduling budget prevented layer reuse")
	}
	for _, change := range []func(*Command){
		func(c *Command) { c.Connection.ID = uuid.New() },
		func(c *Command) { c.Build.Branch = "untrusted" },
		func(c *Command) { c.Build.Architecture = "amd64" },
		func(c *Command) { c.Build.Context = "other" },
		func(c *Command) { c.Build.Dockerfile = "other" },
		func(c *Command) { c.Build.Arguments = map[string]string{"MODE": "debug"} },
		func(c *Command) { c.Build.Secrets = map[string]uuid.UUID{"token": uuid.New()} },
	} {
		copy := c
		build := *c.Build
		copy.Build = &build
		change(&copy)
		if copy.CacheKey() == key {
			t.Fatal("incompatible build inputs reused a cache")
		}
	}
}

func TestCacheExchangeUsesOnlySpareMemory(t *testing.T) {
	b := BuildConfig{Budget: &BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 544, Minutes: 2}}
	if !b.CanCache() {
		t.Fatal("exact cache exchange headroom was rejected")
	}
	b.Budget.MemoryMiB--
	if b.CanCache() {
		t.Fatal("cache exchange exceeds the configured memory ceiling")
	}
}
