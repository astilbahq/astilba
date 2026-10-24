package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
)

func TestUpgradeApprovalRecoveryAndTenantBoundary(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Upgrade"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "PostgreSQL"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Database", "value": "upgrade-database-canary"}, 201, &secret)
	pg, vol := uuid.New(), uuid.New()
	image := domain.Postgres17Image
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: domain.PostgresMount(image)}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Backups = &yes
	in.Capabilities.Upgrades = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil || p.Command.Id != id {
			t.Fatal("command not dispatched")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, state api.ExecutionState, obs *deployment.Observation, status *upgrade.Status) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: state, Steps: []api.ExecutionStep{}}
		if obs != nil {
			raw, _ := json.Marshal(obs)
			r.Observation = (*json.RawMessage)(&raw)
		}
		if status != nil {
			raw, _ := json.Marshal(status)
			r.Upgrade = (*json.RawMessage)(&raw)
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	var deploymentPlan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &deploymentPlan)
	report(poll(deploymentPlan.OperationID), "succeeded", &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)}, nil)
	b.call("GET", path+"/deployment-plans/"+deploymentPlan.ID.String(), nil, 200, &deploymentPlan)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+deploymentPlan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	report(poll(op.Id), "succeeded", &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)}, nil)
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Upgrade storage", "kind": "filesystem", "root": "/srv/backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &dest)
	password := "upgrade-repository-canary"
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Recovery", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var view upgrade.View
	b.call("POST", path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: pg, BaseRevisionId: draft.Id, RetainDays: 30}, 201, &view)
	wrong := prefix + "/projects/" + uuid.NewString() + "/environments/" + env.Id.String() + "/upgrades"
	b.call("GET", wrong, nil, 404, nil)
	actionPath := path + "/upgrades/" + view.Plan.ID.String() + "/actions"
	prepare := api.UpgradeActionRequest{Action: "prepare", RequestKey: uuid.New(), ExpectedPhase: "planned"}
	b.call("POST", actionPath, prepare, 202, &op)
	var again api.Operation
	b.call("POST", actionPath, prepare, 202, &again)
	if op.Id != again.Id {
		t.Fatal("upgrade retry created duplicate work")
	}
	c := poll(op.Id)
	if c.Upgrade == nil || !strings.Contains(string(*c.Upgrade), password) || !strings.Contains(string(*c.Upgrade), "upgrade-database-canary") {
		t.Fatal("assigned agent did not receive required credentials")
	}
	var stored []byte
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", op.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(stored), "canary") {
		t.Fatal("credentials persisted in command")
	}
	b.call("POST", path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: pg, BaseRevisionId: draft.Id, RetainDays: 30}, 409, nil)
	b.call("POST", actionPath, api.UpgradeActionRequest{Action: "cutover", RequestKey: uuid.New(), ExpectedPhase: "running"}, 409, nil)
	status := upgrade.Status{Phase: "ready", Serving: "none", Step: "verified", KeepUntil: time.Now().Add(30 * 24 * time.Hour), Snapshot: strings.Repeat("c", 64)}
	report(c, "succeeded", nil, &status)
	b.call("POST", path+"/deployment-plans/"+deploymentPlan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 409, nil)
	b.call("POST", actionPath, api.UpgradeActionRequest{Action: "cutover", RequestKey: uuid.New(), ExpectedPhase: "ready"}, 202, &op)
	c = poll(op.Id)
	status.Phase = "attention"
	status.Step = "database"
	status.Serving = "unknown"
	status.WritesPossible = true
	report(c, "failed", nil, &status)
	b.call("POST", actionPath, api.UpgradeActionRequest{Action: "return", RequestKey: uuid.New(), ExpectedPhase: "attention"}, 409, nil)
	b.call("POST", actionPath, api.UpgradeActionRequest{Action: "reconcile", RequestKey: uuid.New(), ExpectedPhase: "attention"}, 202, &op)
	status.Phase = "complete"
	status.Step = "complete"
	status.Serving = "new"
	completed := poll(op.Id)
	rawStatus, _ := json.Marshal(status)
	emptyObservation, _ := json.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("d", 64)})
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: completed.Id, Generation: completed.Generation, PlanHash: completed.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Upgrade: (*json.RawMessage)(&rawStatus), Observation: (*json.RawMessage)(&emptyObservation)}, 400, nil)
	report(completed, "succeeded", &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: domain.PostgresImage}}, StateHash: strings.Repeat("d", 64)}, &status)
	b.call("GET", path+"/draft", nil, 200, &draft)
	var final domain.Specification
	if json.Unmarshal(draft.Specification, &final) != nil || draft.Id != view.Plan.TargetRevisionID || final.Applications[0].Mounts[0].VolumeID != view.Plan.TargetVolumeID {
		t.Fatal("verified target was not applied")
	}

	var refreshed deployment.Status
	b.call("GET", path+"/deployment-plans/"+view.Plan.TargetPlanID.String(), nil, 200, &refreshed)
	if refreshed.Plan == nil || refreshed.Plan.RevisionID != view.Plan.TargetRevisionID {
		t.Fatal("completed upgrade plan is not readable")
	}
	b.call("POST", path+"/runtime/refresh", nil, 201, &op)
	observedCommand := poll(op.Id)
	var observationPlan deployment.Payload
	if observedCommand.Visual == nil || json.Unmarshal(*observedCommand.Visual, &observationPlan) != nil || observationPlan.Plan.ID != view.Plan.TargetPlanID {
		t.Fatal("runtime refresh selected stale pre-upgrade plan")
	}
	report(observedCommand, "succeeded", &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: domain.PostgresImage}}, StateHash: strings.Repeat("e", 64)}, nil)
	raw := b.call("GET", path+"/upgrades", nil, 200, nil)
	if strings.Contains(string(raw), "canary") {
		t.Fatal("history exposed credentials")
	}
}
