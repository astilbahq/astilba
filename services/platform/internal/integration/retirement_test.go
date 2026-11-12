package integration

import (
	"context"
	"encoding/json/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// retirementFixture is one owner's workspace with a server whose agent can retire environments.
type retirementFixture struct {
	t      *testing.T
	f      *fixture
	owner  *browser
	ws     uuid.UUID
	prefix string
	agent  authorityAgent
	server uuid.UUID
}

func newRetirementFixture(t *testing.T) retirementFixture {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	var project api.Project
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Retirement"}, 201, &project)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Retirement = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	return retirementFixture{t, f, b, ws, "/api/v1/workspaces/" + ws.String() + "/projects/" + project.Id.String() + "/environments", authorityAgent{t, f, token, in}, enrolled.Server.Id}
}

// environment creates one with a routed service on a kept volume and returns its path and draft.
func (x retirementFixture) environment(name, domainName string) (api.Environment, string, api.Revision) {
	x.t.Helper()
	var env api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": name}, 201, &env)
	path := x.prefix + "/" + env.Id.String()
	app, volume, image := uuid.New(), uuid.New(), "busybox:stable"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Files"}},
		Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/srv"}}}},
		Routes:       []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: domainName, Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	return env, path, draft
}

func (x retirementFixture) deploy(path string, draft api.Revision) {
	x.t.Helper()
	_, spec := draftSpec(x.t, x.owner, path)
	images := map[string]string{}
	for _, app := range spec.Applications {
		images[app.ID.String()] = "busybox@sha256:" + strings.Repeat("a", 64)
	}
	x.agent.reviewAndApply(x.owner, path, x.server, draft.Id, images)
}

func (x retirementFixture) lifecycle(b *browser, path, action, name string, status int) api.Environment {
	x.t.Helper()
	var out api.Environment
	raw := b.call("POST", path+"/"+action, api.EnvironmentLifecycleRequest{ConfirmName: name}, status, nil)
	if status == 201 {
		if err := json.Unmarshal(raw, &out); err != nil {
			x.t.Fatal(err)
		}
	}
	return out
}

func (x retirementFixture) refusal(b *browser, method, path string, body any, field string) {
	x.t.Helper()
	var failure api.Error
	if err := json.Unmarshal(b.call(method, path, body, 409, nil), &failure); err != nil {
		x.t.Fatal(err)
	}
	if failure.Fields == nil || !slices.Contains(*failure.Fields, field) {
		x.t.Fatalf("%s %s: refused for %v, wanted %s", method, path, failure.Fields, field)
	}
}

func (x retirementFixture) report(c api.ExecutionCommand, state api.ExecutionState, note string) {
	x.t.Helper()
	agentRequest(x.t, x.f.server.Client(), x.f.server.URL, "/agent/v1/report", x.agent.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Note: note, Steps: []api.ExecutionStep{}}, 204, nil)
}

func (x retirementFixture) count(query string, args ...any) int {
	x.t.Helper()
	var n int
	if err := x.f.st.Pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		x.t.Fatal(err)
	}
	return n
}

func retireCommand(t *testing.T, c api.ExecutionCommand) deployment.Payload {
	t.Helper()
	var p deployment.Payload
	if c.Kind != "visual" || c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Action != "retire" {
		t.Fatalf("expected a retire command, got %s", c.Kind)
	}
	return p
}

// Retirement removes a deployed environment from its server through the agent, and only owners
// who confirmed recently, naming it exactly, may ask for it.
func TestRetireDeployedEnvironmentAndRestore(t *testing.T) {
	x := newRetirementFixture(t)
	env, path, draft := x.environment("Staging", "retire.example.test")
	x.deploy(path, draft)
	token := *issueAutomation(t, x.owner, path, domain.EnvironmentRead).Token

	// Who may ask.
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/invitations", map[string]string{"email": "operator@example.test", "role": "operator"}, 201, nil)
	operator := x.f.browser(t)
	var accepted api.Session
	operator.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": "Operator", "password": x.f.password}, 200, &accepted)
	operator.csrf = accepted.CsrfToken
	x.lifecycle(operator, path, "retire", "Staging", 403)
	bearerCall(t, x.f, token, "POST", path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, 403, nil)
	x.f.auth.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	x.lifecycle(x.owner, path, "retire", "Staging", 403)
	x.f.auth.Now = time.Now
	x.lifecycle(x.owner, path, "retire", "staging", 400)

	// An agent that cannot retire is not sent a command it would refuse.
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/heartbeat", x.agent.token, api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true, Visual: x.agent.in.Capabilities.Visual}, Preflight: x.agent.in.Preflight}, 200, nil)
	x.refusal(x.owner, "POST", path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, "retirement."+domain.RetirementAgentUpdate)
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/heartbeat", x.agent.token, api.AgentHeartbeat{Capabilities: x.agent.in.Capabilities, Preflight: x.agent.in.Preflight}, 200, nil)
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='environment.retire'", x.ws); n != 0 {
		t.Fatalf("refused requests were audited as retirements: %d", n)
	}

	retiring := x.lifecycle(x.owner, path, "retire", "Staging", 201)
	if retiring.State != api.EnvironmentStateRetiring || retiring.RetirementOperationId == nil || retiring.RetiredBy == nil || retiring.RetiredAt == nil {
		t.Fatalf("retiring environment: %+v", retiring)
	}
	// Its credentials are revoked at once, with an audit record each.
	bearerCall(t, x.f, token, "GET", path, nil, 401, nil)
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='automation.revoke'", x.ws); n != 1 {
		t.Fatalf("revocations audited: %d", n)
	}
	// Nothing new starts while it retires, and the request is not repeated.
	x.refusal(x.owner, "POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: x.server, BaseRevisionId: draft.Id}, "environment.state")
	x.refusal(x.owner, "PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": draft.Specification}, "environment.state")
	x.refusal(x.owner, "POST", path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, "retirement."+domain.RetirementActiveOperation)
	x.refusal(x.owner, "POST", path+"/restore", api.EnvironmentLifecycleRequest{ConfirmName: "Staging"}, "retirement."+domain.RetirementState)

	c := x.agent.next()
	payload := retireCommand(t, c)
	if c.Id != *retiring.RetirementOperationId || c.ServerId != x.server || c.EnvironmentId != env.Id || c.ResourceId != env.DeploymentUnit.Id || payload.Plan.UnitID != env.DeploymentUnit.Id || len(payload.Secrets) != 0 || len(payload.RegistryAccess) != 0 || len(payload.Plan.Specification.Routes) != 1 {
		t.Fatalf("retire command does not name the deployed unit: %+v %+v", c, payload.Plan)
	}
	if n := x.count("SELECT count(*) FROM route_claims WHERE workspace_id=$1 AND environment_id=$2", x.ws, env.Id); n != 1 {
		t.Fatalf("claims released before the host confirmed removal: %d", n)
	}
	x.report(c, "succeeded", "Removed.")
	var retired api.Environment
	x.owner.call("GET", path, nil, 200, &retired)
	if retired.State != api.EnvironmentStateRetired || retired.AppliedRevisionId != nil || retired.RetiredWithoutHost == nil || *retired.RetiredWithoutHost {
		t.Fatalf("retired environment: %+v", retired)
	}
	if n := x.count("SELECT count(*) FROM route_claims WHERE workspace_id=$1 AND environment_id=$2", x.ws, env.Id); n != 0 {
		t.Fatalf("route claims kept after retirement: %d", n)
	}
	if n := x.count("SELECT count(*) FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND NOT deleted", x.ws, env.Id); n != 1 {
		t.Fatalf("retained volumes: %d", n)
	}
	// History and the configuration it last deployed stay readable.
	var exported deployment.Export
	x.owner.call("GET", path+"/export", nil, 200, &exported)
	x.owner.call("GET", path+"/revisions", nil, 200, nil)

	// Another environment can now serve its hostname, so restoring and planning names the conflict.
	_, otherPath, otherDraft := x.environment("Replacement", "retire.example.test")
	x.deploy(otherPath, otherDraft)
	restored := x.lifecycle(x.owner, path, "restore", "Staging", 201)
	if restored.State != api.EnvironmentStateActive || restored.RetiredAt != nil || restored.RetirementOperationId != nil {
		t.Fatalf("restored environment: %+v", restored)
	}
	x.refusal(x.owner, "POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: x.server, BaseRevisionId: draft.Id, AllowHostChanges: true}, "routes.claimed")
	// Credentials stay revoked after restore.
	bearerCall(t, x.f, token, "GET", path, nil, 401, nil)

	// Without that route, the next plan deploys onto the kept volume.
	_, spec := draftSpec(t, x.owner, path)
	spec.Routes = []domain.Route{}
	var edited api.Revision
	x.owner.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &edited)
	x.deploy(path, edited)
	if n := x.count("SELECT count(*) FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND id=$3 AND NOT deleted", x.ws, env.Id, spec.Volumes[0].ID); n != 1 {
		t.Fatal("the restored deployment did not keep its volume")
	}
	if n := x.count("SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action IN ('environment.retire','environment.restore') AND resource_id=$2", x.ws, env.Id); n != 2 {
		t.Fatalf("lifecycle audit events: %d", n)
	}
}

// An environment that was never deployed has nothing on a host, and one whose server was revoked
// cannot be reached, so both finish without a command. A failed retirement can be asked again.
func TestRetirementWithoutHostAndPreconditions(t *testing.T) {
	x := newRetirementFixture(t)

	var fresh api.Environment
	x.owner.call("POST", x.prefix, map[string]string{"name": "Draft only"}, 201, &fresh)
	freshPath := x.prefix + "/" + fresh.Id.String()
	if v := x.lifecycle(x.owner, freshPath, "retire", "Draft only", 201); v.State != api.EnvironmentStateRetired || v.RetirementOperationId != nil {
		t.Fatalf("never-deployed environment: %+v", v)
	}
	var listed []api.Environment
	x.owner.call("GET", x.prefix, nil, 200, &listed)
	if len(listed) != 1 || listed[0].State != api.EnvironmentStateRetired {
		t.Fatalf("listed environments: %+v", listed)
	}

	_, path, draft := x.environment("Production", "failed.example.test")
	// An operation in flight is never cancelled by retirement.
	var plan deployment.Status
	x.owner.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: x.server, BaseRevisionId: draft.Id, AllowHostChanges: true}, 201, &plan)
	x.refusal(x.owner, "POST", path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Production"}, "retirement."+domain.RetirementActiveOperation)
	images := map[string]string{}
	_, spec := draftSpec(t, x.owner, path)
	images[spec.Applications[0].ID.String()] = "busybox@sha256:" + strings.Repeat("b", 64)
	x.agent.observe(x.agent.next(), deployment.Observation{Images: images, StateHash: strings.Repeat("f", 64)})
	x.owner.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, nil)
	x.agent.applied(x.agent.next(), images)

	// A host that could not finish leaves it retiring; asking again queues a new command.
	first := x.lifecycle(x.owner, path, "retire", "Production", 201)
	failed := x.agent.next()
	retireCommand(t, failed)
	x.report(failed, "failed", "Ingress changed outside Astilba.")
	var still api.Environment
	x.owner.call("GET", path, nil, 200, &still)
	if still.State != api.EnvironmentStateRetiring || still.AppliedRevisionId == nil {
		t.Fatalf("a failed retirement changed the environment: %+v", still)
	}
	again := x.lifecycle(x.owner, path, "retire", "Production", 201)
	if again.RetirementOperationId == nil || *again.RetirementOperationId == *first.RetirementOperationId {
		t.Fatal("asking again did not queue a new retirement")
	}
	retried := x.agent.next()
	if retried.Id != *again.RetirementOperationId || retried.Generation <= failed.Generation {
		t.Fatal("the new retirement was not fenced above the failed one")
	}

	// Once its server is revoked, retirement finishes without it and says so.
	x.report(retried, "failed", "Still failing.")
	x.owner.call("POST", "/api/v1/workspaces/"+x.ws.String()+"/servers/"+x.server.String()+"/revoke", nil, 204, nil)
	revoked := x.lifecycle(x.owner, path, "retire", "Production", 201)
	if revoked.State != api.EnvironmentStateRetired || revoked.RetiredWithoutHost == nil || !*revoked.RetiredWithoutHost || revoked.RetirementOperationId != nil {
		t.Fatalf("retired without its host: %+v", revoked)
	}
	// Nothing confirmed a change on that host, so what management knew of it is kept.
	if revoked.AppliedRevisionId == nil {
		t.Fatal("an unconfirmed removal cleared the applied revision")
	}
}

// Retiring a preview template turns its policy off and cleans up its live previews through their own
// cleanup, with the retiring owner's authority, without waiting for them: a template never deploys.
func TestRetirePreviewTemplateCleansUpItsPreviews(t *testing.T) {
	h := newHungPreview(t)
	ctx := t.Context()
	var policy api.PreviewPolicy
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, &policy)
	preview := readyPreview(t, h)

	var retired api.Environment
	h.b.call("POST", h.path+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "PR template"}, 201, &retired)
	if retired.State != api.EnvironmentStateRetired || retired.RetiredBy == nil || retired.RetirementOperationId != nil {
		t.Fatalf("retired template: %+v", retired)
	}
	var enabled bool
	var actor *uuid.UUID
	if err := h.f.st.Pool.QueryRow(ctx, "SELECT p.enabled,r.cleanup_actor FROM preview_policies p JOIN preview_requests r ON r.policy_id=p.id WHERE p.workspace_id=$1 AND r.id=$2", h.ws, preview.Id).Scan(&enabled, &actor); err != nil {
		t.Fatal(err)
	}
	if enabled || actor == nil || *actor != *retired.RetiredBy {
		t.Fatalf("policy enabled %v, cleanup requested by %v rather than %v", enabled, actor, *retired.RetiredBy)
	}

	waitPreviewAt(t, h, h.path, "deleting")
	cleanup := nextPreviewCommand(t, h)
	var payload deployment.Payload
	if err := json.Unmarshal(*cleanup.Visual, &payload); err != nil || payload.Action != "preview-cleanup" || cleanup.EnvironmentId != *preview.EnvironmentId {
		t.Fatalf("expected the preview's own cleanup, got %s for %s", payload.Action, cleanup.EnvironmentId)
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: cleanup.Id, Generation: cleanup.Generation, PlanHash: cleanup.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	waitPreviewAt(t, h, h.path, "deleted")
	// The template's previews stay readable, and a new provider event starts nothing for it.
	h.pullRequestHook(t)
	waitPreviewChecks(t, h)
	if after := previewAt(t, h, h.path); after.State != "deleted" {
		t.Fatal("a retired template's preview came back", after.State, after.Note)
	}
}
