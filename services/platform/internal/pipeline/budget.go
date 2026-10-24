package pipeline

import "strings"

const CacheExchangeMiB = 256

type BuildValidationError struct{ Field, Message string }

func (e *BuildValidationError) Error() string { return e.Message }

type BuildBudget struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memoryMiB"`
	SourceMiB int `json:"sourceMiB"`
	StateMiB  int `json:"stateMiB"`
	Minutes   int `json:"minutes"`
}

func (b BuildConfig) Limits() BuildBudget {
	if b.Budget != nil {
		return *b.Budget
	}
	return BuildBudget{CPUs: 2, MemoryMiB: 2048, SourceMiB: 256, StateMiB: 1024, Minutes: 30}
}
func (b BuildBudget) Validate() error {
	if b.CPUs < 1 || b.CPUs > 8 || b.MemoryMiB < 512 || b.MemoryMiB > 8192 || b.SourceMiB < 64 || b.SourceMiB > 1024 || b.StateMiB < 128 || b.StateMiB > 6144 || b.Minutes < 1 || b.Minutes > 30 {
		return &BuildValidationError{"budget", "Use the supported CPU, memory, storage and assignment-lifetime ranges."}
	}
	if b.SourceMiB+b.StateMiB+96 > b.MemoryMiB {
		return &BuildValidationError{"budget", "Increase the memory limit or reduce source and builder storage: their total plus 96 MiB for tools must fit memory."}
	}
	return nil
}
func (b BuildConfig) NeedsControls() bool { return b.Budget != nil || len(b.WatchPaths) > 0 }

func (b BuildConfig) CanCache() bool {
	l := b.Limits()
	return l.SourceMiB+l.StateMiB+96+CacheExchangeMiB <= l.MemoryMiB
}

// Watch paths are literal repository-relative prefixes, including deleted paths.
func (b BuildConfig) Watches(name string) bool {
	if len(b.WatchPaths) == 0 {
		return true
	}
	for _, p := range b.WatchPaths {
		if name == p || strings.HasPrefix(name, p+"/") {
			return true
		}
	}
	return false
}
