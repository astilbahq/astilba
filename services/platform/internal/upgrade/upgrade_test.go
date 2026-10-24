package upgrade

import "testing"

// The return path must disappear as soon as any new write can have happened,
// even when the subsequent route or application start failed.
func TestRecoveryNeverReturnsAfterNewWriteBoundary(t *testing.T) {
	for _, phase := range []string{"ready", "running", "attention"} {
		s := Status{Phase: phase}
		if !s.Allows("return") {
			t.Fatalf("pre-write recovery unavailable at %s", phase)
		}
		s.WritesPossible = true
		if s.Allows("return") || s.Allows("cutover") || !s.Allows("reconcile") {
			t.Fatalf("unsafe recovery at %s", phase)
		}
	}
	for _, phase := range []string{"complete", "returned", "rejected"} {
		if (Status{Phase: phase}).Allows("return") {
			t.Fatalf("completed phase %s resumed", phase)
		}
	}
}
