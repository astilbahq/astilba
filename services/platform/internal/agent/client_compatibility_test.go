package agent

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
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
	// The challenge that negotiates first lets both heartbeats carry what the controller reads.
	if received.Load() != 2 {
		t.Fatal("build-controls negotiation regressed")
	}
}

// A controller that reads Railpack's version but not its pins rejects the digest, so an agent
// sends it only to one that advertises Astilba-Agent-Railpack-Inputs.
func TestRailpackInputsNeedTheirOwnNegotiation(t *testing.T) {
	j, _ := testVisualJournal(t)
	var reads atomic.Bool
	var received, rejected atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// A controller marks every response with what it reads, refusals included.
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		w.Header().Set("Astilba-Agent-Railpack", "v1")
		if reads.Load() {
			w.Header().Set("Astilba-Agent-Railpack-Inputs", "v1")
		}
		var body api.AgentHeartbeat
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil || body.Capabilities.RailpackVersion == nil {
			w.WriteHeader(400)
			return
		}
		if body.Capabilities.RailpackInputs != nil {
			// The previous request schema rejects the unknown property.
			if !reads.Load() {
				rejected.Add(1)
				w.WriteHeader(400)
				return
			}
			received.Add(1)
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
	version, digest := "0.43.0", strings.Repeat("a", 64)
	heartbeat := func() error {
		return c.call(context.Background(), "POST", "/agent/v1/heartbeat", api.AgentHeartbeat{Capabilities: api.AgentCapabilities{RailpackVersion: &version, RailpackInputs: &digest}}, &api.AgentStatus{}, "fixture")
	}
	for range 2 {
		if err := heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 0 || rejected.Load() != 0 {
		t.Fatal("railpackInputs sent before negotiation")
	}
	reads.Store(true)
	for range 2 {
		if err := heartbeat(); err != nil {
			t.Fatal(err)
		}
	}
	if received.Load() != 1 {
		t.Fatal("railpackInputs not sent once the controller advertised it", received.Load())
	}
	reads.Store(false)
	if err := heartbeat(); err == nil {
		t.Fatal("a downgraded controller accepted railpackInputs")
	}
	if err := heartbeat(); err != nil || received.Load() != 1 || rejected.Load() != 1 {
		t.Fatal("a controller downgrade did not clear the extension", err)
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

// Management refuses builds to a runner whose stored capabilities lack build isolation, and
// a heartbeat replaces them, so even the first one after a start must carry it.
func TestFirstHeartbeatCarriesWhatTheControllerAccepts(t *testing.T) {
	j, _ := testVisualJournal(t)
	var heartbeats []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Astilba-Agent-Features", "build-controls")
		w.Header().Set("Astilba-Agent-Build-Network", "v1")
		w.Header().Set("Astilba-Agent-Rootless-Builds", "v1")
		if r.URL.Path != "/agent/v1/heartbeat" {
			w.WriteHeader(404)
			return
		}
		var body api.AgentHeartbeat
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil {
			w.WriteHeader(400)
			return
		}
		heartbeats = append(heartbeats, body.Capabilities.BuildNetwork != nil && *body.Capabilities.BuildNetwork && body.Capabilities.RootlessBuilds != nil && *body.Capabilities.RootlessBuilds)
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
	c.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Build: &yes, BuildNetwork: &yes, RootlessBuilds: &yes}, Preflight: api.HostPreflight{Notes: []string{}}}
	}
	for range 2 {
		if err := c.heartbeat(context.Background(), "fixture", &api.AgentStatus{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(heartbeats) != 2 || !heartbeats[0] || !heartbeats[1] {
		t.Fatal("heartbeats advertised build isolation as", heartbeats)
	}
}

// An agent's first poll after a start may be held for the whole poll duration, and a heartbeat
// replaces the capabilities management holds. The first heartbeat must not wait for that poll,
// nor go out stripped of extensions the controller reads, which would refuse previews and
// releases until the next one. A controller that marks nothing still hears from the agent.
func TestFirstHeartbeatAfterAStartIsNegotiated(t *testing.T) {
	for _, marks := range []bool{true, false} {
		t.Run(map[bool]string{true: "marking", false: "unmarked"}[marks], func(t *testing.T) {
			j, _ := testVisualJournal(t)
			heartbeats := make(chan api.AgentCapabilities, 16)
			released := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if marks {
					w.Header().Set("Astilba-Agent-Features", "build-controls")
					w.Header().Set("Astilba-Agent-Previews", "v1")
					w.Header().Set("Astilba-Agent-Schedules", "v1")
				}
				switch r.URL.Path {
				case "/agent/v1/poll":
					if marks {
						// Held for as long as the agent runs.
						select {
						case <-r.Context().Done():
						case <-released:
						}
						return
					}
					time.Sleep(200 * time.Millisecond)
					_, _ = w.Write([]byte(`{"command":null}`))
				case "/agent/v1/heartbeat":
					var body api.AgentHeartbeat
					if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil {
						w.WriteHeader(400)
						return
					}
					select {
					case heartbeats <- body.Capabilities:
					default:
					}
					_, _ = w.Write([]byte(`{"role":"deployment","rotationRequested":false}`))
				case "/agent/v1/challenge":
					if !marks {
						// Controllers older than negotiation predate agent keys too.
						w.WriteHeader(404)
						return
					}
					_, _ = w.Write([]byte(`{"nonce":"n","expiresAt":"2030-01-01T00:00:00Z"}`))
				default:
					w.WriteHeader(204)
				}
			}))
			defer server.Close()
			defer close(released)
			cfg, err := j.Config()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Origin, cfg.Enrolled, cfg.Credential = server.URL, true, "agent-credential"
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			c := NewClient(j, &Engine{Journal: j})
			c.HTTP = server.Client()
			yes := true
			c.Observe = func(context.Context) api.AgentHeartbeat {
				return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Previews: &yes, Schedules: &yes}, Preflight: api.HostPreflight{Notes: []string{}}}
			}
			ctx, cancel := context.WithCancel(context.Background())
			stopped := make(chan error, 1)
			go func() { stopped <- c.Run(ctx) }()
			defer func() {
				cancel()
				<-stopped
			}()
			// Well inside the heartbeat interval, so an unmarked controller isn't kept waiting for it.
			select {
			case first := <-heartbeats:
				advertised := first.Previews != nil && first.Schedules != nil
				if advertised != marks {
					t.Fatalf("the first heartbeat advertised extensions %v to a controller that offers them %v", advertised, marks)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("no heartbeat while the first poll was held")
			}
		})
	}
}

// An enrollment replaces the capabilities management holds, so a challenge that fails on its way
// to management holds it back. A controller older than negotiation, which has no challenge and
// marks nothing, still enrolls the agent.
func TestEnrollmentWaitsForNegotiation(t *testing.T) {
	for _, challenge := range []int{503, 404} {
		t.Run(http.StatusText(challenge), func(t *testing.T) {
			j, _ := testVisualJournal(t)
			var enrollments atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/agent/v1/challenge":
					w.WriteHeader(challenge)
				case "/agent/v1/enroll":
					enrollments.Add(1)
					_, _ = w.Write([]byte(`{"role":"deployment","rotationRequested":false}`))
				default:
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			cfg, err := j.Config()
			if err != nil {
				t.Fatal(err)
			}
			cfg.Origin, cfg.MachineID = server.URL, uuid.New()
			if err = j.SetConfig(cfg); err != nil {
				t.Fatal(err)
			}
			c := NewClient(j, &Engine{Journal: j})
			c.HTTP = server.Client()
			c.Observe = func(context.Context) api.AgentHeartbeat { return api.AgentHeartbeat{} }
			err = c.Enroll(context.Background(), strings.Repeat("a", 43))
			if challenge == 404 && (err != nil || enrollments.Load() != 1) {
				t.Fatal("a controller older than negotiation could not enroll the agent", err)
			}
			if challenge == 503 && (!errors.Is(err, errNotNegotiated) || enrollments.Load() != 0) {
				t.Fatal("an enrollment went out before negotiation", err)
			}
		})
	}
}

// A controller replaced between heartbeats may read more than the one the agent last heard, and
// the heartbeat that learns it is followed at once by one that reports it.
func TestHeartbeatResendsWhenTheControllerChanges(t *testing.T) {
	j, _ := testVisualJournal(t)
	var upgraded atomic.Bool
	var heartbeats []bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if upgraded.Load() {
			w.Header().Set("Astilba-Agent-Features", "build-controls")
			w.Header().Set("Astilba-Agent-Build-Network", "v1")
		} else {
			w.Header().Set("Astilba-Agent-Features", "none")
		}
		if r.URL.Path != "/agent/v1/heartbeat" {
			w.WriteHeader(404)
			return
		}
		var body api.AgentHeartbeat
		if json.UnmarshalDecode(jsontext.NewDecoder(r.Body), &body) != nil {
			w.WriteHeader(400)
			return
		}
		heartbeats = append(heartbeats, body.Capabilities.BuildNetwork != nil)
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
	c.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Build: &yes, BuildNetwork: &yes}, Preflight: api.HostPreflight{Notes: []string{}}}
	}
	if err := c.heartbeat(context.Background(), "fixture", &api.AgentStatus{}); err != nil {
		t.Fatal(err)
	}
	upgraded.Store(true)
	if err := c.heartbeat(context.Background(), "fixture", &api.AgentStatus{}); err != nil {
		t.Fatal(err)
	}
	if len(heartbeats) != 3 || heartbeats[0] || heartbeats[1] || !heartbeats[2] {
		t.Fatal("heartbeats advertised build isolation as", heartbeats)
	}
}
