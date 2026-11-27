package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
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
