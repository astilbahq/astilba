package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
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

// An older binary drops a destination's prefix, so it would back up at the bucket's root and save
// the schedule back without it. A prefixed schedule is kept where only this binary reads it, with
// a disabled copy for an older one, as an advanced cadence is.
func TestAPrefixedScheduleIsHiddenFromOlderBinaries(t *testing.T) {
	j, plan := testVisualJournal(t)
	p := backup.Policy{ID: uuid.New(), ServerID: plan.ServerID, EnvironmentID: plan.EnvironmentID, Generation: 1, Enabled: true, Method: "postgres-logical", Schedule: backup.Schedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Plan: plan}
	d := backup.Destination{ID: uuid.New(), Kind: "s3", Endpoint: "https://storage.example.test", Bucket: "backups", Prefix: "servers/web-1"}
	s := scheduleState{Installation: backup.Installation{Policy: p, Destination: d, Next: time.Date(2026, 11, 27, 2, 30, 0, 0, time.UTC)}}
	if err := j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	var legacy scheduleState
	if err := j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("schedules-v2")).Get([]byte(p.ID.String())) == nil {
			t.Error("the prefixed schedule was kept only where an older binary reads it")
		}
		return j.get(tx, "schedules", p.ID.String(), &legacy)
	}); err != nil {
		t.Fatal(err)
	}
	if legacy.Installation.Policy.Enabled || !legacy.Installation.Next.IsZero() {
		t.Fatal("an older binary could run the prefixed schedule")
	}
	got, err := j.schedule(p.ID)
	if err != nil || !got.Installation.Policy.Enabled || got.Installation.Destination.Prefix != d.Prefix {
		t.Fatal("this binary lost the prefixed schedule", err)
	}
	// Without a prefix it stays where it always was.
	s.Installation.Destination.Prefix = ""
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = j.db.View(func(tx *bolt.Tx) error {
		if tx.Bucket([]byte("schedules-v2")).Get([]byte(p.ID.String())) != nil {
			t.Error("an unprefixed daily schedule was hidden from older binaries")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
