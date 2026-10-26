package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestGeneralScheduleRollbackFenceAndLegacyReconciliation(t *testing.T) {
	j, plan := testVisualJournal(t)
	at := time.Date(2026, 10, 26, 10, 0, 23, 0, time.UTC)
	p := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 1, Enabled: true, Method: "container-job", Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 7, Timezone: "UTC"}, Plan: plan}
	run := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: 1, State: "running"}
	s := scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{run}, Writers: []string{"owned-writer"}}
	if err := j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	var mirror scheduleState
	if err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "schedules", p.ID.String(), &mirror) }); err != nil {
		t.Fatal(err)
	}
	if mirror.Installation.Policy.Enabled || mirror.Installation.Policy.GeneralSchedule() || !mirror.Installation.Next.IsZero() || !hasUncertainRun(mirror.Runs) || len(mirror.Writers) != 1 {
		t.Fatal("old binary lost its effect fence or could execute the new cadence")
	}
	states, err := j.scheduleStates()
	if err != nil || len(states) != 1 || !states[0].Installation.Policy.GeneralSchedule() {
		t.Fatal("mirror exposed as another executable policy", err)
	}
	// Model the old binary's resolve-run write, which cannot update the new bucket.
	mirror.Sequence++
	mirror.Runs = append([]backup.Run{}, mirror.Runs...)
	mirror.Runs[0].State = "failed"
	mirror.Writers = nil
	if err = j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "schedules", p.ID.String(), mirror) }); err != nil {
		t.Fatal(err)
	}
	dir := j.dir
	j.Close()
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	effective, err := j.schedule(p.ID)
	if err != nil || effective.Installation.Policy.Enabled || effective.Installation.Policy.GeneralSchedule() || hasUncertainRun(effective.Runs) {
		t.Fatal("old resolution resurrected hidden advanced work", err)
	}
	// A newer owner-approved legacy generation must supersede the hidden row too.
	mirror.Installation.Policy.Generation = 2
	mirror.Installation.Policy.Enabled = true
	mirror.Installation.Next = at
	mirror.Sequence++
	if err = j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "schedules", p.ID.String(), mirror) }); err != nil {
		t.Fatal(err)
	}
	states, err = j.scheduleStates()
	if err != nil || len(states) != 1 || states[0].Installation.Policy.Generation != 2 || states[0].Installation.Policy.GeneralSchedule() {
		t.Fatal("stale advanced generation remained executable", err)
	}
	if err = j.saveSchedule(states[0]); err != nil {
		t.Fatal(err)
	}
	if err = j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("schedules-v2")).Get([]byte(p.ID.String())) != nil {
			t.Fatal("stale advanced state not retired")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestGeneralScheduleTransferRetainsAnchorAndUncertainty(t *testing.T) {
	j, plan := testVisualJournal(t)
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := j.Config()
	cfg.Origin = "https://old.example.test"
	cfg.Enrolled = true
	cfg.PublicKey = key
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	p := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, Generation: 3, Enabled: true, Schedule: backup.Schedule{Kind: "interval", IntervalMinutes: 7, Timezone: "UTC"}}
	at := time.Date(2026, 10, 26, 10, 0, 23, 0, time.UTC)
	s := scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{{ID: uuid.New(), PolicyID: p.ID, Generation: 3, State: "needs_attention"}}}
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); err == nil || !strings.Contains(err.Error(), "schedule") {
		t.Fatalf("transfer ignored advanced uncertainty: %v", err)
	}
	s.Runs[0].State = "failed"
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	inventory, err := j.Inventory()
	if err != nil || len(inventory.Schedules) != 1 || inventory.Schedules[p.ID.String()] != 3 {
		t.Fatal("advanced schedule missing/duplicated in inventory", err)
	}
	saved, err := j.schedule(p.ID)
	if err != nil || !saved.Installation.Next.Equal(at) || saved.Installation.Policy.Schedule.IntervalMinutes != 7 {
		t.Fatal("transfer reset the interval anchor", err)
	}
}

func TestGeneralScheduleRestartSkipsMissedJobsOnce(t *testing.T) {
	for _, schedule := range []backup.Schedule{{Kind: "interval", IntervalMinutes: 7, Timezone: "UTC"}, {Kind: "cron", Expression: "*/7 * * * *", Timezone: "UTC"}} {
		t.Run(schedule.Kind, func(t *testing.T) {
			j, _ := testVisualJournal(t)
			at := time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)
			p := backup.Policy{ID: uuid.New(), Generation: 1, Enabled: true, Method: "container-job", Schedule: schedule}
			if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
				t.Fatal(err)
			}
			dir := j.dir
			j.Close()
			j, err := Open(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer j.Close()
			e := Engine{Journal: j}
			forward := at.Add(19 * time.Minute)
			for _, now := range []time.Time{forward, at.Add(-time.Hour), forward} {
				if err = e.tickSchedules(context.Background(), func() time.Time { return now }); err != nil {
					t.Fatal(err)
				}
			}
			s, err := j.schedule(p.ID)
			if err != nil || len(s.Runs) != 1 || s.Runs[0].State != "skipped" || !s.Installation.Next.After(forward) {
				t.Fatal("restarted/backwards clock replayed missed job", err)
			}
		})
	}
}
