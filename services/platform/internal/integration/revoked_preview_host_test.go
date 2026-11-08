package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A preview policy is bound to its reserved host, and nothing a revoked host says is accepted. Its
// previews could never be confirmed removed, so they stayed blocked, held their resource slots for
// good and stopped the owner moving the policy to another host.
func TestRevokedPreviewHostReleasesItsPreviews(t *testing.T) {
	h := newHungPreview(t)
	ctx := t.Context()
	const canary = "revoked-preview-secret-canary"
	var secret api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "TOKEN", "value": canary}, 201, &secret)
	spec := h.spec
	spec.Variables = []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}
	var draft api.Revision
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": h.draft.Id, "specification": spec}, 200, &draft)
	h.policy["baseRevisionId"] = draft.Id

	// Another template's preview on its own healthy host.
	healthyHost, _, _ := enrollPreviewHost(t, h.hungBuild, "Healthy preview host")
	healthyPath := healthyTemplate(t, h, healthyHost)
	healthy := approvePreview(t, h, healthyPath)
	h.waitState(t, *healthy.OperationId, "dispatched")

	var policy api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &policy)
	preview := readyPreview(t, h)
	released := *preview.EnvironmentId

	h.b.call("POST", h.prefix+"/servers/"+h.server.String()+"/revoke", nil, 204, nil)

	preview = previewAt(t, h, h.path)
	if preview.State != "deleted" || !strings.Contains(preview.Note, "revoked") || !strings.Contains(preview.Note, "can't confirm removal") || !strings.Contains(preview.Note, "rotate those secrets") {
		t.Fatal("the revoked host's preview kept its slot, or its note isn't truthful:", preview.State, preview.Note)
	}
	var held, secrets int
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM preview_requests WHERE workspace_id=$1 AND environment_id IS NOT NULL AND state<>'deleted'", h.ws).Scan(&held); err != nil || held != 1 {
		t.Fatal("slots still held", held, "want only the healthy host's", err)
	}
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2", h.ws, released).Scan(&secrets); err != nil || secrets != 0 {
		t.Fatal("management kept the released preview's secrets", secrets, err)
	}

	// The healthy host's preview is untouched.
	after := previewAt(t, h, healthyPath)
	if after.State != healthy.State || after.Note != healthy.Note || *after.EnvironmentId != *healthy.EnvironmentId || *after.OperationId != *healthy.OperationId {
		t.Fatal("revoking another host changed a healthy preview", healthy, after)
	}
	var state string
	var cancelled bool
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT state,cancel_requested FROM execution_operations WHERE id=$1", *healthy.OperationId).Scan(&state, &cancelled); err != nil || state != "dispatched" || cancelled {
		t.Fatal("revoking another host stopped a healthy preview's build", state, cancelled, err)
	}

	// A provider event while the policy still names the revoked host keeps the note.
	previewEvent(t, h, policy.Id)
	if after := previewAt(t, h, h.path); after.State != "deleted" || after.Note != preview.Note {
		t.Fatal("a provider event replaced the released preview's note", after.State, after.Note)
	}

	// The policy can move to another host, where the request gets a new preview.
	replacement, _, _ := enrollPreviewHost(t, h.hungBuild, "Replacement preview host")
	build := h.policy["build"].(pipeline.BuildConfig)
	build.RunnerID = replacement
	h.policy["build"] = build
	// The first check after the move can't reach the provider, which leaves the request released.
	entered, release := h.provider.hold(t, "/pulls/7")
	h.provider.failReleased()
	marker := previewJobMarker(t, h)
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": h.policy}, 200, nil)
	waitEntered(t, entered)
	release()
	waitPreviewJob(t, h, policy.Id, marker)
	if after := previewAt(t, h, h.path); after.State != "deleted" || after.Note != preview.Note {
		t.Fatal("a provider error took back the released preview's slot", after.State, after.Note)
	}
	previewEvent(t, h, policy.Id)
	moved := approvePreview(t, h, h.path)
	var server uuid.UUID
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT server_id FROM execution_operations WHERE id=$1", *moved.OperationId).Scan(&server); err != nil || server != replacement || *moved.EnvironmentId == released {
		t.Fatal("the moved policy's preview didn't start afresh on the replacement host", server, err)
	}
	if !strings.Contains(moved.Note, "new preview") || !strings.Contains(moved.Note, "revoked") {
		t.Fatal("the recreated preview reads as if nothing happened:", moved.Note)
	}
}

// previewEvent sends a pull-request webhook and waits for the policy's job it starts to finish.
func previewEvent(t *testing.T, h *hungPreview, policy uuid.UUID) {
	t.Helper()
	marker := previewJobMarker(t, h)
	h.pullRequestHook(t)
	waitPreviewJob(t, h, policy, marker)
}

func previewJobMarker(t *testing.T, h *hungPreview) int64 {
	t.Helper()
	var marker int64
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT COALESCE(max(id),0) FROM river_job").Scan(&marker); err != nil {
		t.Fatal(err)
	}
	return marker
}

// waitPreviewJob waits for a job checking request 7 under policy, queued after marker, to finish.
func waitPreviewJob(t *testing.T, h *hungPreview, policy uuid.UUID, marker int64) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var done bool
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM river_job WHERE id>$1 AND kind='platform_preview_v1' AND args->>'policy'=$2 AND args->>'number'='7' AND state='completed')", marker, policy.String()).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the request's preview job never finished")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// healthyTemplate saves and enables a second preview template whose policy uses host.
func healthyTemplate(t *testing.T, h *hungPreview, host uuid.UUID) string {
	t.Helper()
	base := h.path[:strings.LastIndex(h.path, "/")]
	var template api.Environment
	h.b.call("POST", base, map[string]string{"name": "Healthy template", "purpose": "preview_template"}, 201, &template)
	app := uuid.New()
	spec := h.spec
	spec.Applications = []domain.Application{h.spec.Applications[0]}
	spec.Applications[0].ID = app
	path := base + "/" + template.Id.String()
	var draft api.Revision
	h.b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": template.DraftRevisionId, "specification": spec}, 200, &draft)
	build := h.policy["build"].(pipeline.BuildConfig)
	build.RunnerID = host
	config := map[string]any{}
	for k, v := range h.policy {
		config[k] = v
	}
	config["baseRevisionId"], config["applicationId"], config["build"] = draft.Id, app, build
	h.b.call("PUT", path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": config}, 200, nil)
	return path
}

func previewAt(t *testing.T, h *hungPreview, path string) api.PullRequestPreview {
	t.Helper()
	var list []api.PullRequestPreview
	h.b.call("GET", path+"/previews", nil, 200, &list)
	if len(list) != 1 {
		t.Fatal("previews", list)
	}
	return list[0]
}

func waitPreviewAt(t *testing.T, h *hungPreview, path, state string) api.PullRequestPreview {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		var list []api.PullRequestPreview
		h.b.call("GET", path+"/previews", nil, 200, &list)
		if len(list) > 0 && list[0].State == state {
			return list[0]
		}
		if time.Now().After(deadline) {
			t.Fatal("the preview never reached", state, list)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// approvePreview approves the fork request under the template at path and waits for its build.
func approvePreview(t *testing.T, h *hungPreview, path string) api.PullRequestPreview {
	t.Helper()
	preview := waitPreviewAt(t, h, path, "approval_required")
	h.b.call("POST", path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": "approve", "head": preview.Head, "headRepository": preview.HeadRepository}, 200, nil)
	return waitPreviewAt(t, h, path, "building")
}

func nextPreviewCommand(t *testing.T, h *hungPreview) api.ExecutionCommand {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var p api.AgentPoll
		agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &p)
		if p.Command != nil {
			return *p.Command
		}
		if time.Now().After(deadline) {
			t.Fatal("no preview command arrived")
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// readyPreview takes the policy's request through its build, image resolution and deployment.
func readyPreview(t *testing.T, h *hungPreview) api.PullRequestPreview {
	t.Helper()
	approvePreview(t, h, h.path)
	poll := func() api.ExecutionCommand { return nextPreviewCommand(t, h) }
	report := func(c api.ExecutionCommand, result *pipeline.Result, observation *deployment.Observation) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
		if result != nil {
			raw, _ := stablejson.Marshal(result)
			msg := jsontext.Value(raw)
			r.Pipeline = &msg
		}
		if observation != nil {
			raw, _ := stablejson.Marshal(observation)
			msg := jsontext.Value(raw)
			r.Observation = &msg
		}
		agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, r, 204, nil)
	}
	build := poll()
	var command pipeline.Command
	if err := json.Unmarshal(*build.Pipeline, &command); err != nil {
		t.Fatal(err)
	}
	result := pipeline.Result{Commit: command.Commit, Image: "astilba-build-" + build.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("c", 64), Architecture: "arm64"}
	report(build, &result, nil)
	waitPreviewAt(t, h, h.path, "deploying")
	resolve := poll()
	var payload deployment.Payload
	if err := json.Unmarshal(*resolve.Visual, &payload); err != nil {
		t.Fatal(err)
	}
	images := map[string]string{}
	for _, a := range payload.Plan.Specification.Applications {
		images[a.ID.String()] = result.Digest
	}
	report(resolve, nil, &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("d", 64)})
	deploy := poll()
	if err := json.Unmarshal(*deploy.Visual, &payload); err != nil {
		t.Fatal(err)
	}
	resources := []deployment.Resource{}
	for _, a := range payload.Plan.Specification.Applications {
		resources = append(resources, deployment.Resource{ID: a.ID, Kind: "service", State: "running", Image: payload.Plan.Images[a.ID.String()]})
	}
	report(deploy, nil, &deployment.Observation{ObservedAt: time.Now(), Resources: resources, Images: payload.Plan.Images, Applied: true, StateHash: strings.Repeat("e", 64)})
	return waitPreviewAt(t, h, h.path, "ready")
}
