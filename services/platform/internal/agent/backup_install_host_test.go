package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// PLAT-363. Every step of an install's repository can be repeated, and its schedule is committed
// with its success, so an install the agent stopped while creating its repository runs again:
// when the agent restarts, and on astilba-agent retry when an older agent left it needing
// attention. It creates the repository once, and the host's schedules are no longer held.
func TestNativeInterruptedBackupInstallRunsAgain(t *testing.T) {
	for _, tc := range []struct {
		name             string
		heldByOlderAgent bool
	}{
		{name: "restarted"},
		{name: "retried after an older agent held it", heldByOlderAgent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			volume := uuid.New()
			w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/data"}}}, nil)
			e := w.engine
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			e.BackupRoots = []string{root}
			// The restic the agent runs records each command it is given. While stopping exists,
			// it holds the check that follows creating the repository until the agent stops.
			tools := t.TempDir()
			calls, stopping, held := filepath.Join(tools, "calls"), filepath.Join(tools, "stopping"), filepath.Join(tools, "held")
			script := fmt.Sprintf("#!/bin/sh\necho \"${3-}\" >> %s\nif [ \"${3-}\" = cat ] && [ -f %s ] && grep -qx init %s; then\n  touch %s\n  exec sleep 60\nfi\nexec /opt/astilba-platform-tools/restic \"$@\"\n", calls, stopping, calls, held)
			e.ResticCLI = filepath.Join(tools, "restic")
			if err := os.WriteFile(e.ResticCLI, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(stopping, nil, 0600); err != nil {
				t.Fatal(err)
			}
			policy := backup.Policy{ID: uuid.New(), Name: "Interrupted install", WorkspaceID: uuid.New(), EnvironmentID: w.plan.EnvironmentID, ServerID: w.plan.ServerID, DestinationID: uuid.New(), ResourceID: volume, Generation: 1, Method: backup.QuiescedFiles, Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Coordinated: true, Plan: w.plan}
			if err := policy.Validate(); err != nil {
				t.Fatal(err)
			}
			installation := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "disposable-repository-canary"}}
			install := w.backupCommand(backup.Command{Action: "install", Installation: installation}, false)

			// The agent stops once the repository exists, before the install commits its schedule.
			ctx, stop := context.WithCancel(context.Background())
			stopped := make(chan error, 1)
			go func() { stopped <- e.Execute(ctx, install) }()
			for deadline := time.Now().Add(2 * time.Minute); ; time.Sleep(100 * time.Millisecond) {
				if _, err := os.Stat(held); err == nil {
					break
				}
				select {
				case err := <-stopped:
					stop()
					r, _ := w.journal.Record(install.Id)
					t.Fatalf("the install ended %s before its repository check: %s (%v)", r.Report.State, r.Report.Note, err)
				default:
				}
				if time.Now().After(deadline) {
					stop()
					t.Fatalf("the install never reached its repository check: %v", <-stopped)
				}
			}
			stop()
			if err := <-stopped; err == nil {
				t.Fatal("the stopped install recorded an outcome")
			}
			r, err := w.journal.Record(install.Id)
			if err != nil || r.Report.State != "running" || r.Checkpoint != "backup-effect" {
				t.Fatalf("the stopped install was left %s at %s (%v)", r.Report.State, r.Checkpoint, err)
			}
			if tc.heldByOlderAgent {
				// As an older agent ended it when it restarted.
				if err = e.finish(install.Id, "needs_attention", interruptedBackupNote); err != nil {
					t.Fatal(err)
				}
				if report := w.deliver(install); report.State != "needs_attention" {
					t.Fatalf("an install needing attention ran without a retry: %s", report.State)
				}
				if err = w.journal.Retry(install.Id); err != nil {
					t.Fatal(err)
				}
			}
			if err = os.Remove(stopping); err != nil {
				t.Fatal(err)
			}
			report := w.deliver(install)
			if report.State != "succeeded" {
				t.Fatalf("the install ran again and ended %s: %s", report.State, report.Note)
			}
			if err = w.journal.Acknowledge(install.Id, report.Sequence); err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(calls)
			if err != nil || strings.Count(string(raw), "init\n") != 1 {
				t.Fatalf("the repository was not created exactly once: %q (%v)", raw, err)
			}
			stored, err := w.journal.schedule(policy.ID)
			if err != nil || stored.Installation.Policy.Generation != policy.Generation || !backup.SnapshotID(stored.Installation.RepositoryID) {
				t.Fatalf("the install did not commit its schedule: %+v (%v)", stored.Installation, err)
			}
			// The repository the install created opens as the policy's own.
			checked := installation
			checked.RepositoryID = stored.Installation.RepositoryID
			if check := w.deliver(w.backupCommand(backup.Command{Action: "check", Installation: checked}, false)); check.State != "succeeded" {
				t.Fatalf("checking the installed repository ended %s: %s", check.State, check.Note)
			}
		})
	}
}
