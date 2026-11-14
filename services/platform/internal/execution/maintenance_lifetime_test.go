package execution

import (
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
)

// A coordinated backup's leaf lives past its run's own timeout, so with a default policy a long
// upload fails by that timeout and the parent resumes the writers. Other children keep theirs.
func TestMaintenanceChildLifetime(t *testing.T) {
	for _, seconds := range []int{0, 600, 1800} {
		effect := backup.Command{Action: "backup", Installation: backup.Installation{Policy: backup.Policy{TimeoutSeconds: seconds}}}
		m := &coordinatedMaintenance{Effect: maintenanceEffect{Backup: &effect}}
		for _, kind := range []api.ExecutionCommandKind{"backup", "schedule"} {
			if lifetime := maintenanceChildLifetime(api.ExecutionCommand{Kind: kind}, m); lifetime != effect.Lifetime() || lifetime <= effect.Installation.Policy.RunTimeout() {
				t.Fatal("a backup leaf's deadline does not outlast its run timeout:", lifetime)
			}
		}
		if maintenanceChildLifetime(api.ExecutionCommand{Kind: "crosshost"}, m) != 30*time.Minute {
			t.Fatal("a coordinated phase changed its deadline")
		}
	}
	if maintenanceChildLifetime(api.ExecutionCommand{Kind: "upgrade"}, &coordinatedMaintenance{}) != 2*time.Hour {
		t.Fatal("an upgrade changed its deadline")
	}
}
