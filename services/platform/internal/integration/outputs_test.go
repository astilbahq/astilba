package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

func TestOutputAuthorityCloneAndAgentCompatibility(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var recipes []catalog.Recipe
	b.call("GET", prefix+"/service-catalog", nil, 200, &recipes)
	if len(recipes) != 5 {
		t.Fatal("catalog unavailable to workspace member")
	}
	b.call("GET", "/api/v1/workspaces/"+uuid.NewString()+"/service-catalog", nil, 404, nil)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Output references"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Private"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "output-secret-canary"}, 201, &secret)
	image := execution.TestImage
	source, target := uuid.New(), uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: source, Name: "Source", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}, Outputs: []domain.ServiceOutput{{Name: "AUTH", Type: "variable", Variable: "TOKEN"}}}, {ID: target, Name: "Target", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: source, TargetID: target, Output: "AUTH", Variable: "AUTH"}, {ID: uuid.New(), SourceID: source, TargetID: target, Output: "HOST", Variable: "SOURCE_HOST"}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	for _, copySecrets := range []bool{false, true} {
		var clone api.Environment
		b.call("POST", path+"/clone", api.CloneEnvironment{Name: map[bool]string{false: "Without secrets", true: "With secrets"}[copySecrets], CopySecrets: copySecrets}, 201, &clone)
		clonePath := prefix + "/projects/" + project.Id.String() + "/environments/" + clone.Id.String()
		var cloned api.Revision
		b.call("GET", clonePath+"/draft", nil, 200, &cloned)
		parsed, _, err := domain.ParseSpec(cloned.Specification)
		if err != nil {
			t.Fatal(err)
		}
		if parsed.Applications[0].ID == source || parsed.Bindings[0].SourceID == source || parsed.Bindings[0].TargetID == target {
			t.Fatal("clone retained original runtime identities")
		}
		refs := domain.SecretReferences(parsed)
		if copySecrets {
			if len(refs) != 1 || refs[0] == secret.Id || len(parsed.Bindings) != 2 {
				t.Fatal("copied output lost independent secret reference")
			}
		} else if len(refs) != 0 || len(parsed.Bindings) != 1 || parsed.Bindings[0].Output != "HOST" || len(parsed.Applications[0].Outputs) != 0 {
			t.Fatal("clone without secrets retained unresolvable output")
		}
		if strings.Contains(string(cloned.Specification), "output-secret-canary") {
			t.Fatal("clone exposed secret")
		}
	}
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes, no := true, false
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	request := api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}
	b.call("POST", path+"/deployment-plans", request, 400, nil)
	in.Capabilities.ServiceOutputs = &yes
	heartbeat := func() {
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	}
	heartbeat()
	var status deployment.Status
	b.call("POST", path+"/deployment-plans", request, 201, &status)
	var polled api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	if polled.Command == nil || strings.Contains(string(*polled.Command.Visual), "output-secret-canary") {
		t.Fatal("resolution received secret or no command")
	}
	observation, _ := json.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{source.String(): image, target.String(): image}, StateHash: strings.Repeat("a", 64)})
	c := polled.Command
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*json.RawMessage)(&observation)}, 204, nil)
	b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
	in.Capabilities.ServiceOutputs = &no
	heartbeat()
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 400, nil)
	in.Capabilities.ServiceOutputs = &yes
	heartbeat()
	var operation api.Operation
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
	var payload deployment.Payload
	if polled.Command == nil || json.Unmarshal(*polled.Command.Visual, &payload) != nil || payload.Secrets[secret.Id.String()] != "output-secret-canary" {
		t.Fatal("assigned agent lost output secret")
	}
	var stored []byte
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", operation.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "output-secret-canary") {
		t.Fatal("saved command contains hydrated output")
	}
}
