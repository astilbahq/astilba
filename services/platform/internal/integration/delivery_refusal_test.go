package integration

import (
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// refusalRunner is a build runner that offers build controls and the project cache, and
// heartbeats with capabilities before each poll.
type refusalRunner struct {
	*hungBuild
	capabilities api.AgentCapabilities
}

func newRefusalRunner(t *testing.T) *refusalRunner {
	t.Helper()
	r := &refusalRunner{hungBuild: newHungBuild(t)}
	r.capabilities = r.enrolled.Capabilities
	yes := true
	r.capabilities.BuildControls, r.capabilities.BuildCache = &yes, &yes
	r.heartbeat(t)
	return r
}

func (r *refusalRunner) heartbeat(t *testing.T) {
	t.Helper()
	agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/heartbeat", r.token, api.AgentHeartbeat{Capabilities: r.capabilities, Preflight: r.enrolled.Preflight}, 200, nil)
}

// poll heartbeats, then polls once, expecting status.
func (r *refusalRunner) poll(t *testing.T, status int) *api.ExecutionCommand {
	t.Helper()
	r.heartbeat(t)
	var out api.AgentPoll
	agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/poll", r.token, nil, status, &out)
	return out.Command
}

func (r *refusalRunner) next(t *testing.T) api.ExecutionCommand {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if c := r.poll(t, 200); c != nil {
			return *c
		}
		if time.Now().After(deadline) {
			t.Fatal("the runner was given no command")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// deliverWithValues takes a queued build's first delivery, which carries its values.
func (r *refusalRunner) deliverWithValues(t *testing.T, id uuid.UUID) api.ExecutionCommand {
	t.Helper()
	delivered := r.next(t)
	var frozen pipeline.Command
	if delivered.Id != id || delivered.CancelRequested || delivered.SecretRelease == nil || !delivered.SecretRelease.Values || json.Unmarshal(*delivered.Pipeline, &frozen) != nil {
		t.Fatal("the build was not delivered with its values", delivered.Id)
	}
	if frozen.CacheScope == "" {
		t.Fatal("the build was queued without the project cache")
	}
	return delivered
}

func (r *refusalRunner) operation(t *testing.T, id uuid.UUID) (state, note string, cancelled bool) {
	t.Helper()
	if err := r.f.st.Pool.QueryRow(t.Context(), "SELECT state,note,cancel_requested FROM execution_operations WHERE id=$1", id).Scan(&state, &note, &cancelled); err != nil {
		t.Fatal(err)
	}
	return state, note, cancelled
}

// refuseRepository makes the runner's Git provider refuse the repository's own API path, which
// delivery asks before anything is sent.
func refuseRepository(status int, header map[string]string, body string) func(*refusalRunner) {
	return func(r *refusalRunner) {
		r.provider.refusing.Store(&providerRefusal{match: "/repos/team/app", status: status, header: header, body: body})
	}
}

func (r *refusalRunner) report(t *testing.T, c api.ExecutionCommand, state api.ExecutionState) {
	t.Helper()
	agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/report", r.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: state, Steps: []api.ExecutionStep{}}, 204, nil)
}

// A build refused at delivery for a reason no later poll changes would fail every poll by its
// runner until the build's deadline, and the poll serves the runner's oldest command first, so
// the runner's later commands, its update included, would wait behind it. A build never delivered
// fails instead, and one the runner may hold is told to stop, without its values. Either way the
// poll answers, so the runner is not held behind the build.
func TestBuildRefusedAtDeliveryReleasesTheRunner(t *testing.T) {
	for _, tc := range []struct {
		name, failed, stopped string
		refuse, restore       func(r *refusalRunner)
	}{
		{
			name:    "runner lost build controls",
			failed:  "Build stopped before runner delivery. Update the agent on the runner: it no longer applies build controls",
			stopped: "Build stopped. Update the agent on the runner: it no longer applies build controls",
			refuse:  func(r *refusalRunner) { r.capabilities.BuildControls = nil },
			restore: func(r *refusalRunner) { yes := true; r.capabilities.BuildControls = &yes },
		},
		{
			// An agent that has not negotiated the cache advertises none, while it can still build.
			name:    "runner lost the project cache",
			failed:  "Build stopped before runner delivery: its runner no longer supports the project build cache",
			stopped: "Build stopped: its runner no longer supports the project build cache",
			refuse:  func(r *refusalRunner) { r.capabilities.BuildCache = nil },
			restore: func(r *refusalRunner) { yes := true; r.capabilities.BuildCache = &yes },
		},
		{
			name:    "connection moved to another repository",
			failed:  "Refused before runner delivery: the connection's repository is no longer the one this pipeline was queued for.",
			stopped: "Stopped: the connection's repository is no longer the one this pipeline was queued for.",
			refuse:  func(r *refusalRunner) { r.provider.repository.Store(13) },
			restore: func(r *refusalRunner) { r.provider.repository.Store(12) },
		},
		{
			name:    "repository deleted",
			failed:  "Refused before runner delivery: the Git provider has no repository by that name that the connection can see (HTTP 404)",
			stopped: "Stopped: the Git provider has no repository by that name that the connection can see (HTTP 404)",
			refuse:  refuseRepository(http.StatusNotFound, nil, `{"message":"Not Found"}`),
			restore: func(r *refusalRunner) { r.provider.refusing.Store(nil) },
		},
		{
			name:    "repository access refused",
			failed:  "Refused before runner delivery: the Git provider refused the connection access to the repository (HTTP 403)",
			stopped: "Stopped: the Git provider refused the connection access to the repository (HTTP 403)",
			refuse:  refuseRepository(http.StatusForbidden, nil, `{"message":"user does not have permission"}`),
			restore: func(r *refusalRunner) { r.provider.refusing.Store(nil) },
		},
		{
			// Gitea refuses the push check with a bare "Forbidden" whatever the reason, so its API's
			// own refusal of a write request is what shows the token can't write. Anything in front
			// of the API, as an IP allow list, refuses that request the same way for a token that
			// can push.
			name:    "push check unanswered",
			failed:  "Refused before runner delivery: The Git server refused to say whether the connection's clone credential can push",
			stopped: "Stopped: The Git server refused to say whether the connection's clone credential can push",
			refuse: func(r *refusalRunner) {
				r.provider.refusing.Store(&providerRefusal{match: "/branches/" + pushCheckBranch, status: http.StatusForbidden, body: "Forbidden\n"})
			},
			restore: func(r *refusalRunner) { r.provider.refusing.Store(nil) },
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRefusalRunner(t)
			config := r.config
			config.Budget = &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}

			// Never delivered: it fails, and nothing reaches the runner.
			queued := r.createBuild(t, config)
			tc.refuse(r)
			if c := r.poll(t, 200); c != nil {
				t.Fatal("a refused build reached the runner", c.Id)
			}
			if state, note, _ := r.operation(t, queued.Id); state != "failed" || !strings.Contains(note, tc.failed) {
				t.Fatalf("the undelivered build ended %s with the note %q", state, note)
			}
			tc.restore(r)
			r.heartbeat(t)

			// Possibly held: once the runner asks for the build's values again, it is told to stop.
			delivered := r.deliverWithValues(t, r.createBuild(t, config).Id)
			tc.refuse(r)
			agentRequest(t, r.f.server.Client(), r.f.server.URL, "/agent/v1/releases", r.token, api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: delivered.Id, Epoch: delivered.SecretRelease.Epoch, Reason: api.JournalMissing}}}, 204, nil)
			stop := r.next(t)
			var command pipeline.Command
			if stop.Id != delivered.Id || !stop.CancelRequested || stop.SecretRelease != nil || json.Unmarshal(*stop.Pipeline, &command) != nil || command.Access != nil || command.BuildSecrets != nil {
				t.Fatal("the possibly delivered build was not stopped without its values", stop.Id, stop.CancelRequested, stop.SecretRelease)
			}
			// It ends when the runner reports it, since the runner may be running it.
			if state, note, cancelled := r.operation(t, delivered.Id); state != "dispatched" || !cancelled || !strings.HasPrefix(note, tc.stopped) {
				t.Fatalf("the stopped build is %s, cancel requested %v, with the note %q", state, cancelled, note)
			}
			r.report(t, stop, "cancelled")
			if state, _, _ := r.operation(t, delivered.Id); state != "cancelled" {
				t.Fatal("the stopped build did not end cancelled", state)
			}
		})
	}
}

// An agent offers builds, and their cache, only while its host can run one: while Docker is down
// or short of room it offers neither. That passes by itself, so a build for it waits, queued or
// running, and is neither failed nor stopped. A build with no budget still has a cache scope.
func TestBuildWaitsForARunnerBrieflyUnableToBuild(t *testing.T) {
	r := newRefusalRunner(t)
	able := r.capabilities
	unable := able
	no := false
	unable.Build, unable.RootlessBuilds, unable.BuildControls, unable.BuildCache = &no, &no, &no, &no
	unable.RailpackVersion, unable.RailpackInputs = nil, nil
	waits := func(id uuid.UUID) {
		t.Helper()
		r.capabilities = unable
		r.poll(t, 400)
		if state, note, cancelled := r.operation(t, id); state != "dispatched" || cancelled || note != "" {
			t.Fatalf("a build for a runner briefly unable to build is %s, cancel requested %v, with the note %q", state, cancelled, note)
		}
		r.capabilities = able
	}
	queued := r.createBuild(t, r.config)
	waits(queued.Id)
	delivered := r.deliverWithValues(t, queued.Id)
	waits(delivered.Id)
	r.report(t, delivered, "cancelled")
}
