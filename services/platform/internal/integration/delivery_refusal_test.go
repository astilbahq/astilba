package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// A build refused at delivery for a reason no later poll changes would fail every poll by its
// runner until the build's deadline, and the poll serves the runner's oldest command first, so
// the runner's later commands, its update included, would wait behind it. A build never delivered
// fails instead, and one the runner may hold is told to stop, without its values. Either way the
// runner's next command reaches it.
func TestBuildRefusedAtDeliveryReleasesTheRunner(t *testing.T) {
	for _, tc := range []struct {
		name, failed, stopped string
		refuse, restore       func(h *hungBuild, capabilities *api.AgentCapabilities)
	}{
		{
			name:    "runner lost build controls",
			failed:  "Build stopped before runner delivery. Update the agent on the runner: it no longer applies build controls",
			stopped: "Build stopped. Update the agent on the runner: it no longer applies build controls",
			refuse:  func(_ *hungBuild, c *api.AgentCapabilities) { c.BuildControls = nil },
			restore: func(_ *hungBuild, c *api.AgentCapabilities) { yes := true; c.BuildControls = &yes },
		},
		{
			name:    "runner lost the project cache",
			failed:  "Build stopped before runner delivery: its runner no longer supports the project build cache",
			stopped: "Build stopped: its runner no longer supports the project build cache",
			refuse:  func(_ *hungBuild, c *api.AgentCapabilities) { c.BuildCache = nil },
			restore: func(_ *hungBuild, c *api.AgentCapabilities) { yes := true; c.BuildCache = &yes },
		},
		{
			name:    "connection moved to another repository",
			failed:  "Refused before runner delivery: the connection's repository is no longer the one this pipeline was queued for.",
			stopped: "Stopped: the connection's repository is no longer the one this pipeline was queued for.",
			refuse:  func(h *hungBuild, _ *api.AgentCapabilities) { h.provider.repository.Store(13) },
			restore: func(h *hungBuild, _ *api.AgentCapabilities) { h.provider.repository.Store(12) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			capabilities := h.enrolled.Capabilities
			yes := true
			capabilities.BuildControls, capabilities.BuildCache = &yes, &yes
			heartbeat := func() {
				t.Helper()
				agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: capabilities, Preflight: h.enrolled.Preflight}, 200, nil)
			}
			poll := func() *api.ExecutionCommand {
				t.Helper()
				heartbeat()
				var out api.AgentPoll
				agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &out)
				return out.Command
			}
			next := func() api.ExecutionCommand {
				t.Helper()
				deadline := time.Now().Add(15 * time.Second)
				for {
					if c := poll(); c != nil {
						return *c
					}
					if time.Now().After(deadline) {
						t.Fatal("the runner was given no command")
					}
					time.Sleep(30 * time.Millisecond)
				}
			}
			operation := func(id uuid.UUID) (state, note string, cancelled bool) {
				t.Helper()
				if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note,cancel_requested FROM execution_operations WHERE id=$1", id).Scan(&state, &note, &cancelled); err != nil {
					t.Fatal(err)
				}
				return state, note, cancelled
			}
			heartbeat()
			config := h.config
			config.Budget = &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}

			// Never delivered: it fails, and nothing reaches the runner.
			queued := h.createBuild(t, config)
			tc.refuse(h, &capabilities)
			if c := poll(); c != nil {
				t.Fatal("a refused build reached the runner", c.Id)
			}
			if state, note, _ := operation(queued.Id); state != "failed" || !strings.Contains(note, tc.failed) {
				t.Fatalf("the undelivered build ended %s with the note %q", state, note)
			}
			tc.restore(h, &capabilities)
			heartbeat()

			// Possibly held: once the runner asks for the build's values again, it is told to stop.
			held := h.createBuild(t, config)
			delivered := next()
			var frozen pipeline.Command
			if delivered.Id != held.Id || delivered.CancelRequested || delivered.SecretRelease == nil || !delivered.SecretRelease.Values || json.Unmarshal(*delivered.Pipeline, &frozen) != nil {
				t.Fatal("the build was not delivered with its values", delivered.Id)
			}
			if frozen.CacheScope == "" {
				t.Fatal("the build was queued without the project cache")
			}
			tc.refuse(h, &capabilities)
			agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/releases", h.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: held.Id, Epoch: delivered.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
			stop := next()
			var command pipeline.Command
			if stop.Id != held.Id || !stop.CancelRequested || stop.SecretRelease != nil || json.Unmarshal(*stop.Pipeline, &command) != nil || command.Access != nil || command.BuildSecrets != nil {
				t.Fatal("the possibly delivered build was not stopped without its values", stop.Id, stop.CancelRequested, stop.SecretRelease)
			}
			// It ends when the runner reports it, since the runner may be running it.
			if state, note, cancelled := operation(held.Id); state != "dispatched" || !cancelled || !strings.HasPrefix(note, tc.stopped) {
				t.Fatalf("the stopped build is %s, cancel requested %v, with the note %q", state, cancelled, note)
			}
			agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: stop.Id, Generation: stop.Generation, PlanHash: stop.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
			if state, _, _ := operation(held.Id); state != "cancelled" {
				t.Fatal("the stopped build did not end cancelled", state)
			}

			// The runner's next command reaches it.
			tc.restore(h, &capabilities)
			heartbeat()
			later := h.createBuild(t, config)
			if c := next(); c.Id != later.Id || c.CancelRequested {
				t.Fatal("a refused build held the runner's later commands", c.Id)
			}
		})
	}
}
