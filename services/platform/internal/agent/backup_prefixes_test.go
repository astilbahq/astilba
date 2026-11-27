package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

// An agent says it keeps a destination's repositories under its prefix only to a controller that
// reads it, which would otherwise refuse the heartbeat's unknown capability, and stops once the
// controller no longer says it reads it.
func TestBackupPrefixesWaitForTheirNegotiation(t *testing.T) {
	j, _ := testVisualJournal(t)
	var offered atomic.Bool
	var received, rejected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Capabilities map[string]jsontext.Value `json:"capabilities"`
		}
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil {
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		if offered.Load() {
			w.Header().Set("Astilba-Agent-Backup-Prefixes", "v1")
		}
		if _, present := body.Capabilities["backupPrefixes"]; present {
			received.Add(1)
			if !offered.Load() {
				rejected.Add(1)
				w.WriteHeader(400)
				return
			}
		}
		_, _ = w.Write([]byte(`{"role":"combined","rotationRequested":false}`))
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin = server.URL
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP = server.Client()
	yes := true
	heartbeat := func() error {
		return c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{Capabilities: api.AgentCapabilities{BackupPrefixes: &yes}, Preflight: api.HostPreflight{Notes: []string{}}}, &api.AgentStatus{}, "fixture")
	}
	for range 2 {
		if err := heartbeat(); err != nil || received.Load() != 0 {
			t.Fatal("a controller that doesn't read backup prefixes was told of them", err)
		}
	}
	offered.Store(true)
	for range 2 {
		if err := heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 1 || rejected.Load() != 0 {
		t.Fatalf("backup prefixes advertised %d times after negotiation, %d refused", received.Load(), rejected.Load())
	}
	offered.Store(false)
	for range 2 {
		_ = heartbeat()
	}
	if received.Load() != 2 || rejected.Load() != 1 {
		t.Fatalf("after the controller stopped reading them, backup prefixes were advertised %d times in all, %d refused", received.Load(), rejected.Load())
	}
}

// releasedSchedule is the schedule the released binaries read for id: those from before general
// schedules read "schedules" alone, and later ones prefer "schedules-v2" unless "schedules" is
// newer. Neither knows a destination's prefix.
func releasedSchedule(t *testing.T, j *Journal, id uuid.UUID) (first, later scheduleState) {
	t.Helper()
	if err := j.db.View(func(tx *bolt.Tx) error {
		var advanced scheduleState
		if err := j.get(tx, "schedules", id.String(), &first); err != nil {
			return err
		}
		later = first
		err := j.get(tx, "schedules-v2", id.String(), &advanced)
		if err == nil && !(first.Installation.Policy.Generation > advanced.Installation.Policy.Generation || first.Installation.Policy.Generation == advanced.Installation.Policy.Generation && first.Sequence > advanced.Sequence) {
			later = advanced
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return first, later
}

// A released binary would back up a prefixed destination's policy to its bucket's root, so it
// never finds that schedule enabled, whatever its cadence. This binary still runs it, and an
// earlier binary's change to it since leaves it disabled here too, until management installs a new
// generation.
func TestAPrefixedScheduleIsHiddenFromOlderBinaries(t *testing.T) {
	j, plan := testVisualJournal(t)
	d := backup.Destination{ID: uuid.New(), Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "backups", Prefix: "servers/web-1"}
	next := time.Date(2026, 11, 27, 2, 30, 0, 0, time.UTC)
	cadences := map[string]backup.Schedule{"daily": {Hour: 2, Minute: 30, Timezone: "UTC"}, "general": {Timezone: "UTC", Kind: "interval", IntervalMinutes: 90}}
	for name, cadence := range cadences {
		p := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 1, Enabled: true, Method: "postgres-logical", Schedule: cadence, Plan: plan}
		s := scheduleState{Installation: backup.Installation{Policy: p, Destination: d, Next: next}}
		if err := j.saveSchedule(s); err != nil {
			t.Fatal(err)
		}
		for _, released := range func() []scheduleState { a, b := releasedSchedule(t, j, p.ID); return []scheduleState{a, b} }() {
			if released.Installation.Policy.Enabled || !released.Installation.Next.IsZero() || released.Installation.Policy.GeneralSchedule() {
				t.Fatalf("a released binary could run the %s prefixed schedule", name)
			}
		}
		got, err := j.schedule(p.ID)
		if err != nil || !got.Installation.Policy.Enabled || !got.Installation.Next.Equal(next) || got.Installation.Destination.Prefix != d.Prefix {
			t.Fatalf("this binary lost the %s prefixed schedule: %v", name, err)
		}
	}
	p := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 1, Enabled: true, Method: "postgres-logical", Schedule: cadences["daily"], Plan: plan}
	s := scheduleState{Installation: backup.Installation{Policy: p, Destination: d, Next: next}}
	if err := j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	// An earlier binary records something on the disabled mirror it reads, and writes it back
	// without the prefix it doesn't know.
	first, _ := releasedSchedule(t, j, p.ID)
	first.Sequence++
	first.Installation.Destination.Prefix = ""
	if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "schedules", p.ID.String(), first) }); err != nil {
		t.Fatal(err)
	}
	if got, err := j.schedule(p.ID); err != nil || got.Installation.Policy.Enabled || !got.Installation.Next.IsZero() || got.Sequence != first.Sequence {
		t.Fatal("this binary ignored a released binary's later change to the schedule", err)
	}
	// Without a prefix, the schedule is where released binaries read it.
	s.Installation.Destination.Prefix = ""
	if err := j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if _, later := releasedSchedule(t, j, p.ID); !later.Installation.Policy.Enabled {
		t.Fatal("an unprefixed schedule was hidden from released binaries")
	}
	if err := j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("schedules-v3")).Get([]byte(p.ID.String())) != nil {
			t.Error("an unprefixed schedule kept its prefixed record")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Retiring the environment removes its prefixed schedules too.
	if err := j.retireSchedules(plan.ServerID, plan.EnvironmentID); err != nil {
		t.Fatal(err)
	}
	if states, err := j.scheduleStates(); err != nil || len(states) != 0 {
		t.Fatal("retirement left schedules", len(states), err)
	}
}

// An earlier agent that retires an environment removes its schedules only from the buckets it
// knows. The prefixed schedule it leaves in schedules-v3 is gone once this agent opens the journal
// again, so it never runs for a retired environment; schedules it still keeps are untouched.
func TestAScheduleAnEarlierAgentRetiredStaysRetired(t *testing.T) {
	j, plan := testVisualJournal(t)
	dir := j.dir
	d := backup.Destination{ID: uuid.New(), Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "backups", Prefix: "servers/web-1"}
	next := time.Date(2026, 11, 27, 2, 30, 0, 0, time.UTC)
	policy := func(schedule backup.Schedule) backup.Policy {
		return backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 1, Enabled: true, Method: "postgres-logical", Schedule: schedule, Plan: plan}
	}
	retired, kept, general := policy(backup.Schedule{Hour: 2, Minute: 30, Timezone: "UTC"}), policy(backup.Schedule{Hour: 3, Timezone: "UTC"}), policy(backup.Schedule{Timezone: "UTC", Kind: "interval", IntervalMinutes: 90})
	for _, s := range []scheduleState{
		{Installation: backup.Installation{Policy: retired, Destination: d, Next: next}},
		{Installation: backup.Installation{Policy: kept, Destination: d, Next: next}},
		{Installation: backup.Installation{Policy: general, Destination: backup.Destination{ID: uuid.New(), Kind: "s3", Endpoint: d.Endpoint, Bucket: "other"}, Next: next}},
	} {
		if err := j.saveSchedule(s); err != nil {
			t.Fatal(err)
		}
	}
	// What an agent from before schedules-v3 does when it retires the schedule.
	if err := j.db.Update(func(tx *bolt.Tx) error {
		for _, bucket := range []string{"schedules", "schedules-v2"} {
			if err := tx.Bucket([]byte(bucket)).Delete([]byte(retired.ID.String())); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	states, err := j.scheduleStates()
	if err != nil {
		t.Fatal(err)
	}
	enabled := map[uuid.UUID]bool{}
	for _, s := range states {
		enabled[s.Installation.Policy.ID] = s.Installation.Policy.Enabled
	}
	if len(states) != 2 || !enabled[kept.ID] || !enabled[general.ID] {
		t.Fatalf("after reopening, the journal holds %v, want only the schedules still kept, enabled", enabled)
	}
}
