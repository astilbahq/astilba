package integration

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Terminal sessions and scheduled jobs are refused in a privileged service and in every service
// that shares a named volume or a network with it, whoever asks.
func TestTerminalsAndJobsRefuseServicesBesidePrivilegedOnes(t *testing.T) {
	f := setup(t)
	f.execution.Troubleshooting = true
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Builders"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "CI", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	image := execution.TestImage
	source := `services:
  dind:
    image: ` + image + `
    privileged: true
    volumes: [certs:/certs]
    networks: [builder]
  client:
    image: ` + image + `
    volumes: [certs:/certs/client:ro]
    networks: [apps]
  runner:
    image: ` + image + `
    networks: [builder]
  web:
    image: ` + image + `
    networks: [apps]
volumes:
  certs: {}
networks:
  builder: {}
  apps: {}
`
	spec := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}, Compose: &composeproject.Bundle{Namespace: env.DeploymentUnit.Id, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: source}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	var saved domain.Specification
	if err := json.Unmarshal(draft.Specification, &saved); err != nil {
		t.Fatal(err)
	}
	ids := map[string]uuid.UUID{}
	images := map[string]string{}
	resources := []deployment.Resource{}
	for _, app := range saved.Applications {
		ids[app.Name] = app.ID
		images[app.ID.String()] = image
		resources = append(resources, deployment.Resource{ID: app.ID, Kind: "service", State: "running", Image: image})
	}

	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual, in.Capabilities.Compose, in.Capabilities.Backups = &yes, &yes, &yes
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
	var status deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id}, 201, &status)
	report(poll(status.OperationID), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("a", 64), HostChangesReported: true})
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+status.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	report(poll(op.Id), deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: resources, StateHash: strings.Repeat("b", 64)})

	capabilities := in.Capabilities
	capabilities.Troubleshooting, capabilities.TroubleshootingEnabled = &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: capabilities, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 1 << 30, Notes: []string{}}}, 200, nil)
	b.call("PUT", path+"/troubleshooting", api.SaveTroubleshootingPolicy{Enabled: true}, 200, nil)

	t.Run("terminals", func(t *testing.T) {
		for name, elevated := range map[string]bool{"dind": true, "client": true, "runner": true, "web": false} {
			if !elevated {
				b.call("POST", path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: ids[name], Instance: 1}, 201, nil)
				continue
			}
			var failure api.Error
			raw := b.call("POST", path+"/troubleshooting/sessions", api.OpenTroubleshootingSession{ApplicationId: ids[name], Instance: 1}, 409, nil)
			if err := json.Unmarshal(raw, &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Code != "troubleshooting_refused" || failure.Fields == nil || (*failure.Fields)[0] != domain.TroubleshootingElevated {
				t.Fatalf("%s: refusal %s, want %s", name, raw, domain.TroubleshootingElevated)
			}
		}
	})

	t.Run("scheduled jobs", func(t *testing.T) {
		none := ""
		job := func(name string) api.BackupPolicyRequest {
			return api.BackupPolicyRequest{Name: "Job in " + name, ResourceId: ids[name], Method: "container-job", Schedule: api.DailySchedule{Hour: 3, Timezone: "UTC"}, Retain: 1, Enabled: true, Command: []string{"true"}, Password: &none}
		}
		for _, name := range []string{"dind", "client", "runner"} {
			var failure api.Error
			raw := b.call("POST", path+"/backup-policies", job(name), 400, nil)
			if err := json.Unmarshal(raw, &failure); err != nil {
				t.Fatal(err)
			}
			if failure.Fields == nil || (*failure.Fields)[0] != "resourceId" || !strings.Contains(failure.Message, "privileged") {
				t.Fatalf("%s: refusal %s", name, raw)
			}
		}
		var policy backup.Policy
		b.call("POST", path+"/backup-policies", job("web"), 201, &policy)
		// A job saved before its source became elevated cannot be run either.
		if _, err := f.st.Pool.Exec(t.Context(), `UPDATE backup_policies SET configuration=jsonb_set(configuration,'{resourceId}',to_jsonb($3::text)) WHERE workspace_id=$1 AND id=$2`, ws, policy.ID, ids["runner"].String()); err != nil {
			t.Fatal(err)
		}
		b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "install", RequestKey: uuid.New()}, 400, nil)
	})
}
