package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

type troubleshootingFixture struct {
	f                  *fixture
	owner              *browser
	ws, env, app, app2 uuid.UUID
	prefix, path       string
	agentToken         string
}

func deployedTroubleshootingEnvironment(t *testing.T) troubleshootingFixture {
	f := setup(t)
	f.execution.Troubleshooting = true
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Sessions"}, 201, &project)
	base := prefix + "/projects/" + project.Id.String() + "/environments"
	var env api.Environment
	b.call("POST", base, map[string]string{"name": "Web"}, 201, &env)
	path := base + "/" + env.Id.String()
	image := execution.TestImage
	app, app2 := uuid.New(), uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{
		{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Replicas: 4, Health: &domain.HealthCheck{Port: 8080, Path: "/"}},
		{ID: app2, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}},
	}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "sessions.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	server, input, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2", ws, env.Id, draft.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE deployment_units SET server_id=$3 WHERE workspace_id=$1 AND environment_id=$2 AND primary_unit", ws, env.Id, server.Server.Id); err != nil {
		t.Fatal(err)
	}
	return troubleshootingFixture{f, b, ws, env.Id, app, app2, prefix, path, token}
}

func (x troubleshootingFixture) capabilities(t *testing.T, change func(*api.AgentCapabilities)) {
	t.Helper()
	capabilities := api.AgentCapabilities{Protocol: 1, Version: "test", Os: "linux", Arch: "arm64", Docker: true}
	change(&capabilities)
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/heartbeat", x.agentToken, api.AgentHeartbeat{Capabilities: capabilities, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 1 << 30, Notes: []string{}}}, 200, nil)
}

func (x troubleshootingFixture) optIn(t *testing.T) {
	t.Helper()
	yes := true
	x.capabilities(t, func(c *api.AgentCapabilities) { c.Troubleshooting, c.TroubleshootingEnabled = &yes, &yes })
}

func (x troubleshootingFixture) member(t *testing.T, email, role string) *browser {
	t.Helper()
	x.owner.call("POST", x.prefix+"/invitations", map[string]string{"email": email, "role": role}, 201, nil)
	b := x.f.browser(t)
	var accepted api.Session
	b.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": tokenFromLinkMail(t, x.f, "/invitation"), "name": role, "password": x.f.password}, 200, &accepted)
	b.csrf = accepted.CsrfToken
	return b
}

func (x troubleshootingFixture) open(t *testing.T, b *browser, app uuid.UUID, instance, status int) api.OpenedTroubleshootingSession {
	t.Helper()
	var out api.OpenedTroubleshootingSession
	raw := b.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: app, Instance: instance}, status, nil)
	if status == 201 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (x troubleshootingFixture) refused(t *testing.T, b *browser, reason string) {
	t.Helper()
	x.refusedAt(t, b, 1, reason)
}

func (x troubleshootingFixture) refusedAt(t *testing.T, b *browser, instance int, reason string) {
	t.Helper()
	var failure api.Error
	raw := b.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: instance}, 409, nil)
	if err := json.Unmarshal(raw, &failure); err != nil {
		t.Fatal(err)
	}
	if failure.Code != "troubleshooting_refused" || failure.Fields == nil || (*failure.Fields)[0] != reason {
		t.Fatalf("refusal %s, want reason %s", raw, reason)
	}
}

func (x troubleshootingFixture) audited(t *testing.T, action string, want int) {
	t.Helper()
	var got int
	if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action=$2", x.ws, action).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("%s audited %d times, want %d", action, got, want)
	}
}

// Every condition on the permission table refuses with a reason the dashboard can
// explain, and refusals by members are audited.
func TestTroubleshootingAdmissionDecisions(t *testing.T) {
	x := deployedTroubleshootingEnvironment(t)
	viewer := x.member(t, "viewer@example.test", "viewer")
	operator := x.member(t, "operator@example.test", "operator")

	x.f.execution.Troubleshooting = false
	var policy api.TroubleshootingPolicy
	x.owner.call("GET", x.path+"/troubleshooting", nil, 200, &policy)
	if policy.Available || policy.Enabled {
		t.Fatalf("policy before opt-in: %+v", policy)
	}
	x.refused(t, x.owner, domain.TroubleshootingInstallation)
	x.f.execution.Troubleshooting = true
	x.refused(t, x.owner, domain.TroubleshootingEnvironment)
	x.audited(t, "session.refused.environment", 1)

	enable := api.SaveTroubleshootingPolicy{Enabled: true}
	operator.call("PUT", x.path+"/troubleshooting", enable, 403, nil)
	x.f.auth.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	x.owner.call("PUT", x.path+"/troubleshooting", enable, 403, nil)
	x.f.auth.Now = time.Now
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true, Generation: 7}, 409, nil)
	x.owner.call("PUT", x.path+"/troubleshooting", enable, 200, &policy)
	if !policy.Enabled || policy.Generation != 1 {
		t.Fatalf("saved policy: %+v", policy)
	}
	x.audited(t, "troubleshooting.policy", 1)

	x.refused(t, x.owner, domain.TroubleshootingAgentUpdate)
	yes := true
	unavailable := api.Runtime
	x.capabilities(t, func(c *api.AgentCapabilities) { c.TroubleshootingUnavailable = &unavailable })
	x.refused(t, x.owner, domain.TroubleshootingRuntime)
	x.capabilities(t, func(c *api.AgentCapabilities) { c.Troubleshooting = &yes })
	x.refused(t, x.owner, domain.TroubleshootingHostOptIn)
	x.optIn(t)

	x.open(t, viewer, x.app, 1, 403)
	x.open(t, viewer, x.app, 1, 403)
	x.audited(t, "session.refused.forbidden", 1) // repeats are coalesced
	x.owner.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: uuid.New(), Instance: 1}, 409, nil)
	x.owner.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: 5}, 400, nil)
	other := uuid.New()
	x.owner.call("POST", x.path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: 1, ServerId: &other}, 400, nil)
	x.f.auth.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	x.open(t, x.owner, x.app, 1, 403)
	x.f.auth.Now = time.Now

	opened := x.open(t, operator, x.app, 1, 201)
	if opened.Ticket == "" || opened.Session.State != "offered" || time.Until(opened.Session.DeadlineAt) > domain.TroubleshootingMaxDuration {
		t.Fatalf("opened: %+v", opened)
	}
	var ticketHash, browserHash []byte
	var authVersion int64
	if err := x.f.st.Pool.QueryRow(context.Background(), "SELECT ticket_hash,browser_session_hash,auth_version FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&ticketHash, &browserHash, &authVersion); err != nil {
		t.Fatal(err)
	}
	if len(ticketHash) != 32 || len(browserHash) != 32 || authVersion < 1 {
		t.Fatal("session must store hashed bindings only")
	}
	x.audited(t, "session.open", 1)

	viewer.call("GET", x.path+"/troubleshooting/sessions", nil, 403, nil)
	var open []api.TroubleshootingSession
	operator.call("GET", x.path+"/troubleshooting/sessions", nil, 200, &open)
	if len(open) != 1 || open[0].Id != opened.Session.Id {
		t.Fatalf("open sessions: %+v", open)
	}
	operator.call("GET", x.prefix+"/troubleshooting-sessions", nil, 403, nil)

	// An owners-only restriction ends the operator's session and refuses new ones.
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true, OwnersOnly: true, Generation: policy.Generation}, 200, &policy)
	x.open(t, operator, x.app, 1, 403)
	var history []api.TroubleshootingSession
	x.owner.call("GET", x.prefix+"/troubleshooting-sessions", nil, 200, &history)
	if len(history) != 1 || history[0].State != "closed" || history[0].CloseReason == nil || *history[0].CloseReason != "environment setting changed" {
		t.Fatalf("history: %+v", history)
	}
	x.audited(t, "session.close", 1)

	// Widening again needs recent authentication; narrowing did not.
	x.f.auth.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true, Generation: policy.Generation}, 403, nil)
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: false, OwnersOnly: true, Generation: policy.Generation}, 200, &policy)
	x.f.auth.Now = time.Now
	x.refused(t, x.owner, domain.TroubleshootingEnvironment)
}

func TestTroubleshootingSessionLimitsAndClosing(t *testing.T) {
	x := deployedTroubleshootingEnvironment(t)
	ctx := context.Background()
	operator := x.member(t, "operator@example.test", "operator")
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
	x.optIn(t)

	first := x.open(t, x.owner, x.app, 1, 201)
	x.refused(t, operator, domain.TroubleshootingLimit) // one per container
	second := x.open(t, x.owner, x.app, 2, 201)
	x.open(t, x.owner, x.app, 3, 409) // two per actor

	operator.call("POST", x.prefix+"/troubleshooting-sessions/"+first.Session.Id.String()+"/close", nil, 403, nil)
	x.owner.call("POST", x.prefix+"/troubleshooting-sessions/"+first.Session.Id.String()+"/close", nil, 204, nil)
	x.owner.call("POST", x.prefix+"/troubleshooting-sessions/"+first.Session.Id.String()+"/close", nil, 404, nil)

	// An unattached offer stops counting at ticket expiry and is closed and audited.
	if _, err := x.f.st.Pool.Exec(ctx, "UPDATE troubleshooting_sessions SET ticket_expires_at=now()-interval '1 second' WHERE id=$1", second.Session.Id); err != nil {
		t.Fatal(err)
	}
	x.open(t, x.owner, x.app, 2, 201)
	var reason string
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT close_reason FROM troubleshooting_sessions WHERE id=$1", second.Session.Id).Scan(&reason); err != nil || reason != "expired" {
		t.Fatalf("expired offer: %q %v", reason, err)
	}

	// The host limit counts every actor.
	x.open(t, operator, x.app, 1, 201)
	x.open(t, operator, x.app, 3, 201)
	third := x.member(t, "second@example.test", "operator")
	x.open(t, x.owner, x.app2, 1, 201)
	x.refusedAt(t, third, 4, domain.TroubleshootingLimit)

	operator.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 403, nil)
	x.owner.call("POST", x.prefix+"/troubleshooting-sessions/close", nil, 204, nil)
	var open int
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM troubleshooting_sessions WHERE state<>'closed'").Scan(&open); err != nil || open != 0 {
		t.Fatalf("open after close all: %d %v", open, err)
	}
}

// Automation credentials never reach sessions, and a preview template refuses them
// whoever asks.
func TestTroubleshootingRequiresABrowserAndAStandardEnvironment(t *testing.T) {
	x := deployedTroubleshootingEnvironment(t)
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
	x.optIn(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	grant := issueAutomation(t, x.owner, x.path, domain.EnvironmentRead, domain.ConfigurationWrite, domain.DeploymentPlan, domain.DeploymentApply)
	for _, request := range []struct {
		method, path string
		body         any
	}{
		{"GET", x.path + "/troubleshooting", nil},
		{"PUT", x.path + "/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: false, Generation: 1}},
		{"GET", x.path + "/troubleshooting/sessions", nil},
		{"POST", x.path + "/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: 2}},
		{"GET", x.prefix + "/troubleshooting-sessions", nil},
		{"POST", x.prefix + "/troubleshooting-sessions/" + opened.Session.Id.String() + "/close", nil},
		{"POST", x.prefix + "/troubleshooting-sessions/close", nil},
	} {
		bearerCall(t, x.f, *grant.Token, request.method, request.path, request.body, 403, nil)
	}
	var project api.Project
	x.owner.call("POST", x.prefix+"/projects", map[string]string{"name": "Templates"}, 201, &project)
	base := x.prefix + "/projects/" + project.Id.String() + "/environments"
	var template api.Environment
	x.owner.call("POST", base, map[string]string{"name": "Previews", "purpose": "preview_template"}, 201, &template)
	path := base + "/" + template.Id.String()
	x.owner.call("PUT", path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 403, nil)
	x.owner.call("POST", path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: x.app, Instance: 1}, 403, nil)
}

// Both recovery paths share this fence: it needs an owner to re-enable sessions and
// leaves no restored session open or unaudited.
func TestTroubleshootingEndsAtManagementRecovery(t *testing.T) {
	x := deployedTroubleshootingEnvironment(t)
	ctx := context.Background()
	x.owner.call("PUT", x.path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)
	x.optIn(t)
	opened := x.open(t, x.owner, x.app, 1, 201)
	server := opened.Session.ServerId
	if err := x.f.st.PrepareRecovery(ctx, server, 1); err != nil {
		t.Fatal(err)
	}
	var enabled bool
	var state, reason string
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT enabled FROM troubleshooting_policies WHERE environment_id=$1", x.env).Scan(&enabled); err != nil || enabled {
		t.Fatal("recovery left terminal sessions enabled", err)
	}
	if err := x.f.st.Pool.QueryRow(ctx, "SELECT state,close_reason FROM troubleshooting_sessions WHERE id=$1", opened.Session.Id).Scan(&state, &reason); err != nil || state != "closed" || reason != "management recovery" {
		t.Fatal("recovery left a session open", state, reason, err)
	}
	x.audited(t, "session.close", 1)
}
