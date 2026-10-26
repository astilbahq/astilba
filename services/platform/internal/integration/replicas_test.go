package integration

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestReplicaCapabilityInventoryAndRollbackAdmission(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Replicas"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "HTTP"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app, image := uuid.New(), "busybox:stable"
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Replicas: 3, Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "replicas.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes, no := true, false
	in.Capabilities.Visual, in.Capabilities.SafeReleases = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	heartbeat := func(enabled bool) {
		t.Helper()
		in.Capabilities.Replicas = &no
		if enabled {
			in.Capabilities.Replicas = &yes
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	}
	poll := func() api.ExecutionCommand {
		t.Helper()
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &out)
		if out.Command == nil {
			t.Fatal("missing command")
		}
		return *out.Command
	}
	report := func(c api.ExecutionCommand, observation deployment.Observation, status int) {
		t.Helper()
		raw, _ := json.Marshal(observation)
		value := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &value}, status, nil)
	}
	input := func(host bool) api.VisualPlanRequest {
		return api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id, AllowHostChanges: host}
	}
	b.call("POST", path+"/deployment-plans", input(true), 400, nil)
	heartbeat(true)
	digest := "busybox@sha256:" + strings.Repeat("a", 64)
	prepare := func(host bool) deployment.Status {
		t.Helper()
		var out deployment.Status
		b.call("POST", path+"/deployment-plans", input(host), 201, &out)
		report(poll(), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): digest}, StateHash: strings.Repeat("f", 64)}, 204)
		b.call("GET", path+"/deployment-plans/"+out.ID.String(), nil, 200, &out)
		return out
	}
	first := prepare(true)
	if first.Plan == nil || first.Plan.Recipe != deployment.ReplicaRecipe || len(first.Plan.InstanceIDs(app)) != 3 || first.Plan.Release != nil {
		t.Fatal("initial group was not frozen for review")
	}
	applyURL := path + "/deployment-plans/" + first.ID.String() + "/apply"
	heartbeat(false)
	b.call("POST", applyURL, api.ApplyPlan{IdempotencyKey: uuid.New()}, 400, nil)
	heartbeat(true)
	var op api.Operation
	b.call("POST", applyURL, api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	// Capability loss after admission must also refuse credential hydration.
	heartbeat(false)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 400, nil)
	heartbeat(true)
	c := poll()
	if c.Id != op.Id {
		t.Fatal("capability recovery replaced the admitted operation")
	}
	observation := func(p deployment.Plan) deployment.Observation {
		resource := deployment.Resource{ID: app, Kind: "service", State: "running", Image: digest}
		for _, id := range p.InstanceIDs(app) {
			resource.Instances = append(resource.Instances, deployment.InstanceState{InstanceID: id, State: "running"})
		}
		return deployment.Observation{ObservedAt: time.Now(), Applied: true, StateHash: strings.Repeat("e", 64), Resources: []deployment.Resource{resource}}
	}
	bad := observation(*first.Plan)
	bad.Resources[0].Instances = bad.Resources[0].Instances[:2]
	report(c, bad, 400)
	bad = observation(*first.Plan)
	bad.Resources[0].Instances[2].InstanceID = uuid.New()
	report(c, bad, 400)
	report(c, observation(*first.Plan), 204)

	spec.Applications[0].Replicas = 2
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	next := prepare(false)
	if next.Plan == nil || next.Plan.Release == nil || len(next.Plan.InstanceIDs(app)) != 2 {
		t.Fatal("count-only scaling bypassed the protected release")
	}
	for _, id := range next.Plan.InstanceIDs(app) {
		if slices.Contains(first.Plan.InstanceIDs(app), id) {
			t.Fatal("replacement group reused a serving instance")
		}
	}
	b.call("POST", path+"/deployment-plans/"+next.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	report(poll(), observation(*next.Plan), 204)
	var rolled api.Revision
	b.call("POST", path+"/releases/"+first.ID.String()+"/rollback", api.RollbackRelease{BaseRevisionId: draft.Id, ApplicationId: app}, 201, &rolled)
	var restored domain.Specification
	if json.Unmarshal(rolled.Specification, &restored) != nil || restored.Applications[0].Replicas != 3 {
		t.Fatal("rollback draft lost historical replica intent")
	}
	var runtime deployment.Observation
	b.call("GET", path+"/runtime", nil, 200, &runtime)
	if len(runtime.Resources) != 1 || len(runtime.Resources[0].Instances) != 2 {
		t.Fatal("rollback draft changed the installed instance inventory")
	}
}
