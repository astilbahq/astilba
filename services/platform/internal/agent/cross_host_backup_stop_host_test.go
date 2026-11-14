package agent

import (
	"context"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// PLAT-357 and PLAT-360. A coordinated backup leaf whose command stops while the agent runs on
// settles by what its run did. Stopped by the owner before its first effect, the leaf is cancelled
// with its run failed, and the parent resumes the source. Stopped part way through its upload,
// by the owner or by a deadline it reached because it was delivered late, the leaf and its run
// need attention, the hosts stay held with their writers stopped, and rejecting the leaf lets
// the parent resume them. A run that reaches its own timeout first, as one delivered on time
// does, fails, and the parent resumes the writers with no one inspecting the host.
func nativeCoordinatedBackupStops(t *testing.T, hosts []*nativePlacementHost, units []deployment.DistributedUnit, completed []deployment.Observation, app uuid.UUID, secrets map[string]string, installation backup.Installation) {
	t.Helper()
	ctx := context.Background()
	primary := hosts[0]
	writersRunning := func(t *testing.T, want bool) {
		t.Helper()
		for index, host := range hosts {
			id := units[index].Plan.InstanceIDs(app)[0]
			v, err := host.docker.ContainerInspect(ctx, deployment.UnitName(id), client.ContainerInspectOptions{})
			if err != nil || v.Container.State.Running != want {
				t.Fatalf("host %d writer running: %t, want %t (%v)", index, err == nil && v.Container.State.Running, want, err)
			}
		}
	}
	// A policy of its own, so its short run timeout leaves the shared policy's generation alone.
	timed := installation
	timed.Policy.ID, timed.Policy.TimeoutSeconds = uuid.New(), 20
	if err := timed.Policy.Validate(); err != nil {
		t.Fatal(err)
	}
	install := primary.backupCommand(backup.Command{Action: "install", Installation: timed}, nil)
	install.Kind = "schedule"
	primary.command(t, install)
	for _, stop := range []struct {
		name, at, by string
		leaf, run    api.ExecutionState
	}{
		{name: "owner_cancel_before_first_effect", at: "backup-run-recorded", by: "owner", leaf: "cancelled", run: "failed"},
		{name: "owner_cancel_mid_upload", at: "backup-uploading", by: "owner", leaf: "needs_attention", run: "needs_attention"},
		{name: "late_delivery_reaching_its_deadline_mid_upload", at: "backup-uploading", by: "deadline", leaf: "needs_attention", run: "needs_attention"},
		{name: "run_timeout_mid_upload", at: "backup-uploading", by: "timeout", leaf: "failed", run: "failed"},
	} {
		t.Run("coordinated_backup_"+stop.name, func(t *testing.T) {
			installation := installation
			if stop.by == "timeout" {
				installation = timed
			}
			parent, run := uuid.New(), uuid.New()
			effect := backup.Command{Action: "backup", Installation: installation, RunID: run}
			intents := make([]deployment.Maintenance, len(hosts))
			for index := range hosts {
				intents[index] = deployment.Maintenance{Kind: "backup", ResourceID: installation.Policy.ID, RunID: run, EffectHash: effect.Digest(), ExpectedState: completed[index].StateHash}
			}
			command := func(index int, phase string) api.ExecutionCommand {
				body := deployment.CrossHostCommand{ParentID: parent, GroupHash: strings.Repeat("5", 64), Phase: phase, Unit: units[index], Secrets: secretsForPlan(units[index].Plan, secrets), Maintenance: &intents[index]}
				if phase == "effect" {
					body.EffectAction = "backup"
				}
				return hosts[index].coordinatedCommand(body)
			}
			for _, phase := range []string{"reserve", "pause"} {
				for index, host := range hosts {
					host.command(t, command(index, phase))
				}
			}
			writersRunning(t, false)
			primary.command(t, command(0, "effect"))
			leaf := primary.backupCommand(effect, &parent)
			if installation.Policy.GeneralSchedule() {
				leaf.Kind = "schedule"
			}
			// As management issues it: the leaf outlives its run's own timeout.
			leaf.Deadline = time.Now().Add(effect.Lifetime())
			request := hostRequest{Command: &leaf, StallAt: stop.at}
			switch stop.by {
			case "owner":
				request.OwnerCancelAt, request.StallUntil = stop.at, time.Now().Add(time.Second)
			case "deadline":
				// The agent was silent for all but the last seconds of the leaf's life.
				leaf.Deadline = time.Now().Add(10 * time.Second)
				request.StallUntil = leaf.Deadline.Add(500 * time.Millisecond)
			case "timeout":
				request.StallUntil = time.Now().Add(installation.Policy.RunTimeout() + 3*time.Second)
			}
			reply := primary.call(t, request)
			if reply.Report == nil || reply.Report.State != stop.leaf {
				t.Fatalf("the stopped leaf settled as %+v", reply.Report)
			}
			status := primary.call(t, hostRequest{Policy: &installation.Policy.ID}).Status
			if last := status.Runs[len(status.Runs)-1]; last.ID != run || last.State != string(stop.run) {
				t.Fatalf("the stopped leaf's run settled as %+v", last)
			}
			if stop.leaf == "needs_attention" {
				// The parent waits on the uncertain leaf, so nothing resumes the writers or
				// releases the hosts until the owner rejects its outcome.
				writersRunning(t, false)
				for _, host := range hosts {
					if held := host.call(t, hostRequest{Barrier: true}).Barrier; held == nil || !*held {
						t.Fatal("an uncertain leaf released its host")
					}
				}
				primary.call(t, hostRequest{Reject: &leaf.Id})
				if reply = primary.call(t, hostRequest{Command: &leaf}); reply.Report == nil || reply.Report.State != "failed" {
					t.Fatalf("the rejected leaf settled as %+v", reply.Report)
				}
				status = primary.call(t, hostRequest{Policy: &installation.Policy.ID}).Status
				if last := status.Runs[len(status.Runs)-1]; last.ID != run || last.State != "failed" {
					t.Fatalf("the rejected run settled as %+v", last)
				}
			}
			primary.call(t, hostRequest{Acknowledge: reply.Report})
			// A cancelled or failed leaf moves the parent to resume the source.
			for _, phase := range []string{"resume-source", "complete"} {
				for index, host := range hosts {
					r := host.command(t, command(index, phase))
					var receipt deployment.CrossHostReceipt
					if phase == "complete" {
						if err := json.Unmarshal(*r.Crosshost, &receipt); err != nil {
							t.Fatal(err)
						}
						completed[index] = receipt.Observation
					}
				}
			}
			for _, host := range hosts {
				if held := host.call(t, hostRequest{Barrier: true}).Barrier; held == nil || *held {
					t.Fatal("the settled backup left its host reserved")
				}
			}
			writersRunning(t, true)
			nativeWaitPlacementRecovery(t)
		})
	}
}
