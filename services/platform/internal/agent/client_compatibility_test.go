package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	bolt "go.etcd.io/bbolt"
)

func TestBuildCapabilityNegotiatesWithControllerVersions(t *testing.T) {
	for _, capable := range []bool{false, true} {
		t.Run(map[bool]string{false: "deployment", true: "builder"}[capable], func(t *testing.T) {
			j, _ := testVisualJournal(t)
			var modern atomic.Bool
			var extensions atomic.Int32
			var cacheExtensions atomic.Int32
			var railpackExtensions atomic.Int32
			var releaseExtensions atomic.Int32
			var outputExtensions atomic.Int32
			var scheduleExtensions atomic.Int32
			var replicaExtensions atomic.Int32
			var continuationExtensions atomic.Int32
			var ingressExtensions atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !modern.Load() {
					// Every controller marks its responses; this one offers no extension.
					w.Header().Set("Astilba-Agent-Features", "none")
				}
				var body struct {
					Capabilities map[string]jsontext.Value `json:"capabilities"`
				}
				if err := json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				if _, present := body.Capabilities["maintenanceContinuation"]; present {
					continuationExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["primaryIngress"]; present {
					ingressExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["safeReleases"]; present {
					releaseExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["serviceOutputs"]; present {
					outputExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["schedules"]; present {
					scheduleExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["replicas"]; present {
					replicaExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["railpackVersion"]; present {
					railpackExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["buildControls"]; present {
					extensions.Add(1)
					// The previous protocol-1 request schema rejects this property, even false.
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if _, present := body.Capabilities["buildCache"]; present {
					cacheExtensions.Add(1)
					if !modern.Load() {
						w.WriteHeader(400)
						return
					}
				}
				if modern.Load() {
					w.Header().Set("Astilba-Agent-Features", "build-controls")
					w.Header().Set("Astilba-Agent-Build-Cache", "v1")
					w.Header().Set("Astilba-Agent-Railpack", "v1")
					w.Header().Set("Astilba-Agent-Safe-Releases", "v1")
					w.Header().Set("Astilba-Agent-Service-Outputs", "v1")
					w.Header().Set("Astilba-Agent-Schedules", "v1")
					w.Header().Set("Astilba-Agent-Replicas", "v1")
					w.Header().Set("Astilba-Agent-Cross-Host", "v1")
					w.Header().Set("Astilba-Agent-Maintenance-Continuation", "v1")
					w.Header().Set("Astilba-Agent-Primary-Ingress", "v1")
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"role":"combined","rotationRequested":false}`))
			}))
			defer server.Close()
			cfg, err := j.Config()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Origin = server.URL
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			c := NewClient(j, &Engine{Journal: j})
			c.HTTP = server.Client()
			c.Observe = func(context.Context) api.AgentHeartbeat {
				version := "0.43.0"
				return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Build: &capable, BuildControls: &capable, BuildCache: &capable, RailpackVersion: &version, SafeReleases: &capable, ServiceOutputs: &capable, Schedules: &capable, Replicas: &capable, CrossHost: &capable, MaintenanceContinuation: &capable, PrimaryIngress: &capable}}
			}
			if err = c.Enroll(context.Background(), strings.Repeat("a", 43)); err != nil {
				t.Fatal("older controller enrollment", err)
			}
			heartbeat := func() error {
				return c.call(context.Background(), "POST", "/agent/v1/heartbeat", c.Observe(context.Background()), &api.AgentStatus{}, "fixture")
			}
			if err = heartbeat(); err != nil || extensions.Load() != 0 || cacheExtensions.Load() != 0 || railpackExtensions.Load() != 0 || releaseExtensions.Load() != 0 || outputExtensions.Load() != 0 || scheduleExtensions.Load() != 0 || replicaExtensions.Load() != 0 || continuationExtensions.Load() != 0 || ingressExtensions.Load() != 0 {
				t.Fatal("older controller received capability extension", err)
			}
			modern.Store(true)
			if err = heartbeat(); err != nil || extensions.Load() != 0 || cacheExtensions.Load() != 0 || railpackExtensions.Load() != 0 || releaseExtensions.Load() != 0 || outputExtensions.Load() != 0 || scheduleExtensions.Load() != 0 || replicaExtensions.Load() != 0 || continuationExtensions.Load() != 0 || ingressExtensions.Load() != 0 {
				t.Fatal("advertised before negotiation", err)
			}
			if err = heartbeat(); err != nil || extensions.Load() != 1 || cacheExtensions.Load() != 1 || railpackExtensions.Load() != 1 || releaseExtensions.Load() != 1 || outputExtensions.Load() != 1 || scheduleExtensions.Load() != 1 || replicaExtensions.Load() != 1 || continuationExtensions.Load() != 1 || ingressExtensions.Load() != 1 {
				t.Fatal("supported extension not advertised", err)
			}
			modern.Store(false)
			if heartbeat() == nil {
				t.Fatal("old request schema fixture did not reject unsupported field")
			}
			if err = heartbeat(); err != nil || releaseExtensions.Load() != 1 || outputExtensions.Load() != 1 || scheduleExtensions.Load() != 1 || replicaExtensions.Load() != 1 || continuationExtensions.Load() != 2 || ingressExtensions.Load() != 1 {
				t.Fatal("controller downgrade did not clear negotiation", err)
			}
		})
	}
}

func TestCacheCapabilityDoesNotChangeBuildControlsNegotiation(t *testing.T) {
	j, _ := testVisualJournal(t)
	var received atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body api.AgentHeartbeat
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil || body.Capabilities.BuildCache != nil {
			t.Error("previous build-controls controller received an unknown cache capability")
			w.WriteHeader(400)
			return
		}
		if body.Capabilities.BuildControls != nil && *body.Capabilities.BuildControls {
			received.Add(1)
		}
		w.Header().Set("Astilba-Agent-Features", "build-controls")
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
	for range 2 {
		if err := c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{Capabilities: api.AgentCapabilities{BuildControls: &yes, BuildCache: &yes}}, &api.AgentStatus{}, "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 1 {
		t.Fatal("build-controls negotiation regressed")
	}
}

func TestProxyResponsesKeepNegotiation(t *testing.T) {
	proxied := map[string]struct {
		status int
		body   string
	}{
		"rate limit page":   {429, "<html>Too many requests</html>"},
		"bad gateway":       {502, "<html>Bad gateway</html>"},
		"unavailable":       {503, "no available server"},
		"gateway timeout":   {504, ""},
		"error envelope":    {400, `{"code":"invalid_input","message":"Rejected.","requestId":"request-1"}`},
		"maintenance page":  {200, "<html>Back soon</html>"},
		"empty JSON at 200": {200, "{}"},
	}
	for name, response := range proxied {
		t.Run(name, func(t *testing.T) {
			var behindProxy atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if behindProxy.Load() {
					w.WriteHeader(response.status)
					_, _ = w.Write([]byte(response.body))
					return
				}
				w.Header().Set("Astilba-Agent-Features", "build-controls")
				w.Header().Set("Astilba-Agent-Previews", "v1")
				_, _ = w.Write([]byte(`{"role":"combined","rotationRequested":false}`))
			}))
			defer server.Close()
			j, _ := testVisualJournal(t)
			cfg, err := j.Config()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Origin = server.URL
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			c := NewClient(j, &Engine{Journal: j})
			c.HTTP = server.Client()
			if err = c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{}, &api.AgentStatus{}, "fixture"); err != nil {
				t.Fatal(err)
			}
			behindProxy.Store(true)
			_ = c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{}, &api.AgentStatus{}, "fixture")
			yes := true
			advertised := c.advertisedCapabilities(api.AgentCapabilities{BuildControls: &yes, Previews: &yes, Schedules: &yes})
			if advertised.BuildControls == nil || advertised.Previews == nil || advertised.Schedules != nil {
				t.Fatalf("proxy response changed negotiation: %+v", advertised)
			}
		})
	}
}

// A restriction is admitted beside a record the controller keeps rejecting, so its report
// must still be sent even when its key sorts after the rejected record.
func TestRejectedReportDoesNotBlockLaterRecords(t *testing.T) {
	j, _ := testVisualJournal(t)
	rejected, later := uuid.MustParse("00000000-0000-4000-8000-000000000001"), uuid.MustParse("ffffffff-ffff-4fff-bfff-ffffffffffff")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/agent/v1/report" {
			w.WriteHeader(400)
			return
		}
		var report api.AgentReport
		if err := json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &report); err != nil || report.Id == rejected {
			w.WriteHeader(400)
			return
		}
		w.WriteHeader(204)
	}))
	defer server.Close()
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, "agent-credential"
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err = j.db.Update(func(tx *bolt.Tx) error {
		for _, id := range []uuid.UUID{rejected, later} {
			record := Record{Command: api.ExecutionCommand{Id: id, ServerId: cfg.ServerID, Kind: "test"}, Report: api.AgentReport{Id: id, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}}
			if err := j.put(tx, "operations", id.String(), record); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP = server.Client()
	c.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- c.Run(ctx) }()
	defer func() {
		cancel()
		<-stopped
	}()
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		record, err := j.Record(later)
		if err != nil {
			t.Fatal(err)
		}
		if record.Acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the later record was not reported")
		}
	}
	if record, err := j.Record(rejected); err != nil || record.Acknowledged {
		t.Fatal("rejected record was acknowledged", err)
	}
}

// A controller on an older release still answers polls with encoding/json v1, and
// the agent's own digest check depends on reading that encoding unchanged.
func TestPollResponsesStayStrictAcrossControllerVersions(t *testing.T) {
	j, _ := testVisualJournal(t)
	var answer atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Astilba-Agent-Features", "none")
		_, _ = w.Write(answer.Load().([]byte))
	}))
	defer server.Close()
	cfg, _ := j.Config()
	cfg.Origin = server.URL
	if err := j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	c := NewClient(j, &Engine{Journal: j})
	c.HTTP = server.Client()
	payload := jsontext.Value(`{"phase":"a && b <c>"}`)
	want := api.ExecutionCommand{Id: uuid.New(), ServerId: cfg.ServerID, Generation: 2, PlanHash: "hash", Kind: "crosshost", Action: "inspect", Deadline: time.Date(2026, 1, 2, 3, 4, 5, 6, time.UTC), Crosshost: &payload}
	raw, err := stablejson.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	answer.Store(raw)
	var got api.ExecutionCommand
	if err = c.call(context.Background(), "GET", "/agent/v1/poll", nil, &got, "fixture"); err != nil {
		t.Fatal(err)
	}
	wantPayload, _ := stablejson.Marshal(payload)
	if got.Id != want.Id || !got.Deadline.Equal(want.Deadline) || got.Crosshost == nil || string(*got.Crosshost) != string(wantPayload) {
		t.Fatalf("read %+v", got)
	}
	for name, body := range map[string]string{
		"case variant": strings.Replace(string(raw), `"planHash"`, `"PLANHASH"`, 1),
		"duplicate":    strings.Replace(string(raw), `"planHash"`, `"planHash":"other","planHash"`, 1),
		"trailing":     string(raw) + " {}",
	} {
		answer.Store([]byte(body))
		if err = c.call(context.Background(), "GET", "/agent/v1/poll", nil, &api.ExecutionCommand{}, "fixture"); err == nil {
			t.Fatalf("accepted a %s", name)
		}
	}
}

// A controller that coordinates cross-host deployments but predates ingress takeover rejects the
// capability it doesn't know, so the agent leaves it out until that controller offers it.
func TestPrimaryIngressWaitsForItsOwnNegotiation(t *testing.T) {
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
		w.Header().Set("Astilba-Agent-Cross-Host", "v1")
		if offered.Load() {
			w.Header().Set("Astilba-Agent-Primary-Ingress", "v1")
		}
		if _, present := body.Capabilities["primaryIngress"]; present {
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
		return c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{Capabilities: api.AgentCapabilities{CrossHost: &yes, PrimaryIngress: &yes}}, &api.AgentStatus{}, "fixture")
	}
	for range 2 {
		if err := heartbeat(); err != nil || received.Load() != 0 {
			t.Fatal("a cross-host controller without ingress takeover received the capability", err)
		}
	}
	offered.Store(true)
	for range 2 {
		if err := heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 1 || rejected.Load() != 0 {
		t.Fatalf("capability advertised %d times after negotiation, %d rejected", received.Load(), rejected.Load())
	}
}
