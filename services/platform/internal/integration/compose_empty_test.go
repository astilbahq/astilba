package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A client reads a revision, changes it and sends the whole document back. A Compose
// route derives "allowCidrs": null, so the request contract must accept what the server
// returns, and an environment emptied after a deployment must plan its own removal.
func TestComposeEnvironmentEmptiedAfterDeployment(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Compose"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "source-secret-canary"}, 201, &secret)
	source := "services:\n  web:\n    image: " + execution.TestImage + "\n    environment:\n      TOKEN: ${TOKEN:?set token}\n    volumes: [data:/data]\nvolumes:\n  data: {}\nx-astilba:\n  routes:\n    - name: public\n      service: web\n      protocol: http\n      domain: app.example.test\n      port: 8080\n      tls: 'off'\n"
	value := "plain"
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}, {Name: "PLAIN", Value: &value}}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: source}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)

	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Compose = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			var value api.AgentPoll
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &value)
			if value.Command != nil && value.Command.Id == id {
				return *value.Command
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatal("operation was not dispatched")
		return api.ExecutionCommand{}
	}
	report := func(command api.ExecutionCommand, observation deployment.Observation) {
		t.Helper()
		raw, _ := stablejson.Marshal(observation)
		message := jsontext.Value(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &message}, 204, nil)
	}
	var deployed domain.Specification
	if err := json.Unmarshal(draft.Specification, &deployed); err != nil || len(deployed.Applications) != 1 || len(deployed.Routes) != 1 || len(deployed.Volumes) != 1 {
		t.Fatalf("source graph missing: %v", err)
	}
	web := deployed.Applications[0]
	running := []deployment.Resource{{ID: web.ID, Kind: "service", State: "running", Image: *web.Source.Image}}
	deploy := func(revision uuid.UUID, planned domain.Specification, resources []deployment.Resource) deployment.Status {
		t.Helper()
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision, ServerId: enrolled.Server.Id, AllowHostChanges: true}, 201, &status)
		images := map[string]string{}
		for _, app := range planned.Applications {
			images[app.ID.String()] = *app.Source.Image
		}
		report(poll(status.OperationID), deployment.Observation{ObservedAt: time.Now(), Resources: resources, Images: images, StateHash: strings.Repeat("a", 64)})
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		if status.Plan == nil {
			t.Fatalf("plan was not reviewed: %s", status.State)
		}
		var op api.Operation
		b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
		command := poll(op.Id)
		var payload deployment.Payload
		if err := json.Unmarshal(*command.Visual, &payload); err != nil {
			t.Fatal(err)
		}
		applied := []deployment.Resource{}
		for _, app := range payload.Plan.Specification.Applications {
			applied = append(applied, deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: *app.Source.Image})
		}
		report(command, deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: applied, StateHash: strings.Repeat("b", 64)})
		deadline := time.Now().Add(5 * time.Second)
		for b.call("GET", path+"/operations/"+op.Id.String(), nil, 200, &op); op.State != "succeeded" && time.Now().Before(deadline); b.call("GET", path+"/operations/"+op.Id.String(), nil, 200, &op) {
			time.Sleep(20 * time.Millisecond)
		}
		if op.State != "succeeded" {
			t.Fatalf("apply ended %s", op.State)
		}
		return status
	}
	deploy(draft.Id, deployed, []deployment.Resource{})

	// The dashboard edits the revision it read, including the members derived from the source.
	var current api.Revision
	b.call("GET", path+"/draft", nil, 200, &current)
	if !strings.Contains(string(current.Specification), `"allowCidrs":null`) {
		t.Fatal("fixture no longer derives a route without an allowlist")
	}
	var edited map[string]any
	if err := json.Unmarshal(current.Specification, &edited); err != nil {
		t.Fatal(err)
	}
	b.call("POST", path+"/compose/preview", map[string]any{"specification": edited}, 200, nil)
	edited["compose"].(map[string]any)["files"].([]any)[0].(map[string]any)["content"] = "services: {}\n"
	edited["variables"] = []any{}
	var preview api.ComposePreview
	b.call("POST", path+"/compose/preview", map[string]any{"specification": edited}, 200, &preview)
	if len(preview.Diagnostics) != 0 || preview.Specification == nil {
		t.Fatalf("empty source did not validate: %+v", preview.Diagnostics)
	}
	var emptied api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, &emptied)
	var saved domain.Specification
	_ = json.Unmarshal(emptied.Specification, &saved)
	if len(saved.Applications) != 0 || len(saved.Routes) != 0 || len(saved.Volumes) != 0 || len(saved.Variables) != 0 {
		t.Fatal("saved revision kept derived resources the source no longer declares")
	}

	status := deploy(emptied.Id, saved, running)
	effects := strings.Join(status.Plan.Effects, "\n")
	for _, want := range []string{"Remove service web; retain its named volumes.", "Remove volume data from the configuration; retain its data", "Remove route app.example.test;"} {
		if !strings.Contains(effects, want) {
			t.Fatalf("reviewed effects omit %q:\n%s", want, effects)
		}
	}
	if len(status.Plan.Specification.Applications) != 0 || len(status.Plan.Specification.Routes) != 0 {
		t.Fatal("emptied plan still deploys services or routes")
	}
}
