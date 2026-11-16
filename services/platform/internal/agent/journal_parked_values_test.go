package agent

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"encoding/json/v2"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// canaryAccess is repository access as management releases it for one command.
var canaryAccess = backup.Access{Password: releaseCanary, AccessKey: "key-id", SecretKey: releaseCanary}

// PLAT-448. A backup or schedule command waiting for attention keeps no repository access, however
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
				if held || stored.Values != valuesRemoved {
					t.Fatal(kind, "a parked backup kept its repository access, or did not record removing it:", stored.Values)
				}
				if !sameCommand(stored.Command, full) || stored.Command.PlanHash != full.PlanHash {
					t.Fatal(kind, "a parked backup no longer matches its command")
				}
			} else if !held || stored.Values != "" {
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
	if err != nil || stored.Values != valuesRemoved || bytes.Contains(sealedRecord(t, j, c.Id), []byte(releaseCanary)) {
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
// reads them. Parked again, it gives them up again and wants none.
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
	if r, err := j.Accept(full); err != nil || held() || r.Values != valuesRemoved {
		t.Fatal("a parked backup took values from a delivery", err)
	}
	if err := j.Retry(full.Id); err != nil {
		t.Fatal(err)
	}
	if r, err := j.Accept(full); err != nil || held() || r.Report.State != "running" {
		t.Fatal("a retried backup took values before it wanted them", err)
	}
	if err := j.Mutate(full.Id, func(r *Record) { r.Values = valuesWanted }); err != nil {
		t.Fatal(err)
	}
	if r, err := j.Accept(valueFree); err != nil || held() || r.Values != valuesWanted {
		t.Fatal("a delivery without values changed a backup that wants them", err)
	}
	r, err := j.Accept(full)
	if err != nil || !held() || r.Values != "" {
		t.Fatal("a backup that wants its values did not take them from a delivery that brings them", err)
	}
	stored, err := j.Record(full.Id)
	if err != nil || !sameCommand(stored.Command, full) || stored.Command.Backup == nil || !bytes.Equal(*stored.Command.Backup, *full.Backup) {
		t.Fatal("taking values back changed the command", err)
	}
	if err = j.Mutate(full.Id, func(r *Record) { r.Values = valuesWanted }); err != nil {
		t.Fatal(err)
	}
	park()
	if stored, err = j.Record(full.Id); err != nil || held() || stored.Values != valuesRemoved {
		t.Fatal("a backup parked again kept its values or still wants them", err)
	}
}

// A retried install that gave up its access waits for it, with a note saying so, and runs once a
// delivery brings it back. Past its deadline it is cancelled instead, since management releases
// nothing then.
func TestRetriedInstallWaitsForItsAccess(t *testing.T) {
	e, p, _ := scheduledJobFixture(t, "")
	// An interrupted run of another policy holds the host, so the install is parked before it starts.
	other := backup.Policy{ID: uuid.New(), ServerID: p.ServerID, EnvironmentID: p.EnvironmentID, Generation: 1}
	uncertain := backup.Run{ID: uuid.New(), PolicyID: other.ID, State: "needs_attention", StartedAt: time.Now().UTC()}
	if err := e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: other}, Runs: []backup.Run{uncertain}}); err != nil {
		t.Fatal(err)
	}
	install := scheduleCommand(p, backup.Command{Action: "install", Installation: backup.Installation{Access: canaryAccess}}, time.Now().Add(time.Minute))
	held := func() bool { return bytes.Contains(sealedRecord(t, e.Journal, install.Id), []byte(releaseCanary)) }
	ctx := context.Background()
	if err := e.Execute(ctx, install); err != nil {
		t.Fatal(err)
	}
	r, err := e.Journal.Record(install.Id)
	if err != nil || r.Report.State != "needs_attention" || r.Values != valuesRemoved || held() {
		t.Fatal("the gate's parked install kept its access:", r.Report.State, r.Values, err)
	}

	// The operator resolves the run and retries the install.
	uncertain.State = "failed"
	if err = e.Journal.saveSchedule(scheduleState{Installation: backup.Installation{Policy: other}, Runs: []backup.Run{uncertain}}); err != nil {
		t.Fatal(err)
	}
	if err = e.Journal.Retry(install.Id); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(install.Id); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err = e.Execute(ctx, r.Command); err != nil {
			t.Fatal(err)
		}
		if r, err = e.Journal.Record(install.Id); err != nil || r.Report.State != "running" || r.Values != valuesWanted || r.Report.Note != valuesWantedNote || r.reached("backup-effect") {
			t.Fatal("a retried install ran without its access, or did not say it waits for it:", r.Report.State, r.Values, r.Report.Note, err)
		}
	}
	if _, err = e.Journal.Accept(install); err != nil || !held() {
		t.Fatal("the delivery that brings the access back was not taken", err)
	}
	if r, err = e.Journal.Record(install.Id); err != nil {
		t.Fatal(err)
	}
	if err = e.Execute(ctx, r.Command); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(install.Id); err != nil || r.Report.State != "succeeded" {
		t.Fatal("a retried install with its access back did not run:", r.Report.State, r.Report.Note, err)
	}

	// The same wait past the deadline ends the command.
	late := scheduleCommand(p, backup.Command{Action: "check"}, time.Now().Add(-time.Second))
	late.Generation = install.Generation + 10
	if err = e.Journal.Acknowledge(install.Id, r.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Journal.Accept(late); err != nil {
		t.Fatal(err)
	}
	if err = e.Journal.Mutate(late.Id, func(r *Record) { r.Values = valuesRemoved }); err != nil {
		t.Fatal(err)
	}
	if r, err = e.Journal.Record(late.Id); err != nil {
		t.Fatal(err)
	}
	if wait, err := e.awaitValues(late, r); !wait || err != nil {
		t.Fatal("a command past its deadline did not stop waiting", err)
	}
	if r, err = e.Journal.Record(late.Id); err != nil || r.Report.State != "cancelled" || r.Report.Note != valuesExpiredNote {
		t.Fatal("a command past its deadline was not cancelled:", r.Report.State, r.Report.Note, err)
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
	if err = j.Mutate(full.Id, func(r *Record) { r.Values = valuesWanted }); err != nil {
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
	if r, err := j.Record(full.Id); err != nil || r.Values != "" || !bytes.Contains(sealedRecord(t, j, full.Id), []byte(releaseCanary)) {
		t.Fatal("a released delivery did not give a wanting command its values", err)
	}
}
