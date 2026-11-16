package agent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json/v2"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// canaryAccess is repository access as management releases it for one command.
var canaryAccess = backup.Access{Password: releaseCanary, AccessKey: "key-id", SecretKey: releaseCanary}

// PLAT-448. A backup, schedule or restore command waiting for attention keeps no repository access, however
// it came to wait, and still matches its own delivery. Other kinds keep their values until PLAT-457.
func TestParkedBackupsKeepNoRepositoryAccess(t *testing.T) {
	j, plan := testVisualJournal(t)
	generation := int64(0)
	for _, kind := range releasedKinds {
		generation++
		full := releasedCommand(t, plan, kind, uuid.New(), generation, true)
		// Each kind is written as running, as Accept writes it, beside the others.
		if err := j.db.Update(func(tx *bolt.Tx) error {
			return j.put(tx, "operations", full.Id.String(), Record{Command: full, Report: api.AgentReport{Id: full.Id, State: "running", Sequence: 1}, Checkpoint: "accepted"})
		}); err != nil {
			t.Fatal(kind, err)
		}
		if !bytes.Contains(sealedRecord(t, j, full.Id), []byte(releaseCanary)) {
			t.Fatal(kind, "a running command lost its values")
		}
		if err := j.Mutate(full.Id, func(r *Record) { settleReport(r, "needs_attention", "Inspect it.") }); err != nil {
			t.Fatal(err)
		}
		for range 2 {
			stored, err := j.Record(full.Id)
			if err != nil {
				t.Fatal(err)
			}
			held := bytes.Contains(sealedRecord(t, j, full.Id), []byte(releaseCanary))
			if parkedValues[kind] {
				if held {
					t.Fatal(kind, "a parked backup kept its repository access")
				}
				if !sameCommand(stored.Command, full) || stored.Command.PlanHash != full.PlanHash {
					t.Fatal(kind, "a parked backup no longer matches its command")
				}
			} else if !held {
				t.Fatal(kind, "a parked command of a kind that keeps its values lost them")
			}
			j = reopen(t, j)
		}
	}
}

// An earlier agent parked backups without removing their access. Opening the journal removes it.
func TestOpenRemovesAccessAnEarlierAgentParked(t *testing.T) {
	j, plan := testVisualJournal(t)
	c := releasedCommand(t, plan, "schedule", uuid.New(), 1, true)
	writeAsEarlierAgent(t, j, "operations", c.Id.String(), Record{Command: c, Report: api.AgentReport{Id: c.Id, State: "needs_attention", Sequence: 2}, Checkpoint: "accepted"})
	j = reopen(t, j)
	stored, err := j.Record(c.Id)
	if err != nil || !sameCommand(stored.Command, c) || bytes.Contains(sealedRecord(t, j, c.Id), []byte(releaseCanary)) {
		t.Fatal("opening kept a parked backup's access", err)
	}
	before := sealedRows(t, j)
	j = reopen(t, j)
	if !unchangedRows(before, sealedRows(t, j)) {
		t.Fatal("opening rewrote a parked backup it had already redacted")
	}
}

// A parked command takes its values back only once a retry wants them: not from a delivery while it
// is parked, such as a release made again after its lease, nor before the retry reaches the step that
// reads them. Parked again, or ended, it wants none.
func TestParkedBackupTakesValuesBackOnlyOnceWanted(t *testing.T) {
	j, plan := testVisualJournal(t)
	full := releasedCommand(t, plan, "backup", uuid.New(), 1, true)
	valueFree := releasedCommand(t, plan, "backup", full.Id, 1, false)
	valueFree.Deadline = full.Deadline
	if _, err := j.Accept(full); err != nil {
		t.Fatal(err)
	}
	park := func() {
		t.Helper()
		if err := j.Mutate(full.Id, func(r *Record) { settleReport(r, "needs_attention", "Inspect it.") }); err != nil {
			t.Fatal(err)
		}
	}
	held := func() bool { return bytes.Contains(sealedRecord(t, j, full.Id), []byte(releaseCanary)) }
	park()
	if r, err := j.Accept(full); err != nil || held() || r.ValuesWanted {
		t.Fatal("a parked backup took values from a delivery", err)
	}
	if err := j.Retry(full.Id); err != nil {
		t.Fatal(err)
	}
	if r, err := j.Accept(full); err != nil || held() || r.Report.State != "running" {
		t.Fatal("a retried backup took values before it wanted them", err)
	}
	want := func() {
		t.Helper()
		if err := j.Mutate(full.Id, func(r *Record) { r.ValuesWanted = true }); err != nil {
			t.Fatal(err)
		}
	}
	want()
	if r, err := j.Accept(valueFree); err != nil || held() || !r.ValuesWanted {
		t.Fatal("a delivery without values changed a backup that wants them", err)
	}
	r, err := j.Accept(full)
	if err != nil || !held() || r.ValuesWanted {
		t.Fatal("a backup that wants its values did not take them from a delivery that brings them", err)
	}
	stored, err := j.Record(full.Id)
	if err != nil || !sameCommand(stored.Command, full) || stored.Command.Backup == nil || !bytes.Equal(*stored.Command.Backup, *full.Backup) {
		t.Fatal("taking values back changed the command", err)
	}
	want()
	park()
	if r, err = j.Accept(full); err != nil || held() || r.ValuesWanted {
		t.Fatal("a backup parked again kept its values or still wants them", err)
	}
	if err = j.Retry(full.Id); err != nil {
		t.Fatal(err)
	}
	want()
	if err = j.Mutate(full.Id, func(r *Record) { settleReport(r, "cancelled", "Cancelled.") }); err != nil {
		t.Fatal(err)
	}
	if r, err = j.Accept(full); err != nil || held() || r.ValuesWanted {
		t.Fatal("an ended backup still wants values, or took them", err)
	}
}

// accessRestic stands in for restic. It answers a repository's config only when given the access
// it expects, so a command that runs without its access fails.
func accessRestic(t *testing.T, access backup.Access) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' 'restic " + backup.ResticVersion + " fixture'; exit 0; fi\n" +
		"[ \"$(cat \"$RESTIC_PASSWORD_FILE\")\" = '" + access.Password + "' ] || exit 3\n" +
		"[ \"$AWS_SECRET_ACCESS_KEY\" = '" + access.SecretKey + "' ] || exit 4\n" +
		"case \"$*\" in *'cat config'*) printf '{\"id\":\"" + strings.Repeat("e", 64) + "\",\"version\":2}\\n'; exit 0;; esac\n" +
		"exit 5\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// A retried check of a repository that gave up its access waits for it, with a note saying so, and
// runs with it once a delivery brings it back, even from a copy of the command read before then.
// A retried install past its first effect and its deadline is cancelled instead, since management
// releases nothing then.
func TestRetriedCheckWaitsForItsAccess(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	p.ID, p.Method, p.Command, p.DestinationID = uuid.New(), backup.QuiescedFiles, nil, uuid.New()
	destination := backup.Destination{ID: p.DestinationID, Name: "Bucket", Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "parked-fixture", Region: "test-1"}
	e.ResticCLI = accessRestic(t, canaryAccess)
	// An interrupted run of another policy holds the host, so the check is parked before it starts.
	other := backup.Policy{ID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, Generation: 1}
	uncertain := backup.Run{ID: uuid.New(), PolicyID: other.ID, State: "needs_attention", StartedAt: time.Now().UTC()}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: other}, Runs: []backup.Run{uncertain}}); err != nil {
		t.Fatal(err)
	}
	check := scheduleCommand(p, backup.Command{Action: "check", Installation: backup.Installation{Destination: destination, Access: canaryAccess}}, time.Now().Add(time.Minute))
	held := func() bool { return bytes.Contains(sealedRecord(t, e.Journal, check.Id), []byte(releaseCanary)) }
	ctx := context.Background()
	if err := e.Execute(ctx, check); err != nil {
		t.Fatal(err)
	}
	r, err := e.Journal.Record(check.Id)
	if err != nil || r.Report.State != "needs_attention" || held() {
		t.Fatal("the gate's parked check kept its access:", r.Report.State, err)
	}

	// The operator resolves the run and retries the check.
	uncertain.State = "failed"
	if err = e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: other}, Runs: []backup.Run{uncertain}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Journal.Retry(check.Id); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(check.Id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = e.Execute(ctx, r.Command); err != nil {
			t.Fatal(err)
		}
		if r, err = e.Journal.Record(check.Id); err != nil || r.Report.State != "running" || !r.ValuesWanted || r.Report.Note != valuesWantedNote {
			t.Fatal("a retried check ran without its access, or did not say it waits for it:", r.Report.State, r.ValuesWanted, r.Report.Note, err)
		}
	}
	// The executor read this copy before the delivery that brings the access back.
	read, waiting := r.Command, r.Report.Sequence
	if r, err = e.Journal.Accept(check); err != nil || !held() || r.ValuesWanted || r.Report.Note != "" || r.Report.Sequence <= waiting {
		t.Fatal("the delivery that brings the access back was not taken, or not reported over the wait", r.Report.Note, err)
	}
	if err = e.Execute(ctx, read); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(check.Id); err != nil || r.Report.State != "succeeded" {
		t.Fatal("a retried check with its access back did not run with it:", r.Report.State, r.Report.Note, err)
	}

	if err = e.Journal.Acknowledge(check.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	install := scheduleCommand(p, backup.Command{Action: "install", Installation: backup.Installation{Destination: destination}}, time.Now().Add(-time.Second))
	if _, err = e.Journal.Accept(install); err != nil {
		t.Fatal(err)
	}
	if err = e.Journal.Mutate(install.Id, func(r *Record) { r.Checkpoint = "backup-effect" }); err != nil {
		t.Fatal(err)
	}
	if err = e.Execute(ctx, install); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(install.Id); err != nil || r.Report.State != "cancelled" || r.Report.Note != valuesExpiredNote {
		t.Fatal("a retried install past its deadline was not cancelled:", r.Report.State, r.Report.Note, err)
	}
}

// A retried restore that gave up its access waits for it, and runs with it once a delivery brings
// it back, even from a copy of the command read before then. This restore then stops at its missing
// restic, which it reaches only past the wait.
func TestRetriedRestoreWaitsForItsAccess(t *testing.T) {
	j, plan := testVisualJournal(t)
	if err := j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"absent"}`))
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	e := &Engine{Journal: j, Docker: docker, ResticCLI: filepath.Join(t.TempDir(), "restic")}
	restore, v := recoveryCommand(t, plan, nil)
	held := func() bool {
		return bytes.Contains(sealedRecord(t, j, restore.Id), []byte(v.Installation.Access.Password))
	}
	if _, err = j.Accept(restore); err != nil {
		t.Fatal(err)
	}
	if err = j.Mutate(restore.Id, func(r *Record) { settleReport(r, "needs_attention", "Inspect it.") }); err != nil {
		t.Fatal(err)
	}
	if err = j.Retry(restore.Id); err != nil {
		t.Fatal(err)
	}
	r, err := j.Record(restore.Id)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = e.Execute(ctx, r.Command); err != nil {
		t.Fatal(err)
	}
	if r, err = j.Record(restore.Id); err != nil || r.Report.State != "running" || !r.ValuesWanted || r.Report.Note != valuesWantedNote || r.StartedAt != nil {
		t.Fatal("a retried restore ran without its access, or did not say it waits for it:", r.Report.State, r.ValuesWanted, r.Report.Note, err)
	}
	read := r.Command
	if r, err = j.Accept(restore); err != nil || !held() || r.ValuesWanted {
		t.Fatal("the delivery that brings the access back was not taken", err)
	}
	if err = e.Execute(ctx, read); err != nil {
		t.Fatal(err)
	}
	if r, err = j.Record(restore.Id); err != nil || r.Report.State != "failed" || !strings.Contains(r.Report.Note, "needs restic "+backup.ResticVersion) {
		t.Fatal("a retried restore with its access back did not run:", r.Report.State, r.Report.Note, err)
	}
}

// A command waiting for its access ends at its deadline though management is out of reach and no
// delivery wakes it, so it does not hold the host's local schedules beyond then.
func TestWaitForAccessEndsAtTheDeadlineWithoutManagement(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	p.ID, p.Method, p.Command, p.DestinationID = uuid.New(), backup.QuiescedFiles, nil, uuid.New()
	destination := backup.Destination{ID: p.DestinationID, Name: "Bucket", Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "parked-fixture", Region: "test-1"}
	// Long enough for a slow runner to start waiting before it.
	check := scheduleCommand(p, backup.Command{Action: "check", Installation: backup.Installation{Destination: destination}}, time.Now().Add(3*time.Second))
	if _, err := e.Journal.Accept(check); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(503) }))
	defer server.Close()
	cfg, err := e.Journal.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, "agent-credential"
	if err = e.Journal.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(e.Journal, e)
	c.HTTP = server.Client()
	c.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- c.Run(ctx) }()
	defer func() {
		cancel()
		<-stopped
	}()
	waited := false
	for limit := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		r, err := e.Journal.Record(check.Id)
		if err != nil {
			t.Fatal(err)
		}
		waited = waited || r.ValuesWanted
		if r.Report.State == "cancelled" {
			break
		}
		if time.Now().After(limit) {
			t.Fatal("a command waiting for its access outlived its deadline:", r.Report.State, r.Report.Note)
		}
	}
	if !waited {
		t.Fatal("the command did not wait for its access before its deadline")
	}
}

// A delivery without values of a command that wants them asks management to release them again,
// as for a lost record. One parked, or cancelled, asks for nothing.
func TestWantedValuesAreAskedForAgain(t *testing.T) {
	j, plan := testVisualJournal(t)
	var mu sync.Mutex
	var notices []api.SecretReleaseNotice
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var notice api.SecretReleaseNotice
		if err := json.UnmarshalRead(r.Body, &notice); err != nil {
			t.Error(err)
		}
		mu.Lock()
		notices = append(notices, notice)
		mu.Unlock()
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, plan.ServerID.String()+".credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := &Client{Journal: j, HTTP: server.Client()}
	ctx := context.Background()
	full := releasedCommand(t, plan, "backup", uuid.New(), 1, true)
	if _, err = j.Accept(full); err != nil {
		t.Fatal(err)
	}
	if err = j.Mutate(full.Id, func(r *Record) { settleReport(r, "needs_attention", "Inspect it.") }); err != nil {
		t.Fatal(err)
	}
	without := releasedCommand(t, plan, "backup", full.Id, 1, false)
	without.Deadline = full.Deadline
	without.SecretRelease = &api.SecretRelease{Epoch: 4, Digest: strings.Repeat("d", 64), Values: false}
	redeliveries := func() int {
		mu.Lock()
		defer mu.Unlock()
		n := 0
		for _, notice := range notices {
			for _, r := range notice.Redeliver {
				if r.OperationId == full.Id && r.Epoch == 4 && r.Reason == api.JournalMissing {
					n++
				}
			}
		}
		return n
	}
	if err = c.acceptReleased(ctx, without); err != nil || redeliveries() != 0 {
		t.Fatal("a parked command asked for its values", err)
	}
	if err = j.Retry(full.Id); err != nil {
		t.Fatal(err)
	}
	if err = j.Mutate(full.Id, func(r *Record) { r.ValuesWanted = true }); err != nil {
		t.Fatal(err)
	}
	if err = c.acceptReleased(ctx, without); err != nil || redeliveries() != 1 {
		t.Fatal("a command that wants its values did not ask for them", err)
	}
	stop := without
	stop.CancelRequested = true
	if err = c.acceptReleased(ctx, stop); err != nil || redeliveries() != 1 {
		t.Fatal("a cancelled command asked for its values", err)
	}
	with := full
	with.SecretRelease = &api.SecretRelease{Epoch: 4, Digest: strings.Repeat("d", 64), Values: true}
	if err = c.acceptReleased(ctx, with); err != nil {
		t.Fatal(err)
	}
	if r, err := j.Record(full.Id); err != nil || r.ValuesWanted || !bytes.Contains(sealedRecord(t, j, full.Id), []byte(releaseCanary)) {
		t.Fatal("a released delivery did not give a wanting command its values", err)
	}
}

// A restore stopped after it began changing this server is left to inspect without the repository's
// access, which settling it never reads. Retried past its deadline, it is still settled by its
// containers rather than waiting for its access or cancelled as though it changed nothing.
func TestRetriedRestorePastItsEffectSettlesWithoutItsAccess(t *testing.T) {
	j, plan := testVisualJournal(t)
	left := `[{"Id":"left","Names":["/left"],"State":"exited"}]`
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/containers/json") {
			// Until the operator clears it, a container that removal never takes away.
			_, _ = w.Write([]byte(left))
			return
		}
		w.WriteHeader(404)
		_, _ = w.Write([]byte(`{"message":"absent"}`))
	}))
	defer daemon.Close()
	docker, err := client.New(client.WithHost(daemon.URL), client.WithAPIVersion("1.54"))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	e := &Engine{Journal: j, Docker: docker}
	restore, v := recoveryCommand(t, plan, nil)
	restore.Deadline = time.Now().Add(-time.Minute)
	if _, err = j.Accept(restore); err != nil {
		t.Fatal(err)
	}
	if err = e.checkpoint(restore.Id, recoveryEffect); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = e.Execute(ctx, restore); err != nil {
		t.Fatal(err)
	}
	r, err := j.Record(restore.Id)
	if err != nil || r.Report.State != "needs_attention" || bytes.Contains(sealedRecord(t, j, restore.Id), []byte(v.Installation.Access.Password)) {
		t.Fatal("the restore was not left to inspect without its access:", r.Report.State, r.Report.Note, err)
	}
	left = `[]`
	if err = j.Retry(restore.Id); err != nil {
		t.Fatal(err)
	}
	if r, err = j.Record(restore.Id); err != nil {
		t.Fatal(err)
	}
	if err = e.Execute(ctx, r.Command); err != nil {
		t.Fatal(err)
	}
	if r, err = j.Record(restore.Id); err != nil || r.Report.State != "failed" || !strings.Contains(r.Report.Note, "Nothing it started is still running.") || r.ValuesWanted {
		t.Fatal("the retried restore was not settled by its containers:", r.Report.State, r.Report.Note, r.ValuesWanted, err)
	}
}
