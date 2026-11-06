package store

import (
	"testing"
	"time"
)

// The rule was chosen from these minutes of the runs in
// experiments/fleet-capacity/RESULTS.md, plus two that those runs cannot produce.
func TestPoolShortage(t *testing.T) {
	for _, tc := range []struct {
		name string
		use  poolUse
		want bool
	}{
		{"room to spare", poolUse{acquires: 117621, waited: 4366, waiting: 3 * time.Second}, false},
		{"worst minute close to the ceiling", poolUse{acquires: 214223, waited: 161364, waiting: 2520 * time.Second}, false},
		{"a slowdown with failing polls", poolUse{acquires: 57100, waited: 36918, waiting: 3325 * time.Second, canceled: 4}, true},
		{"past the ceiling", poolUse{acquires: 72803, waited: 72803, waiting: 38190 * time.Second, canceled: 164}, true},
		{"a client disconnected on an idle pool", poolUse{acquires: 240, canceled: 1}, false},
		{"too stuck to complete anything", poolUse{acquires: 3, canceled: 900}, true},
		{"nothing happened", poolUse{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.use.short(); got != tc.want {
				t.Fatalf("short() = %v", got)
			}
		})
	}
}
