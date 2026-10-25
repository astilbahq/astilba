package integration

import (
	"encoding/json"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"strings"
	"testing"
	"time"
)

func TestReleasePlanHistoryAndRollbackAdmission(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Releases"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "HTTP"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := "busybox:stable"
	value := "v1"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "VALUE", Value: &value}}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "releases.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrollment, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.SafeReleases = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func() api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil {
			t.Fatal("missing command")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, obs deployment.Observation) {
		t.Helper()
		raw, _ := json.Marshal(obs)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*json.RawMessage)(&raw)}, 204, nil)
	}
	digest := "busybox@sha256:" + strings.Repeat("a", 64)
	prepare := func(host bool, options *api.ReleaseOptions) deployment.Status {
		t.Helper()
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrollment.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: host, ReleaseOptions: options}, 201, &status)
		report(poll(), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): digest}, StateHash: strings.Repeat("f", 64)})
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status
	}
	apply := func(status deployment.Status) {
		t.Helper()
		var op api.Operation
		key := uuid.New()
		url := path + "/deployment-plans/" + status.ID.String() + "/apply"
		b.call("POST", url, api.ApplyPlan{IdempotencyKey: key}, 201, &op)
		var again api.Operation
		b.call("POST", url, api.ApplyPlan{IdempotencyKey: key}, 201, &again)
		if op.Id != again.Id {
			t.Fatal("release duplicated")
		}
		c := poll()
		report(c, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "service", State: "running", Image: digest}}, Applied: true, StateHash: strings.Repeat("e", 64)})
	}
	first := prepare(true, nil)
	if first.Plan == nil || first.Plan.Release != nil {
		t.Fatal("initial creation promised old traffic")
	}
	apply(first)
	// No saved-spec change: mutable image identity alone selects the protected path.
	digest = "busybox@sha256:" + strings.Repeat("b", 64)
	next := prepare(false, nil)
	if next.Plan == nil || next.Plan.Release == nil || next.Plan.Release.PreviousPlanID != first.ID || next.Plan.Instances[app.String()] == uuid.Nil {
		t.Fatal("moved tag bypassed release protection")
	}
	apply(next)
	var history []api.ReleaseHistoryEntry
	b.call("GET", path+"/releases", nil, 200, &history)
	if len(history) != 2 {
		t.Fatalf("history contains unapplied plans: %d", len(history))
	}
	wrong := prefix + "/projects/" + uuid.NewString() + "/environments/" + env.Id.String()
	b.call("GET", wrong+"/releases", nil, 404, nil)
	var rolled api.Revision
	rollbackURL := path + "/releases/" + first.ID.String() + "/rollback"
	inRollback := api.RollbackRelease{BaseRevisionId: draft.Id, ApplicationId: app}
	b.call("POST", wrong+"/releases/"+first.ID.String()+"/rollback", inRollback, 404, nil)
	b.call("POST", rollbackURL, inRollback, 201, &rolled)
	b.call("POST", rollbackURL, inRollback, 409, nil)
	var restored domain.Specification
	if json.Unmarshal(rolled.Specification, &restored) != nil || *restored.Applications[0].Source.Image != "busybox@sha256:"+strings.Repeat("a", 64) {
		t.Fatal("rollback did not freeze historical image")
	}
	draft = rolled
	digest = *restored.Applications[0].Source.Image
	rollback := prepare(false, nil)
	if rollback.Plan == nil || rollback.Plan.Release == nil || len(rollback.Plan.Release.Options.Command) != 0 {
		t.Fatal("rollback skipped protection or copied hook")
	}
	// A capability downgrade after review cannot dispatch an instance plan.
	no := false
	in.Capabilities.SafeReleases = &no
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	b.call("POST", path+"/deployment-plans/"+rollback.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 400, nil)
}
