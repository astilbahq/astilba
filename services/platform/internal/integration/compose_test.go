package integration

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

func TestComposeSourceAdmissionAndExport(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Compose"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Files", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	if string(env.ConfigurationOwner) != "compose" {
		t.Fatal("ownership was not saved")
	}
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN", "value": "source-secret-canary"}, 201, &secret)
	original := "# retained comment\nx-extra: &settings\n  owner: example\nservices:\n  web:\n    image: " + execution.TestImage + "\n    privileged: true\n    environment:\n      TOKEN: ${TOKEN:?set token}\n    ports: ['127.0.0.1:18080:8080']\n    volumes: [data:/data, './message.txt:/message.txt:ro']\nvolumes:\n  data: {}\n"
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{{Name: "TOKEN", SecretVersionID: &secret.Id}}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: original}, {Path: "message.txt", Content: "from bundle\n"}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	var saved domain.Specification
	_ = json.Unmarshal(draft.Specification, &saved)
	if len(saved.Applications) != 1 || len(saved.Volumes) != 1 {
		t.Fatal("source graph missing")
	}
	oldVolume := saved.Volumes[0].ID
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}}, 400, nil)
	var preview api.ComposePreview
	b.call("POST", path+"/compose/preview", map[string]any{"specification": spec, "edit": map[string]string{"service": "web", "image": "busybox:1.37.0"}}, 200, &preview)
	if len(preview.Changes) != 1 || !strings.Contains(preview.Changes[0].After, "# retained comment") || len(preview.Permissions) != 2 {
		t.Fatal("preview lost source or elevated permissions")
	}
	bad := spec
	copyBundle := *spec.Compose
	bad.Compose = &copyBundle
	bad.Compose.Namespace = uuid.New()
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": bad}, 400, nil)
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
		raw, _ := json.Marshal(observation)
		message := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &message}, 204, nil)
	}
	create := func() deployment.Status {
		t.Helper()
		var status deployment.Status
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id, AllowHostChanges: true}, 201, &status)
		command := poll(status.OperationID)
		if strings.Contains(string(*command.Visual), "source-secret-canary") {
			t.Fatal("resolution received plaintext secret")
		}
		images := map[string]string{}
		for _, app := range saved.Applications {
			images[app.ID.String()] = *app.Source.Image
		}
		report(command, deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64)})
		b.call("GET", path+"/deployment-plans/"+status.ID.String(), nil, 200, &status)
		return status
	}
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id}, 400, nil)
	stale := create()
	value := "new value"
	spec.Variables = append(spec.Variables, domain.Variable{Name: "VALUE", Value: &value})
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	b.call("POST", path+"/deployment-plans/"+stale.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	_ = json.Unmarshal(draft.Specification, &saved)
	if saved.Volumes[0].ID != oldVolume {
		t.Fatal("ordinary source edit changed storage identity")
	}
	ready := create()
	if ready.Plan == nil || !strings.Contains(strings.Join(ready.Plan.Effects, " "), "privileged") {
		t.Fatal("review omitted elevated permissions")
	}
	key := uuid.New()
	var op, again api.Operation
	b.call("POST", path+"/deployment-plans/"+ready.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: key}, 201, &op)
	b.call("POST", path+"/deployment-plans/"+ready.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: key}, 201, &again)
	if op.Id != again.Id {
		t.Fatal("apply not idempotent")
	}
	command := poll(op.Id)
	var payload deployment.Payload
	_ = json.Unmarshal(*command.Visual, &payload)
	if payload.Secrets[secret.Id.String()] != "source-secret-canary" {
		t.Fatal("executor did not receive frozen secret")
	}
	app := saved.Applications[0]
	report(command, deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: app.ID, Kind: "service", State: "running", Image: *app.Source.Image}}, StateHash: strings.Repeat("b", 64)})
	var exported deployment.Export
	b.call("GET", path+"/export", nil, 200, &exported)
	if exported.Files[0].Content != original || strings.Contains(exported.Compose, "source-secret-canary") || !strings.Contains(exported.Compose, "ASTILBA_SECRET_") {
		t.Fatal("export lost source or exposed secret")
	}
	archive := b.call("GET", path+"/export/archive", nil, 200, nil)
	reader, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, file := range reader.File {
		if file.Name == "source/message.txt" {
			r, e := file.Open()
			if e != nil {
				t.Fatal(e)
			}
			data, _ := io.ReadAll(r)
			r.Close()
			found = string(data) == "from bundle\n"
		}
	}
	if !found {
		t.Fatal("archive omitted supporting files")
	}
	var clone api.Environment
	b.call("POST", path+"/clone", api.CloneEnvironment{Name: "Copy", CopySecrets: true}, 201, &clone)
	if string(clone.ConfigurationOwner) != "compose" || clone.DeploymentUnit.Id == env.DeploymentUnit.Id {
		t.Fatal("clone ownership or identity lost")
	}
	var cloned api.Revision
	b.call("GET", prefix+"/projects/"+project.Id.String()+"/environments/"+clone.Id.String()+"/draft", nil, 200, &cloned)
	var copied domain.Specification
	_ = json.Unmarshal(cloned.Specification, &copied)
	if copied.Compose.Namespace != clone.DeploymentUnit.Id || copied.Volumes[0].ID == oldVolume || *copied.Variables[0].SecretVersionID == secret.Id {
		t.Fatal("clone retained production resource or secret identity")
	}
	spec.Compose.Files[0].Content += "x-astilba:\n  routes:\n    - name: public\n      service: web\n      protocol: http\n      domain: original.example.test\n      port: 8080\n      tls: 'off'\n"
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	b.call("POST", path+"/clone", api.CloneEnvironment{Name: "Unsafe routes", CopySecrets: true}, 400, nil)

}
