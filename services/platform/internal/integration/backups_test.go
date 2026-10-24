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
	"github.com/google/uuid"
)

func TestBackupScopeHydrationReportsAndCutover(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Recovery"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Database"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Database password", "value": "database-password-canary"}, 201, &secret)
	pg, vol := uuid.New(), uuid.New()
	image := domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Backups = &yes
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
	report := func(c api.ExecutionCommand, obs *deployment.Observation) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}
		if obs != nil {
			raw, _ := json.Marshal(obs)
			r.Observation = (*json.RawMessage)(&raw)
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	report(poll(plan.OperationID), &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)})
	b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	report(poll(op.Id), &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)})
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Recovery bucket", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "recovery-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key-canary", "secretKey": "storage-secret-canary", "password": ""}}, 201, &dest)
	password := "repository-password-canary"
	input := api.BackupPolicyRequest{Name: "Daily backup", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "Europe/London"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", input, 201, &policy)
	for _, route := range []string{prefix + "/backup-destinations", path + "/backup-policies"} {
		raw := b.call("GET", route, nil, 200, nil)
		if strings.Contains(string(raw), "canary") {
			t.Fatal("public response exposed credentials")
		}
	}
	commandInput := api.BackupCommandRequest{Action: "install", RequestKey: uuid.New(), Snapshot: ""}
	commandPath := path + "/backup-policies/" + policy.ID.String() + "/commands"
	b.call("POST", commandPath, commandInput, 202, &op)
	var again api.Operation
	b.call("POST", commandPath, commandInput, 202, &again)
	if op.Id != again.Id {
		t.Fatal("command replay duplicated operation")
	}
	c := poll(op.Id)
	if c.Backup == nil || !strings.Contains(string(*c.Backup), password) || !strings.Contains(string(*c.Backup), "storage-secret-canary") {
		t.Fatal("assigned backup agent did not receive repository credentials")
	}
	var persisted []byte
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", op.Id).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(persisted), "canary") {
		t.Fatal("plaintext access persisted in execution command")
	}
	report(c, nil)
	b.call("POST", commandPath, api.BackupCommandRequest{Action: "backup", RequestKey: uuid.New(), Snapshot: ""}, 202, &op)
	uploadedCommand := poll(op.Id)
	var upload backup.Command
	if uploadedCommand.Backup == nil || json.Unmarshal(*uploadedCommand.Backup, &upload) != nil || upload.TargetID != uuid.Nil {
		t.Fatal("an upload was assigned an uncreated restore target")
	}
	report(uploadedCommand, nil)
	status := backup.Status{PolicyID: policy.ID, Generation: 1, Next: time.Now().Add(time.Hour), Runs: []backup.Run{}}
	now := time.Now()
	target := uuid.New()
	run := backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, ScheduledAt: now, StartedAt: now, FinishedAt: &now, State: "succeeded", Snapshot: strings.Repeat("a", 64), Verified: false, TargetID: target, Manifest: &backup.Manifest{Roles: []backup.Role{}, Extensions: []string{}, Recipe: 1, PolicyID: policy.ID, EnvironmentID: env.Id, ResourceID: pg}}
	status.Runs = append(status.Runs, run)
	send := func(seq int64, want int) {
		raw, _ := json.Marshal(status)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/schedule-reports", token, map[string]any{"sequence": seq, "status": json.RawMessage(raw)}, want, nil)
	}
	send(1, 204)
	send(1, 204)
	var got backup.Status
	b.call("GET", path+"/backup-policies/"+policy.ID.String()+"/status", nil, 200, &got)
	if len(got.Runs) != 1 {
		t.Fatal("report retry duplicated a scheduled run")
	}
	cutover := path + "/backup-policies/" + policy.ID.String() + "/cutover"
	b.call("POST", cutover, api.BackupCutoverRequest{BaseRevisionId: draft.Id, RunId: run.ID}, 409, nil)
	status.Runs[0].Verified = true
	uploadRecord := status.Runs[0]
	uploadRecord.ID = upload.RunID
	uploadRecord.TargetID = uuid.Nil
	status.Runs = append(status.Runs, uploadRecord)
	send(2, 204)
	b.call("POST", cutover, api.BackupCutoverRequest{BaseRevisionId: draft.Id, RunId: upload.RunID}, 409, nil)
	var next api.Revision
	b.call("POST", cutover, api.BackupCutoverRequest{BaseRevisionId: draft.Id, RunId: run.ID}, 200, &next)
	var restored domain.Specification
	if json.Unmarshal(next.Specification, &restored) != nil || restored.Applications[0].Mounts[0].VolumeID != target {
		t.Fatal("cutover draft did not bind the fresh target")
	}
	var applied *uuid.UUID
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT applied_revision_id FROM environments WHERE id=$1", env.Id).Scan(&applied); err != nil || applied == nil || *applied != draft.Id {
		t.Fatal("preparing cutover applied it without review")
	}
	wrong := prefix + "/projects/" + uuid.NewString() + "/environments/" + env.Id.String() + "/backup-policies"
	b.call("GET", wrong, nil, 404, nil)
	foreign := policy.ID
	status.PolicyID = uuid.New()
	send(3, 404)
	status.PolicyID = foreign
	if err := f.st.Rewrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	b.call("POST", prefix+"/invitations", map[string]string{"email": "backup-viewer@example.test", "role": "viewer"}, 201, nil)
	invite := tokenFromMail(t, f.mail)
	viewer := f.browser(t)
	var session api.Session
	viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &session)
	viewer.csrf = session.CsrfToken
	viewer.call("POST", commandPath, api.BackupCommandRequest{Action: "backup", RequestKey: uuid.New(), Snapshot: ""}, 403, nil)
}
