package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Operator writes ask the provider between passes too, and their outcome follows who the
// caller is after the call. A provider failure keeps its response, and audits nothing.
func TestBuildAdmissionRechecksAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name       string
		during     func(t *testing.T, h *hungBuild)
		status     int
		operations int
	}{
		{name: "nothing changed", during: func(*testing.T, *hungBuild) {}, status: 202, operations: 1},
		{
			name: "caller lost write access",
			during: func(t *testing.T, h *hungBuild) {
				execSQL(t, h, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
			},
			status: 403,
		},
		{
			name:   "provider failed",
			during: func(t *testing.T, h *hungBuild) { h.provider.failReleased() },
			status: 400,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			entered, release := h.provider.hold(t, "/branches/main")
			finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": h.config})
			waitEntered(t, entered)
			h.requireNothingHeld(t, uuid.Nil)
			tc.during(t, h)
			release()
			body := requireReply(t, <-finished, tc.status)
			if tc.name == "provider failed" && !bytes.Contains(body, []byte("branch")) {
				t.Fatal("a provider failure changed its response", string(body))
			}
			var operations, audits int
			if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM execution_operations WHERE workspace_id=$1),(SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='build.create')", h.ws).Scan(&operations, &audits); err != nil {
				t.Fatal(err)
			}
			if operations != tc.operations || audits != tc.operations {
				t.Fatal("admission outcome", operations, audits, "want", tc.operations)
			}
		})
	}
}

// Without the lock across the call, retries of one request all reach the provider. The key
// read under the lock still admits it once.
func TestConcurrentBuildRetriesAdmitOnceAfterAHungProvider(t *testing.T) {
	h := newHungBuild(t)
	entered, release := h.provider.hold(t, "/branches/main")
	key := uuid.New()
	body := map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": key, "configuration": h.config}
	results := make([]<-chan reply, 4)
	for i := range results {
		results[i] = h.b.send("POST", h.path+"/builds", body)
	}
	for range results {
		waitEntered(t, entered)
	}
	release()
	ids := map[uuid.UUID]bool{}
	for _, result := range results {
		var op api.Operation
		if err := json.Unmarshal(requireReply(t, <-result, 202), &op); err != nil {
			t.Fatal(err)
		}
		ids[op.Id] = true
	}
	var operations int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM execution_operations WHERE request_key=$1", key).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 || operations != 1 {
		t.Fatal("concurrent retries were admitted more than once", ids, operations)
	}
}

func TestConnectionCreationHoldsNothingDuringItsCheck(t *testing.T) {
	h := newHungBuild(t)
	var before int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM git_connections WHERE workspace_id=$1", h.ws).Scan(&before); err != nil {
		t.Fatal(err)
	}
	entered, release := h.provider.hold(t, "/repos/team/app")
	finished := h.b.send("POST", h.prefix+"/git-connections", map[string]any{"name": "Second", "provider": "gitea", "baseUrl": h.provider.server.URL, "repository": "team/app"})
	waitEntered(t, entered)
	h.requireNothingHeld(t, uuid.Nil)
	execSQL(t, h, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
	release()
	requireReply(t, <-finished, 403)
	var after int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM git_connections WHERE workspace_id=$1", h.ws).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Fatal("a connection was created for a caller who lost access during the check")
	}
}

func (h *hungBuild) push(t *testing.T, branch, commit string) {
	t.Helper()
	raw, _ := stablejson.Marshal(map[string]any{"ref": "refs/heads/" + branch, "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(raw)
	request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gitea-Event", "push")
	request.Header.Set("X-Gitea-Delivery", uuid.NewString())
	request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := h.f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("push rejected", response.StatusCode)
	}
}

// A push handled during an admission's provider call used to wait for the workspace, and so
// always saw what the admission committed. Now its refresh can finish first, finding no
// target yet, and nothing would look at the branch again. The delivery makes the admission
// ask again instead, so it commits the commit it was told about.
func TestPushDuringBuildAdmissionIsNotLost(t *testing.T) {
	h := newHungBuild(t)
	config := h.config
	config.AutoBuild = true
	entered, release := h.provider.holdOnce(t, "/branches/main")
	finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": config})
	waitEntered(t, entered)
	newer := strings.Repeat("c", 40)
	h.provider.head.Store(newer)
	// No target tracks the branch until the admission commits, so the push starts no refresh;
	// the admission must notice it.
	h.push(t, "main", newer)
	var refreshes int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_git_refresh_v1'").Scan(&refreshes); err != nil || refreshes != 0 {
		t.Fatal("a push for an untracked branch started a refresh", refreshes, err)
	}
	release()
	requireReply(t, <-finished, 202)
	var requested, automatic string
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT requested_commit,automatic_requested_commit FROM build_targets WHERE workspace_id=$1", h.ws).Scan(&requested, &automatic); err != nil {
		t.Fatal(err)
	}
	if requested != newer || automatic != newer {
		t.Fatal("the admission recorded a commit older than the push", requested, automatic)
	}
}

// A busy repository must not keep an admission asking about a branch nobody pushed to.
func TestPushToAnotherBranchKeepsTheAdmissionsAnswer(t *testing.T) {
	h := newHungBuild(t)
	entered, release := h.provider.holdOnce(t, "/branches/main")
	finished := h.b.send("POST", h.path+"/builds", map[string]any{"baseRevisionId": h.draft.Id, "applicationId": h.app, "requestKey": uuid.New(), "configuration": h.config})
	waitEntered(t, entered)
	h.push(t, "other", strings.Repeat("c", 40))
	release()
	requireReply(t, <-finished, 202)
	if asked := h.provider.requests("/branches/main"); asked != 1 {
		t.Fatal("a push to another branch discarded the answer", asked)
	}
}

func (h *hungBuild) pullRequestHook(t *testing.T) {
	t.Helper()
	raw := []byte(`{"number":7,"repository":{"id":12}}`)
	mac := hmac.New(sha256.New, []byte(h.secret))
	mac.Write(raw)
	request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Gitea-Event", "pull_request")
	request.Header.Set("X-Gitea-Delivery", uuid.NewString())
	request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := h.f.server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("hook rejected", response.StatusCode)
	}
}

func (h *hungBuild) settleFirstBuild(t *testing.T, config pipeline.BuildConfig) *api.ExecutionCommand {
	t.Helper()
	first := h.deliver(t, config)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: first.Id, Generation: first.Generation, PlanHash: first.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	return first
}

// A refresh's worker used to hold the workspace queue and the connection row across its call,
// so a tenant's own server could stall that workspace's writers, and every signed webhook
// for the connection pinned a pool connection behind it. The draft it admits a build for
// must be the one current after the provider answered, not the one it read before asking.
func TestRefreshWorkerRechecksAfterAHungProvider(t *testing.T) {
	h := newHungBuild(t)
	config := h.config
	config.AutoBuild = true
	first := h.settleFirstBuild(t, config)
	commit := strings.Repeat("c", 40)
	h.provider.head.Store(commit)
	entered, release := h.provider.hold(t, "/branches/main")
	h.push(t, "main", commit)
	waitEntered(t, entered)
	h.requireNothingHeld(t, first.Id)
	// A webhook for this connection waits on the row the refresh used to hold.
	h.push(t, "unrelated", commit)
	h.spec.Applications[0].Name = "edited during the call"
	var edited api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": h.spec}, 200, &edited)
	release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command, err := h.f.execution.Poll(ctx, h.identity)
	if err != nil || command == nil || command.Id == first.Id {
		t.Fatal("refresh admitted nothing", command, err)
	}
	var payload pipeline.Command
	if err = json.Unmarshal(*command.Pipeline, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.RevisionID != edited.Id || payload.Commit != commit {
		t.Fatal("refresh admitted against the draft it read before asking", payload.RevisionID, edited.Id, payload.Commit)
	}
}

// A second push while a refresh waits on the provider is folded into that running job, so
// the job has to look at the branch again rather than finish with what it was told first.
func TestRefreshWorkerAsksAgainAfterAPushDuringItsCheck(t *testing.T) {
	h := newHungBuild(t)
	config := h.config
	config.AutoBuild = true
	first := h.settleFirstBuild(t, config)
	older, newer := strings.Repeat("b", 40), strings.Repeat("c", 40)
	h.provider.head.Store(older)
	entered, release := h.provider.holdOnce(t, "/branches/main")
	h.push(t, "main", older)
	waitEntered(t, entered)
	h.provider.head.Store(newer)
	h.push(t, "main", newer)
	release()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	command, err := h.f.execution.Poll(ctx, h.identity)
	if err != nil || command == nil || command.Id == first.Id {
		t.Fatal("refresh admitted nothing", command, err)
	}
	var payload pipeline.Command
	if err = json.Unmarshal(*command.Pipeline, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Commit != newer {
		t.Fatal("refresh admitted the commit from before the second push", payload.Commit)
	}
}

// The automatic deployment worker checks the branch once more before planning. Its owner and
// draft must be the ones after that call.
func TestAutomaticWorkerRechecksAfterAHungProvider(t *testing.T) {
	for _, tc := range []struct {
		name   string
		during func(t *testing.T, h *hungBuild)
		note   string
	}{
		{
			name: "owner demoted",
			during: func(t *testing.T, h *hungBuild) {
				execSQL(t, h, "UPDATE memberships SET role='viewer' WHERE workspace_id=$1", h.ws)
			},
			note: "owner",
		},
		{
			name: "draft replaced",
			during: func(t *testing.T, h *hungBuild) {
				var current uuid.UUID
				if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT draft_revision_id FROM environments WHERE workspace_id=$1", h.ws).Scan(&current); err != nil {
					t.Fatal(err)
				}
				h.spec.Applications[0].Name = "edited"
				h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": current, "specification": h.spec}, 200, nil)
			},
			note: "draft",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHungBuild(t)
			config := h.config
			config.AutoBuild, config.AutoDeploy = true, true
			c := h.deliver(t, config)
			execSQL(t, h, "UPDATE environments SET applied_revision_id=draft_revision_id WHERE id=$1", c.EnvironmentId)
			execSQL(t, h, "UPDATE deployment_units SET server_id=$2 WHERE environment_id=$1", c.EnvironmentId, h.server)
			// The report's own branch check passes; the worker's is the one that hangs.
			entered, release := h.provider.holdAfter(t, "/branches/main", 1)
			if err := h.f.execution.Report(t.Context(), h.identity, h.succeeded(t, c)); err != nil {
				t.Fatal(err)
			}
			waitEntered(t, entered)
			h.requireNothingHeld(t, c.Id)
			tc.during(t, h)
			release()
			deadline := time.Now().Add(20 * time.Second)
			for {
				var state, note string
				if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note FROM pipeline_auto_deployments WHERE id=$1", c.Id).Scan(&state, &note); err != nil {
					t.Fatal(err)
				}
				if state == "blocked" {
					if !strings.Contains(note, tc.note) {
						t.Fatal("stopped for another reason:", note)
					}
					return
				}
				if time.Now().After(deadline) {
					t.Fatal("the worker never stopped", state, note)
				}
				time.Sleep(30 * time.Millisecond)
			}
		})
	}
}
