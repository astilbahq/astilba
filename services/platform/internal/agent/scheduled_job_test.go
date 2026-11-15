package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type scheduleTransport func(*http.Request) (*http.Response, error)

func (f scheduleTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// scheduledJobFixture fails one Docker call named by mode. A mode prefixed "running" keeps the
// job running until it is stopped.
func scheduledJobFixture(t *testing.T, mode string) (*Engine, backup.Policy, *int) {
	running := strings.HasPrefix(mode, "running")
	cleanupFailure := strings.TrimPrefix(strings.TrimPrefix(mode, "running"), "-")
	stopped := false
	j, p := testVisualJournal(t)
	app := uuid.New()
	image := execution.TestImage
	p.Specification.Applications = []domain.Application{{ID: app, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Command: []string{"sleep", "86400"}}}
	p.Images = map[string]string{app.String(): image}
	p = p.WithHash()
	policy := backup.Policy{ID: uuid.New(), Name: "Worker", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: app, Generation: 1, Method: "container-job", Command: []string{"sleep", "20"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	if err := policy.Validate(); err != nil {
		t.Fatal("fixture policy invalid", err)
	}
	created, jobInspects, removals := 0, 0, 0
	// A create that fails may still have made the container, as Docker's can.
	exists := false
	var jobLabels map[string]string
	source := map[string]any{"Id": "source", "Image": image, "Config": map[string]any{"Image": image, "Labels": visualLabels(p, app)}, "HostConfig": map[string]any{}, "State": map[string]any{"Running": true, "Status": "running"}}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.Path
		switch {
		case strings.HasSuffix(path, "/containers/json") && strings.Contains(r.URL.Query().Get("filters"), "com.astilba.run="):
			jobs := []any{}
			if exists {
				state := "exited"
				if running && !stopped {
					state = "running"
				}
				jobs = append(jobs, map[string]any{"Id": "job", "Labels": jobLabels, "Names": []string{"/astilba-job"}, "State": state})
			}
			stablejson.Encode(w, jobs)
		case strings.HasSuffix(path, "/containers/json"):
			stablejson.Encode(w, []any{map[string]any{"Id": "source", "Labels": visualLabels(p, app), "Names": []string{"/" + p.ContainerName(app)}}})
		case strings.HasSuffix(path, "/containers/source/json"):
			stablejson.Encode(w, source)
		case strings.HasSuffix(path, "/containers/create"):
			created++
			exists = true
			var b struct {
				Labels map[string]string `json:"Labels"`
			}
			json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &b)
			jobLabels = b.Labels
			jobInspects = 0
			if cleanupFailure == "create" {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"create failed"}`)
				return
			}
			w.WriteHeader(201)
			fmt.Fprint(w, `{"Id":"job","Warnings":[]}`)
		case strings.HasSuffix(path, "/containers/job/start"):
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/containers/job/stop"):
			stopped = true
			w.WriteHeader(204)
		case strings.HasSuffix(path, "/containers/job/json"):
			jobInspects++
			if cleanupFailure == "inspect" && jobInspects == 2 {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"cleanup inspect failed"}`)
				return
			}
			if running && !stopped {
				stablejson.Encode(w, map[string]any{"Id": "job", "Config": map[string]any{"Labels": jobLabels}, "State": map[string]any{"Running": true, "Status": "running"}})
				return
			}
			stablejson.Encode(w, map[string]any{"Id": "job", "Config": map[string]any{"Labels": jobLabels}, "State": map[string]any{"Running": false, "Status": "exited", "ExitCode": 0}})
		case strings.HasSuffix(path, "/containers/job") && r.Method == "DELETE":
			removals++
			if cleanupFailure == "remove" || cleanupFailure == "remove-once" && removals == 1 {
				w.WriteHeader(500)
				fmt.Fprint(w, `{"message":"cleanup remove failed"}`)
				return
			}
			exists = false
			w.WriteHeader(204)
		default:
			w.WriteHeader(404)
			fmt.Fprint(w, `{"message":"absent"}`)
		}
	})
	transport := scheduleTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w.Result(), nil
	})
	docker, err := client.New(client.WithHost("http://docker-fixture"), client.WithHTTPClient(&http.Client{Transport: transport}), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	e := &Engine{Journal: j, Docker: docker}
	stored := p
	stored.ID = uuid.Nil
	if err = j.saveUnit(unitState{Plan: stored}); err != nil {
		t.Fatal(err)
	}
	_, expected, err := e.observeUnit(context.Background(), p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	return e, policy, &created
}

func TestHydratedScheduleCancellationPreservesAcceptedCredentials(t *testing.T) {
	for _, kind := range []api.ExecutionCommandKind{"backup", "schedule"} {
		t.Run(string(kind), func(t *testing.T) {
			j, p := testVisualJournal(t)
			b := backup.Command{Action: "backup", Installation: backup.Installation{Policy: backup.Policy{ID: uuid.New()}, Access: backup.Access{Password: "journal-credential-canary"}}}
			if kind == "schedule" {
				b.Installation.Policy.Schedule = backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}
			}
			raw, _ := stablejson.Marshal(b)
			payload := jsontext.Value(raw)
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: b.Installation.Policy.ID, Kind: kind, Generation: 1, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
			if _, err := j.Accept(c); err != nil {
				t.Fatal(err)
			}
			b.Installation.Access = backup.Access{}
			raw, _ = stablejson.Marshal(b)
			bare := jsontext.Value(raw)
			c.Backup = &bare
			c.CancelRequested = true
			record, err := j.Accept(c)
			if err != nil || !record.Command.CancelRequested || !strings.Contains(string(*record.Command.Backup), "journal-credential-canary") {
				t.Fatal("credential-free cancellation lost accepted intent/access", err)
			}
			b.Installation.Policy.TimeoutSeconds = 5
			raw, _ = stablejson.Marshal(b)
			changed := jsontext.Value(raw)
			c.Backup = &changed
			if _, err = j.Accept(c); err == nil {
				t.Fatal("cancellation changed frozen execution inputs")
			}
		})
	}
}
func TestManualAdvancedRunRejectsSupersedingLegacyMirror(t *testing.T) {
	j, p := testVisualJournal(t)
	policy := backup.Policy{ID: uuid.New(), Name: "Job", WorkspaceID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, ResourceID: uuid.New(), Generation: 1, Method: "container-job", Command: []string{"true"}, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 1, Timezone: "UTC"}, TimeoutSeconds: 1, Retain: 1, Enabled: true, Plan: p}
	if err := policy.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy, Next: time.Now().Add(time.Minute)}}); err != nil {
		t.Fatal(err)
	}
	if err := j.db.Update(func(tx *bolt.Tx) error {
		var mirror scheduleState
		if err := j.get(tx, "schedules", policy.ID.String(), &mirror); err != nil {
			return err
		}
		mirror.Sequence++
		return j.put(tx, "schedules", policy.ID.String(), mirror)
	}); err != nil {
		t.Fatal(err)
	}
	b := backup.Command{Action: "backup", Installation: backup.Installation{Policy: policy}, RunID: uuid.New()}
	raw, _ := stablejson.Marshal(b)
	payload := jsontext.Value(raw)
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: policy.ID, Kind: "schedule", Generation: 2, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
	e := Engine{Journal: j}
	if err := e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	record, err := j.Record(c.Id)
	if err != nil || record.Report.State != "failed" {
		t.Fatal("manual mirror execution was admitted", err)
	}
	state, err := j.schedule(policy.ID)
	if err != nil || len(state.Runs) != 0 {
		t.Fatal("manual execution created effects before current installation", err)
	}
}

// PLAT-366. A known exit does not settle a job whose container may remain. One its own cleanup
// lost track of is found by its run label and removed, so the run settles by its exit, succeeding
// here, and the next occurrence runs. One Docker won't remove holds the host until it is
// resolved, so the next doesn't.
func TestKnownJobExitDoesNotReleaseUnconfirmedCleanupFence(t *testing.T) {
	for _, tc := range []struct {
		failure string
		state   string
		runs    int
	}{{"inspect", "succeeded", 2}, {"remove", "needs_attention", 1}} {
		t.Run(tc.failure, func(t *testing.T) {
			e, p, created := scheduledJobFixture(t, tc.failure)
			at := time.Now().UTC()
			if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				now := at.Add(time.Duration(i) * time.Minute)
				if err := e.tickSchedules(context.Background(), func() time.Time { return now }); err != nil {
					t.Fatal(err)
				}
			}
			state, err := e.Journal.schedule(p.ID)
			if err != nil || *created != tc.runs || len(state.Runs) != tc.runs || state.Runs[0].State != tc.state || state.Runs[0].ExitCode == nil {
				t.Fatal("a known exit settled the job by the wrong cleanup", state.Runs, err)
			}
		})
	}
}

// A container job that fails before it asks Docker for a container has changed nothing, so it
// must not hold the host: a deployment that moved the plan before the policy was refreshed fails
// the run, and the refresh and the host's other schedules still go ahead.
func TestJobFailingBeforeItsContainerDoesNotHoldTheHost(t *testing.T) {
	e, current, created := scheduledJobFixture(t, "")
	stale := current
	stale.ID, stale.Name = uuid.New(), "Stale"
	stale.Plan.RevisionID = uuid.New()
	stale.Plan = stale.Plan.WithHash()
	if err := stale.Validate(); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC()
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: stale, Next: at.Add(-time.Second)}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: current, Next: at}}); err != nil {
		t.Fatal(err)
	}
	if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	failed, err := e.Journal.schedule(stale.ID)
	if err != nil || len(failed.Runs) != 1 || failed.Runs[0].State != "failed" || failed.BeforeEffects != uuid.Nil {
		t.Fatal("a stale policy's run was not recorded as failed", err, failed.Runs)
	}
	ran, err := e.Journal.schedule(current.ID)
	if err != nil || *created != 1 || len(ran.Runs) != 1 || ran.Runs[0].State != "succeeded" {
		t.Fatal("another due schedule was held by a run that changed nothing", err, ran.Runs)
	}

	refreshed := stale
	refreshed.Generation, refreshed.Plan = 2, current.Plan
	b := backup.Command{Action: "install", Installation: backup.Installation{Policy: refreshed}}
	raw, _ := stablejson.Marshal(b)
	payload := jsontext.Value(raw)
	kind := api.ExecutionCommandKind("backup")
	if refreshed.GeneralSchedule() {
		kind = "schedule"
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: refreshed.ServerID, EnvironmentId: refreshed.EnvironmentID, ResourceId: refreshed.ID, Kind: kind, Generation: 1, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
	if err = e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	if record, err := e.Journal.Record(c.Id); err != nil || record.Report.State != "succeeded" {
		t.Fatal("the policy refresh was not admitted", err, record.Report.State, record.Report.Note)
	}
}

// A run is uncertain only once it may have changed something outside the journal: one the agent
// stopped during before that fails, and one it stopped after it still needs inspection. A job
// whose container create failed fails once the container Docker made anyway is found and removed.
func TestJobUncertaintyStartsAtItsFirstEffect(t *testing.T) {
	t.Run("restart before effects", func(t *testing.T) {
		e, p, created := scheduledJobFixture(t, "")
		at := time.Now().UTC()
		run := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: at, State: "running"}
		if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{run}, BeforeEffects: run.ID}); err != nil {
			t.Fatal(err)
		}
		if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
		state, err := e.Journal.schedule(p.ID)
		if err != nil || len(state.Runs) != 2 || state.Runs[0].State != "failed" || state.Runs[0].FinishedAt == nil || state.Runs[1].State != "succeeded" || *created != 1 {
			t.Fatal("a run stopped before any effect held the schedule", err, state.Runs)
		}
	})
	t.Run("restart after effects", func(t *testing.T) {
		e, p, created := scheduledJobFixture(t, "")
		at := time.Now().UTC()
		run := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: at, State: "running"}
		if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{run}}); err != nil {
			t.Fatal(err)
		}
		if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
		state, err := e.Journal.schedule(p.ID)
		if err != nil || len(state.Runs) != 1 || state.Runs[0].State != "needs_attention" || *created != 0 {
			t.Fatal("a run that may have had effects was not held for inspection", err, state.Runs)
		}
	})
	t.Run("create failed", func(t *testing.T) {
		e, p, created := scheduledJobFixture(t, "create")
		at := time.Now().UTC()
		if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
			t.Fatal(err)
		}
		if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
			t.Fatal(err)
		}
		state, err := e.Journal.schedule(p.ID)
		if err != nil || *created != 1 || len(state.Runs) != 1 || state.Runs[0].State != "failed" {
			t.Fatal("a failed create's container was not found and removed", err, state.Runs)
		}
		if left, err := e.Docker.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", "com.astilba.run="+state.Runs[0].ID.String())}); err != nil || len(left.Items) != 0 {
			t.Fatal("the container a failed create made was left", left.Items, err)
		}
	})
}

// A backup command the agent stopped during is held for inspection only while its run may have
// changed something with an unknown outcome. A run that never started, stopped before its first
// effect, or had already failed settles the command, cancelled or not.
func TestInterruptedRunCommandSettlesByItsRun(t *testing.T) {
	cases := []struct {
		name, action      string
		run               string // "" for a run never recorded
		marked, cancel    bool
		command, recorded string
	}{
		{name: "never recorded", command: "failed"},
		{name: "before effects", run: "running", marked: true, command: "failed", recorded: "failed"},
		{name: "before effects cancelled", run: "running", marked: true, cancel: true, command: "cancelled", recorded: "failed"},
		{name: "already failed", run: "failed", command: "failed", recorded: "failed"},
		{name: "already failed cancelled", run: "failed", cancel: true, command: "cancelled", recorded: "failed"},
		// The agent can stop after the run succeeds and before its command finishes.
		{name: "already succeeded", run: "succeeded", command: "succeeded", recorded: "succeeded"},
		{name: "already succeeded cancelled", run: "succeeded", cancel: true, command: "succeeded", recorded: "succeeded"},
		{name: "check cancelled", action: "check", cancel: true, command: "cancelled"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, p, _ := scheduledJobFixture(t, "")
			at := time.Now().UTC()
			action := tc.action
			if action == "" {
				action = "backup"
			}
			b := backup.Command{Action: action, Installation: backup.Installation{Policy: p}, RunID: uuid.New()}
			raw, _ := stablejson.Marshal(b)
			payload := jsontext.Value(raw)
			kind := api.ExecutionCommandKind("backup")
			if p.GeneralSchedule() {
				kind = "schedule"
			}
			c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Kind: kind, Generation: 1, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
			if _, err := e.Journal.Accept(c); err != nil {
				t.Fatal(err)
			}
			if err := e.checkpoint(c.Id, "backup-effect"); err != nil {
				t.Fatal(err)
			}
			if tc.cancel {
				c.CancelRequested = true
				if _, err := e.Journal.Accept(c); err != nil {
					t.Fatal(err)
				}
			}
			state := scheduleState{Installation: backup.Installation{Policy: p, Next: at.Add(time.Hour)}}
			if tc.run != "" {
				state.Runs = []backup.Run{{ID: b.RunID, PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: at, State: tc.run}}
			}
			if tc.marked {
				state.BeforeEffects = b.RunID
			}
			if err := e.Journal.saveSchedule(state); err != nil {
				t.Fatal(err)
			}
			if err := e.Execute(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			record, err := e.Journal.Record(c.Id)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := e.Journal.schedule(p.ID)
			if err != nil || record.Report.State != api.ExecutionState(tc.command) || stored.BeforeEffects != uuid.Nil || hasUncertainRun(stored) {
				t.Fatal("an interrupted run command was settled as", record.Report.State, err)
			}
			if tc.recorded != "" && (len(stored.Runs) != 1 || stored.Runs[0].State != tc.recorded) {
				t.Fatal("the interrupted run was settled as", stored.Runs)
			}
		})
	}
}

// The mark goes into the journal with the run and leaves it before the first effect. A backup's
// placement, before its repository, is held by the host test that interrupts an upload.
func TestRunsAreMarkedUntilTheirFirstEffect(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	at := time.Now().UTC()
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
		t.Fatal(err)
	}
	seen := map[string]uuid.UUID{}
	e.Point = func(point string) {
		if s, err := e.Journal.schedule(p.ID); err == nil {
			seen[point] = s.BeforeEffects
		}
	}
	if err := e.tickSchedules(context.Background(), func() time.Time { return at }); err != nil {
		t.Fatal(err)
	}
	stored, err := e.Journal.schedule(p.ID)
	if err != nil || len(stored.Runs) != 1 {
		t.Fatal(err, stored.Runs)
	}
	recorded, recordedOK := seen["backup-run-recorded"]
	begun, begunOK := seen["backup-effects-begun"]
	if !recordedOK || recorded != stored.Runs[0].ID || !begunOK || begun != uuid.Nil || stored.BeforeEffects != uuid.Nil {
		t.Fatal("the run was not marked from its record to its first effect", seen, stored.Runs)
	}
}

// A run cancelled before its first effect changed nothing, so it fails rather than wait.
func TestRunCancelledBeforeItsEffectsFails(t *testing.T) {
	e, p, created := scheduledJobFixture(t, "")
	at := time.Now().UTC()
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.Point = func(point string) {
		if point == "backup-run-recorded" {
			cancel()
		}
	}
	if err := e.tickSchedules(ctx, func() time.Time { return at }); err == nil {
		t.Fatal("a cancelled tick reported no error")
	}
	stored, err := e.Journal.schedule(p.ID)
	if err != nil || *created != 0 || len(stored.Runs) != 1 || stored.Runs[0].State != "failed" || hasUncertainRun(stored) {
		t.Fatal("a run cancelled before its first effect was left uncertain", err, stored.Runs)
	}
}

// A policy refresh can reach a restarted agent before its first tick. It settles a run stopped
// before its first effect rather than carry it on as uncertain.
func TestPolicyRefreshSettlesARunStoppedBeforeItsEffects(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	at := time.Now().UTC()
	run := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: at, State: "running"}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{run}, BeforeEffects: run.ID}); err != nil {
		t.Fatal(err)
	}
	refreshed := p
	refreshed.Generation = 2
	b := backup.Command{Action: "install", Installation: backup.Installation{Policy: refreshed}}
	raw, _ := stablejson.Marshal(b)
	payload := jsontext.Value(raw)
	kind := api.ExecutionCommandKind("backup")
	if refreshed.GeneralSchedule() {
		kind = "schedule"
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: p.ServerID, EnvironmentId: p.EnvironmentID, ResourceId: p.ID, Kind: kind, Generation: 1, PlanHash: b.Digest(), Backup: &payload, Deadline: time.Now().Add(time.Minute)}
	if err := e.Execute(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	record, err := e.Journal.Record(c.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatal("the refresh was not admitted", err, record.Report.Note)
	}
	stored, err := e.Journal.schedule(p.ID)
	if err != nil || stored.Installation.Policy.Generation != 2 || len(stored.Runs) != 1 || stored.Runs[0].State != "failed" || hasUncertainRun(stored) {
		t.Fatal("the refresh carried on a run that changed nothing as uncertain", err, stored.Runs)
	}
}
