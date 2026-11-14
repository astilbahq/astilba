package agent

import (
	"context"
	"encoding/json/jsontext"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// testCommandGeneration orders the commands a test sends, as management does.
var testCommandGeneration atomic.Int64

func scheduleCommand(p backup.Policy, b backup.Command, deadline time.Time) api.ExecutionCommand {
	b.Installation.Policy = p
	raw, _ := stablejson.Marshal(b)
	payload := jsontext.Value(raw)
	kind := api.ExecutionCommandKind("backup")
	if p.GeneralSchedule() {
		kind = "schedule"
	}
	return api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Kind: kind, Generation: testCommandGeneration.Add(1), PlanHash: b.Digest(), Backup: &payload, Deadline: deadline}
}

// PLAT-357. A run its command stops after the first effect is never left running, which nothing
// settles on a reserved host. It needs attention, as its command does, unless its outcome was
// confirmed: a job whose container was stopped and removed, with its exit recorded, has failed.
func TestRunStoppedAfterItsFirstEffectSettlesByWhatItConfirmed(t *testing.T) {
	cases := []struct {
		name, mode, run string
		cancel          bool
		command         api.ExecutionState
	}{
		{name: "deadline, cleanup confirmed", mode: "running", run: "failed", command: "cancelled"},
		{name: "cancel, cleanup confirmed", mode: "running", cancel: true, run: "failed", command: "cancelled"},
		{name: "deadline, cleanup unconfirmed", mode: "running-remove", run: "needs_attention", command: "needs_attention"},
		{name: "cancel, cleanup unconfirmed", mode: "running-remove", cancel: true, run: "needs_attention", command: "needs_attention"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p, created := scheduledJobFixture(t, tc.mode)
			p.TimeoutSeconds = 60
			if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: time.Now().Add(time.Hour)}}); err != nil {
				t.Fatal(err)
			}
			deadline := time.Now().Add(2 * time.Second)
			if tc.cancel {
				deadline = time.Now().Add(time.Minute)
			}
			c := scheduleCommand(p, backup.Command{Action: "backup", RunID: uuid.New()}, deadline)
			if tc.cancel {
				e.Point = func(point string) {
					if point == "backup-effects-begun" {
						cancelled := c
						cancelled.CancelRequested = true
						if _, err := e.Journal.Accept(cancelled); err != nil {
							t.Error(err)
						}
					}
				}
			}
			if err := e.Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			record, err := e.Journal.Record(c.Id)
			if err != nil || record.Report.State != tc.command {
				t.Fatal("the command was settled as", record.Report.State, record.Report.Note, err)
			}
			stored, err := e.Journal.schedule(p.ID)
			if err != nil || *created != 1 || len(stored.Runs) != 1 || stored.Runs[0].State != tc.run || stored.Runs[0].ExitCode == nil {
				t.Fatal("the run was settled as", stored.Runs, err)
			}
			if tc.run == "failed" {
				return
			}
			// resolve-run settles the command with its run, so neither holds the host's schedules.
			if err = e.ResolveScheduledRun(context.Background(), p.ID, c.Id); err == nil {
				t.Fatal("an unknown run was resolved")
			}
			if err = e.ResolveScheduledRun(context.Background(), p.ID, stored.Runs[0].ID); err != nil {
				t.Fatal(err)
			}
			if record, err = e.Journal.Record(c.Id); err != nil || record.Report.State != "failed" {
				t.Fatal("resolve-run left the command unsettled", record.Report.State, err)
			}
			pending, err := e.Journal.Pending()
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range pending {
				if !execution.Terminal(r.Report.State) {
					t.Fatal("a resolved run's command still holds the host's schedules", r.Report.State)
				}
			}
			if stored, err = e.Journal.schedule(p.ID); err != nil || hasUncertainRun(stored) {
				t.Fatal("the resolved run still holds the host", stored.Runs, err)
			}
		})
	}
}

// PLAT-358. A snapshot is protected from retention only while a restore of it is running or
// unresolved, whatever ended the restore, and history an earlier agent left protected is released.
func TestRestoreProtectionLastsOnlyWhileTheRestoreIsUnsettled(t *testing.T) {
	j, p := testVisualJournal(t)
	policy := backup.Policy{ID: uuid.New(), Name: "Data", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: uuid.New(), Generation: 1, Method: "container-job", Command: []string{"true"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	snapshot, other := strings.Repeat("a", 64), strings.Repeat("b", 64)
	at := time.Now().UTC()
	source := backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, ScheduledAt: at, StartedAt: at, State: "succeeded", Snapshot: snapshot}
	unrelated := backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, ScheduledAt: at, StartedAt: at, State: "succeeded", Snapshot: other}
	restore := backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, ScheduledAt: at, StartedAt: at, State: "running", Snapshot: snapshot, TargetID: uuid.New()}
	protected := func(s scheduleState) []bool {
		out := []bool{}
		for _, r := range s.Runs {
			out = append(out, r.Protected)
		}
		return out
	}
	for _, tc := range []struct {
		state string
		want  []bool
	}{
		{"running", []bool{true, false, true}},
		{"needs_attention", []bool{true, false, true}},
		{"failed", []bool{false, false, false}},
		{"succeeded", []bool{false, false, false}},
	} {
		restore.State = tc.state
		if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy}, Runs: []backup.Run{source, unrelated, restore}}); err != nil {
			t.Fatal(err)
		}
		stored, err := j.schedule(policy.ID)
		if err != nil || !slices.Equal(protected(stored), tc.want) {
			t.Fatal("a restore that is", tc.state, "left protection", protected(stored), err)
		}
	}

	// An earlier agent kept every failed restore's snapshot protected, until history could not
	// take another run.
	legacy := scheduleState{Installation: backup.Installation{Policy: policy}, Runs: []backup.Run{source}}
	legacy.Runs[0].Protected = true
	for range 127 {
		failed := restore
		failed.ID, failed.State, failed.Protected = uuid.New(), "failed", true
		legacy.Runs = append(legacy.Runs, failed)
	}
	legacy.Sequence = 1 << 20
	if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "schedules", policy.ID.String(), legacy) }); err != nil {
		t.Fatal(err)
	}
	stored, err := j.schedule(policy.ID)
	if err != nil || slices.Contains(protected(stored), true) {
		t.Fatal("protection outlived the restores that set it", err)
	}
	if err = trimRunHistory(&stored, 127); err != nil || len(stored.Runs) != 127 || stored.Runs[0].ID != source.ID {
		t.Fatal("history full of failed restores took no further run", err)
	}
}

// PLAT-358. A restore that fails releases its snapshot as soon as it ends.
func TestFailedRestoreReleasesItsSnapshot(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	snapshot := strings.Repeat("c", 64)
	at := time.Now().UTC()
	source := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: 1, ScheduledAt: at, StartedAt: at, State: "succeeded", Snapshot: snapshot}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at.Add(time.Hour)}, Runs: []backup.Run{source}}); err != nil {
		t.Fatal(err)
	}
	var during scheduleState
	e.Point = func(point string) {
		if point == "backup-run-recorded" {
			during, _ = e.Journal.schedule(p.ID)
		}
	}
	// A job's policy can't restore, so this restore fails once it starts.
	c := scheduleCommand(p, backup.Command{Action: "restore", RunID: uuid.New(), Snapshot: snapshot, TargetID: uuid.New()}, time.Now().Add(time.Minute))
	if err := e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if len(during.Runs) != 2 || !during.Runs[0].Protected || !during.Runs[1].Protected {
		t.Fatal("the restore did not protect its snapshot while it ran", during.Runs)
	}
	stored, err := e.Journal.schedule(p.ID)
	if err != nil || len(stored.Runs) != 2 || stored.Runs[1].State != "failed" || stored.Runs[0].Protected || stored.Runs[1].Protected {
		t.Fatal("a failed restore kept its snapshot protected", stored.Runs, err)
	}
}

// PLAT-359. A backup command marks its effect only just before one: a check, a disable and a job
// install change nothing outside the journal, which they commit with their success, so one
// stopped part way runs again rather than hold every schedule on the host for inspection. A
// backup still marks its run.
func TestBackupCommandsMarkTheirEffectOnlyWhenOneBegins(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	marked := func(c api.ExecutionCommand) bool {
		r, err := e.Journal.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		return r.Checkpoint == "backup-effect" || slices.ContainsFunc(r.Report.Steps, func(s api.ExecutionStep) bool { return s.Name == "backup-effect" })
	}
	deadline := time.Now().Add(time.Minute)
	install := scheduleCommand(p, backup.Command{Action: "install"}, deadline)
	check := scheduleCommand(p, backup.Command{Action: "check"}, deadline)
	disable := scheduleCommand(p, backup.Command{Action: "disable"}, deadline)
	run := scheduleCommand(p, backup.Command{Action: "backup", RunID: uuid.New()}, deadline)
	for _, tc := range []struct {
		c      api.ExecutionCommand
		state  api.ExecutionState
		marked bool
	}{{install, "succeeded", false}, {check, "failed", false}, {disable, "succeeded", false}, {run, "succeeded", true}} {
		if err := e.Execute(context.Background(), tc.c); err != nil {
			t.Fatal(err)
		}
		r, err := e.Journal.Record(tc.c.Id)
		if err != nil || r.Report.State != tc.state || marked(tc.c) != tc.marked {
			t.Fatal("a command marked an effect it does not have, or ended as", r.Report.State, r.Report.Note, err)
		}
		if err = e.Journal.Acknowledge(tc.c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	}
	// The disable was committed with its success.
	if stored, err := e.Journal.schedule(p.ID); err != nil || stored.Installation.Policy.Enabled {
		t.Fatal("the disable succeeded without its schedule", err)
	}
}

// A coordinated restore that fails its checks before it records its cutover intent has stopped
// nothing more than the pause did, so it fails: the parent then resumes the source rather than
// hold every host for an outcome that is already known. Once the intent is recorded, it needs
// attention.
func TestRestorePhaseFailingBeforeItsIntentIsRefused(t *testing.T) {
	for _, intent := range []bool{false, true} {
		j, p := testVisualJournal(t)
		p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true}
		e := &Engine{Journal: j}
		s := crossHostState{ParentID: uuid.New(), Unit: deployment.DistributedUnit{Plan: p}, Phase: "paused", Maintenance: &deployment.Maintenance{Kind: "restore"}}
		want := api.ExecutionState("failed")
		if intent {
			s.Phase, s.Restore, want = "restoring", &restoreCutoverState{Stage: "stopping"}, "needs_attention"
		}
		c := api.ExecutionCommand{Id: uuid.New(), ParentId: &s.ParentID, ServerId: p.ServerID, Kind: "crosshost"}
		if err := j.db.Update(func(tx *bolt.Tx) error {
			return j.put(tx, "operations", c.Id.String(), Record{Command: c, Report: api.AgentReport{Id: c.Id, State: "running"}, Checkpoint: "accepted"})
		}); err != nil {
			t.Fatal(err)
		}
		if err := e.executeMaintenancePhase(context.Background(), c, deployment.CrossHostCommand{Phase: "restore"}, s); err != nil {
			t.Fatal(err)
		}
		if r, err := j.Record(c.Id); err != nil || r.Report.State != want {
			t.Fatal("a restore phase with intent", intent, "was settled as", r.Report.State, err)
		}
	}
}
