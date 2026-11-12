package integration

import (
	"encoding/json/jsontext"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

type hostReviewFixture struct {
	t      *testing.T
	f      *fixture
	b      *browser
	path   string
	server uuid.UUID
	token  string
	app    uuid.UUID
	spec   domain.Specification
	draft  uuid.UUID
}

// newHostReview is an owner's environment with one routed service on a fresh visual server.
func newHostReview(t *testing.T) *hostReviewFixture {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Host review"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	x := &hostReviewFixture{t: t, f: f, b: b, path: prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String(), app: uuid.New(), draft: env.DraftRevisionId}
	image := execution.TestImage
	x.spec = domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: x.app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Volumes: []domain.Volume{}, Bindings: []domain.Binding{}, Routes: []domain.Route{{ID: uuid.New(), TargetID: x.app, Protocol: "http", Domain: "app.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	x.save()
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	x.server, x.token = enrolled.Server.Id, token
	return x
}

func (x *hostReviewFixture) save() {
	x.t.Helper()
	var draft api.Revision
	x.b.call("PUT", x.path+"/draft", map[string]any{"baseRevisionId": x.draft, "specification": x.spec}, 200, &draft)
	x.draft = draft.Id
}

func (x *hostReviewFixture) poll(id uuid.UUID) api.ExecutionCommand {
	x.t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
		var p api.AgentPoll
		agentRequest(x.t, x.f.server.Client(), x.f.server.URL, "/agent/v1/poll", x.token, nil, 200, &p)
		if p.Command != nil && p.Command.Id == id {
			return *p.Command
		}
	}
	x.t.Fatal("operation not dispatched")
	return api.ExecutionCommand{}
}

func (x *hostReviewFixture) report(c api.ExecutionCommand, obs deployment.Observation) {
	x.t.Helper()
	raw, _ := stablejson.Marshal(obs)
	agentRequest(x.t, x.f.server.Client(), x.f.server.URL, "/agent/v1/report", x.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
}

// plan prepares and resolves a plan, the agent reporting the given host changes.
func (x *hostReviewFixture) plan(reported ...deployment.HostChange) deployment.Status {
	x.t.Helper()
	var s deployment.Status
	x.b.call("POST", x.path+"/deployment-plans", api.VisualPlanRequest{ServerId: x.server, BaseRevisionId: x.draft}, 201, &s)
	x.report(x.poll(s.OperationID), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{x.app.String(): execution.TestImage}, StateHash: strings.Repeat("a", 64), HostChanges: reported})
	x.b.call("GET", x.path+"/deployment-plans/"+s.ID.String(), nil, 200, &s)
	if s.Plan == nil {
		x.t.Fatal("resolved plan missing")
	}
	return s
}

func (x *hostReviewFixture) apply(s deployment.Status, approve bool, status int) api.Operation {
	x.t.Helper()
	var op api.Operation
	x.b.call("POST", x.path+"/deployment-plans/"+s.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: &approve}, status, &op)
	return op
}

func (x *hostReviewFixture) deployed(op api.Operation) {
	x.t.Helper()
	x.report(x.poll(op.Id), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: x.app, Kind: "service", State: "running", Image: execution.TestImage}}, Applied: true, StateHash: strings.Repeat("b", 64)})
}

func (x *hostReviewFixture) later(d time.Duration) {
	at := time.Now().Add(d)
	x.f.auth.Now = func() time.Time { return at }
}

// Preparing a plan needs no approval and no confirmation, even when it changes the host. Applying
// it needs the owner's approval of the listed changes and then a recent confirmation, in that
// order, so nobody is asked to confirm before they have approved anything.
func TestHostChangesAreApprovedWhenThePlanIsApplied(t *testing.T) {
	x := newHostReview(t)
	x.later(20 * time.Minute)
	first := x.plan()
	if !first.Plan.HostChanges || !slices.ContainsFunc(first.Plan.Effects, func(e string) bool { return strings.Contains(e, "publish the reviewed host ports") }) {
		t.Fatalf("a first route on the server was not reviewed as a host change: %v", first.Plan.Effects)
	}
	x.apply(first, false, 400)
	x.apply(first, true, 403)
	x.b.reauthenticate(map[string]string{"password": x.f.password}, 200)
	x.deployed(x.apply(first, true, 201))
}

// A redeploy that changes nothing on the host, such as new environment variables, needs
// neither approval nor a recent confirmation, and its plan claims no host changes.
func TestOrdinaryRedeployNeedsNoConfirmation(t *testing.T) {
	x := newHostReview(t)
	x.deployed(x.apply(x.plan(), true, 201))
	x.spec.Variables = greeting()
	x.spec.Routes[0].AllowCIDRs = []string{"192.0.2.0/24"}
	x.save()
	x.later(20 * time.Minute)
	next := x.plan()
	if next.Plan.HostChanges || slices.ContainsFunc(next.Plan.Effects, func(e string) bool { return strings.Contains(e, "Traefik") }) {
		t.Fatalf("an ordinary redeploy was reviewed as a host change: %v", next.Plan.Effects)
	}
	x.deployed(x.apply(next, false, 201))
}

// What only the agent can see, such as turning on managed egress, makes the plan a host change
// too, in the reviewed wording rather than the agent's own.
func TestAgentReportedHostChangesNeedApproval(t *testing.T) {
	x := newHostReview(t)
	x.deployed(x.apply(x.plan(), true, 201))
	x.spec.Variables = greeting()
	x.save()
	reported := x.plan(deployment.HostChange{Kind: deployment.HostChangeManagedEgress})
	if !reported.Plan.HostChanges || !reported.HostReview || !slices.Contains(reported.Plan.Effects, deployment.HostChangeEffect(deployment.HostChange{Kind: deployment.HostChangeManagedEgress})) {
		t.Fatalf("an agent-reported host change was not reviewed: %+v", reported.Plan)
	}
	x.apply(reported, false, 400)
	x.deployed(x.apply(reported, true, 201))
}

// Confirming identity covers fifteen minutes of ordinary security-sensitive changes, such as
// saving a secret, but only five of changes that could outlast the session: the account's own
// credentials, and host changes, which can give root on the server.
func TestRecentConfirmationWindows(t *testing.T) {
	x := newHostReview(t)
	first := x.plan()
	x.later(10 * time.Minute)
	x.b.call("POST", "/api/v1/auth/password", map[string]string{"password": "a replacement password here"}, 403, nil)
	x.apply(first, true, 403)
	x.b.call("POST", x.path+"/secrets", map[string]string{"name": "TOKEN", "value": "secret-for-window-test"}, 201, nil)
	x.later(16 * time.Minute)
	x.b.call("POST", x.path+"/secrets", map[string]string{"name": "OTHER", "value": "secret-for-window-test"}, 403, nil)
	x.b.reauthenticate(map[string]string{"password": x.f.password}, 200)
	x.deployed(x.apply(first, true, 201))
}

func greeting() []domain.Variable {
	value := "hello"
	return []domain.Variable{{Name: "GREETING", Value: &value}}
}

// approved approves a plan's host changes when applying it.
func approved() *bool {
	yes := true
	return &yes
}
