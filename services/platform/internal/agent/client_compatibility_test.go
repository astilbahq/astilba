package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
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
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					Capabilities map[string]json.RawMessage `json:"capabilities"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
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
				return api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Build: &capable, BuildControls: &capable, BuildCache: &capable, RailpackVersion: &version, SafeReleases: &capable, ServiceOutputs: &capable, Schedules: &capable}}
			}
			if err = c.Enroll(context.Background(), strings.Repeat("a", 43)); err != nil {
				t.Fatal("older controller enrollment", err)
			}
			heartbeat := func() error {
				return c.call(context.Background(), "POST", "/agent/v1/heartbeat", c.Observe(context.Background()), &api.AgentStatus{}, "fixture")
			}
			if err = heartbeat(); err != nil || extensions.Load() != 0 || cacheExtensions.Load() != 0 || railpackExtensions.Load() != 0 || releaseExtensions.Load() != 0 || outputExtensions.Load() != 0 || scheduleExtensions.Load() != 0 {
				t.Fatal("older controller received capability extension", err)
			}
			modern.Store(true)
			if err = heartbeat(); err != nil || extensions.Load() != 0 || cacheExtensions.Load() != 0 || railpackExtensions.Load() != 0 || releaseExtensions.Load() != 0 || outputExtensions.Load() != 0 || scheduleExtensions.Load() != 0 {
				t.Fatal("advertised before negotiation", err)
			}
			if err = heartbeat(); err != nil || extensions.Load() != 1 || cacheExtensions.Load() != 1 || railpackExtensions.Load() != 1 || releaseExtensions.Load() != 1 || outputExtensions.Load() != 1 || scheduleExtensions.Load() != 1 {
				t.Fatal("supported extension not advertised", err)
			}
			modern.Store(false)
			if heartbeat() == nil {
				t.Fatal("old request schema fixture did not reject unsupported field")
			}
			if err = heartbeat(); err != nil || releaseExtensions.Load() != 2 || outputExtensions.Load() != 1 || scheduleExtensions.Load() != 1 {
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
		if json.NewDecoder(r.Body).Decode(&body) != nil || body.Capabilities.BuildCache != nil {
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
