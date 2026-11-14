package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

// Offline recovery can keep a copy of a preview policy naming a host that not all its open previews
// run on: a request only the restored database holds open stays where the policy was before. Each
// preview's cleanup goes to the host its unit runs on and is authorised there. The policy's own
// approval is bound to the policy's host, so it cleans up only the previews running there.
func TestPreviewCleanupGoesToEachPreviewsHost(t *testing.T) {
	t.Run("pull requests closed", func(t *testing.T) {
		h, left, moved := previewsOnTwoHosts(t)
		h.provider.pullClosed.Store(true)
		h.pullRequestHookFor(t, 7)
		h.pullRequestHookFor(t, 8)
		waitPreviewRequest(t, h, left.id, func(r previewRequestState) bool { return r.closed })
		waitPreviewRequest(t, h, moved.id, func(r previewRequestState) bool { return r.closed })
		execSQL(t, h.hungBuild, "UPDATE preview_requests SET cleanup_at=now()-interval '1 second',next_check_at=now() WHERE id IN ($1,$2)", left.id, moved.id)
		h.pullRequestHookFor(t, 7)
		h.pullRequestHookFor(t, 8)

		// The policy's approval removes the preview on its own host.
		waitPreviewRequest(t, h, moved.id, func(r previewRequestState) bool { return r.state == "deleting" })
		moved.cleanUp(t, h)
		// The other preview waits for an owner to ask, which binds its cleanup to its own host.
		blocked := waitPreviewRequest(t, h, left.id, func(r previewRequestState) bool { return r.state == "blocked" })
		if !strings.Contains(blocked.note, "renewed owner approval") {
			t.Fatal("the preview on the policy's former host isn't waiting for an owner:", blocked.note)
		}
		var preview api.PullRequestPreview
		h.b.call("POST", h.path+"/previews/"+left.id.String()+"/actions", map[string]any{"action": "cleanup", "head": left.head, "headRepository": left.repository}, 200, &preview)
		left.cleanUp(t, h)
	})
	t.Run("template retired", func(t *testing.T) {
		h, left, moved := previewsOnTwoHosts(t)
		h.b.call("POST", h.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "PR template"}, 201, nil)
		left.cleanUp(t, h)
		moved.cleanUp(t, h)
	})
}

// hostedPreview is an allocated preview request and the host its unit runs on.
type hostedPreview struct {
	id, environment, host uuid.UUID
	token, head           string
	repository            int64
}

// previewsOnTwoHosts opens request 7's preview on the policy's host, then moves the policy to another
// host, as recovery keeping the checkpoint's copy of it would, and opens request 8's preview there.
// Request 8's build fails, so nothing more runs, and both keep their environments.
func previewsOnTwoHosts(t *testing.T) (*hungPreview, hostedPreview, hostedPreview) {
	t.Helper()
	h := newHungPreview(t)
	h.policy["maxActive"] = 2
	var policy api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &policy)
	first := readyPreview(t, h)
	left := hostedPreview{id: first.Id, environment: *first.EnvironmentId, host: h.server, token: h.token, head: first.Head, repository: first.HeadRepository}

	other, token, _ := enrollPreviewHost(t, h.hungBuild, "Other preview host")
	// A controller generation of its own, so a cleanup bound to the wrong host can't pass.
	execSQL(t, h.hungBuild, "UPDATE servers SET controller_generation=controller_generation+5 WHERE id=$1", other)
	execSQL(t, h.hungBuild, `UPDATE preview_policies p SET server_id=s.id,controller_generation=s.controller_generation,configuration=jsonb_set(p.configuration,'{build,runnerId}',to_jsonb(s.id::text))
		FROM servers s WHERE p.id=$1 AND s.id=$2`, policy.Id, other)

	h.provider.secondPull.Store(true)
	h.pullRequestHookFor(t, 8)
	var second uuid.UUID
	deadline := time.Now().Add(20 * time.Second)
	for {
		err := h.f.st.Pool.QueryRow(t.Context(), "SELECT id FROM preview_requests WHERE policy_id=$1 AND number=8 AND state='approval_required'", policy.Id).Scan(&second)
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("request 8 never asked for approval", err)
		}
		time.Sleep(30 * time.Millisecond)
	}
	h.b.call("POST", h.path+"/previews/"+second.String()+"/actions", map[string]any{"action": "approve", "head": first.Head, "headRepository": first.HeadRepository}, 200, nil)
	build := nextHostCommand(t, h, token)
	if build.Pipeline == nil {
		t.Fatal("request 8's first command isn't its build")
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: build.Id, Generation: build.Generation, PlanHash: build.PlanHash, Sequence: 2, State: "failed", Steps: []api.ExecutionStep{}}, 204, nil)
	h.pullRequestHookFor(t, 8)
	r := waitPreviewRequest(t, h, second, func(r previewRequestState) bool { return r.state == "failed" })
	if r.environment == nil {
		t.Fatal("request 8 has no preview environment")
	}
	var placed uuid.UUID
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT server_id FROM deployment_units WHERE environment_id=$1 AND primary_unit", *r.environment).Scan(&placed); err != nil || placed != other {
		t.Fatal("request 8's preview isn't on the policy's new host", placed, err)
	}
	moved := hostedPreview{id: second, environment: *r.environment, host: other, token: token, head: first.Head, repository: first.HeadRepository}
	return h, left, moved
}

// cleanUp takes the preview's cleanup from its own host, confirms it, and waits for the request to
// be deleted.
func (p hostedPreview) cleanUp(t *testing.T, h *hungPreview) {
	t.Helper()
	cleanup := nextHostCommand(t, h, p.token)
	var payload deployment.Payload
	if cleanup.Visual == nil || json.Unmarshal(*cleanup.Visual, &payload) != nil || payload.Action != "preview-cleanup" || cleanup.EnvironmentId != p.environment || cleanup.ServerId != p.host {
		t.Fatalf("expected the preview's cleanup on its host, got %+v", cleanup)
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", p.token, api.AgentReport{Id: cleanup.Id, Generation: cleanup.Generation, PlanHash: cleanup.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	waitPreviewRequest(t, h, p.id, func(r previewRequestState) bool { return r.state == "deleted" })
}

// nextHostCommand polls as the agent holding token until it's given a command.
func nextHostCommand(t *testing.T, h *hungPreview, token string) api.ExecutionCommand {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var p api.AgentPoll
		agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command != nil {
			return *p.Command
		}
		if time.Now().After(deadline) {
			t.Fatal("the host was given no command")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

type previewRequestState struct {
	state, note string
	closed      bool
	environment *uuid.UUID
}

// waitPreviewRequest waits until the request's row satisfies done, and returns it.
func waitPreviewRequest(t *testing.T, h *hungPreview, id uuid.UUID, done func(previewRequestState) bool) previewRequestState {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var r previewRequestState
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT state,note,closed_at IS NOT NULL,environment_id FROM preview_requests WHERE id=$1", id).Scan(&r.state, &r.note, &r.closed, &r.environment); err != nil {
			t.Fatal(err)
		}
		if done(r) {
			return r
		}
		if time.Now().After(deadline) {
			t.Fatalf("the preview request never got there: %+v", r)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// A host's preview slots are the previews running on it, wherever their policies point. Ten previews
// run on the policy's host under another policy that names a different host, as recovery can leave
// them, so a new preview there waits for a slot rather than taking an eleventh.
func TestPreviewSlotsCountThePreviewsRunningOnAHost(t *testing.T) {
	h := newHungPreview(t)
	other, _, _ := enrollPreviewHost(t, h.hungBuild, "Other preview host")
	running := seedPreviews(t, h.f, h.b, h.ws, other)
	for number := 9; number < 17; number++ {
		openPreview(t, h.f, h.b, h.ws, running, number)
	}
	execSQL(t, h.hungBuild, `UPDATE deployment_units SET server_id=$1 WHERE environment_id IN (SELECT environment_id FROM preview_requests WHERE policy_id=$2)`, h.server, running.policy)
	// Nothing checks them again during the test.
	execSQL(t, h.hungBuild, `UPDATE preview_requests SET next_check_at=now()+interval '1 day' WHERE policy_id=$1`, running.policy)

	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	waiting := waitPreviewAt(t, h, h.path, "approval_required")
	h.b.call("POST", h.path+"/previews/"+waiting.Id.String()+"/actions", map[string]any{"action": "approve", "head": waiting.Head, "headRepository": waiting.HeadRepository}, 200, nil)
	if queued := waitPreviewAt(t, h, h.path, "queued"); queued.EnvironmentId != nil || !strings.Contains(queued.Note, "slot") {
		t.Fatal("a preview took an eleventh slot on its host:", queued.State, queued.Note)
	}

	// Once one of them ends, the new preview takes its slot.
	execSQL(t, h.hungBuild, `UPDATE preview_requests SET state='deleted' WHERE id=$1`, running.requests[0])
	h.pullRequestHook(t)
	build := nextPreviewCommand(t, h)
	if build.Pipeline == nil || build.ServerId != h.server {
		t.Fatalf("the preview didn't start on its policy's host once a slot was free: %+v", build)
	}
}
