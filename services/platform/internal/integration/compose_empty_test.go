package integration

import (
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
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
	agent := authorityAgent{t, f, token, in}
	var deployed domain.Specification
	if err := json.Unmarshal(draft.Specification, &deployed); err != nil || len(deployed.Applications) != 1 || len(deployed.Routes) != 1 || len(deployed.Volumes) != 1 {
		t.Fatalf("source graph missing: %v", err)
	}
	web := deployed.Applications[0]
	images := map[string]string{web.ID.String(): *web.Source.Image}
	agent.reviewAndApply(b, path, enrolled.Server.Id, draft.Id, images)

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

	var status deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: emptied.Id, ServerId: enrolled.Server.Id}, 201, &status)
	agent.observe(agent.next(), deployment.Observation{Resources: []deployment.Resource{{ID: web.ID, Kind: "service", State: "running", Image: *web.Source.Image}}, Images: map[string]string{}, StateHash: strings.Repeat("a", 64)})
	b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
	if status.Plan == nil {
		t.Fatalf("plan was not reviewed: %s", status.State)
	}
	effects := strings.Join(status.Plan.Effects, "\n")
	for _, want := range []string{"Remove service web; retain its named volumes.", "Remove volume data from the configuration; retain its data", "Remove route app.example.test;"} {
		if !strings.Contains(effects, want) {
			t.Fatalf("reviewed effects omit %q:\n%s", want, effects)
		}
	}
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, nil)
	if plan := agent.applied(agent.next(), images); len(plan.Specification.Applications) != 0 || len(plan.Specification.Routes) != 0 {
		t.Fatal("the emptied deployment still runs services or routes")
	}
}
