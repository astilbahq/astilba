package integration

import (
	"context"
	"encoding/json/jsontext"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// fakeRestic stands in for restic. It answers a repository's config only when it is given the
// password and secret key the test expects, so a command that ran without its repository access
// fails.
func fakeRestic(t *testing.T, password, secretKey string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "restic")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' 'restic " + backup.ResticVersion + " fixture'; exit 0; fi\n" +
		"[ \"$(cat \"$RESTIC_PASSWORD_FILE\")\" = '" + password + "' ] || exit 3\n" +
		"[ \"$AWS_SECRET_ACCESS_KEY\" = '" + secretKey + "' ] || exit 4\n" +
		"case \"$*\" in *'cat config'*) printf '{\"id\":\"" + strings.Repeat("e", 64) + "\",\"version\":2}\\n'; exit 0;; esac\n" +
		"exit 5\n"
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

// PLAT-448. A backup command parked for attention keeps no repository access in the agent's
// journal. When the operator retries it, the agent asks management for the access again, as it does
// for a record it lost; management releases it again, through the broker, and the command runs with
// it and completes.
func TestParkedBackupGivesUpItsAccessAndARetryGetsItBack(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Parked"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Database"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var secret api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "Database password", "value": "database-password-value"}, 201, &secret)
	pg, vol := uuid.New(), uuid.New()
	image := domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: secret.Id, Roles: domain.PostgresOwner}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
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
	report := func(r api.AgentReport, obs *deployment.Observation) {
		t.Helper()
		if obs != nil {
			raw, _ := stablejson.Marshal(obs)
			r.Observation = (*jsontext.Value)(&raw)
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	succeeded := func(c api.ExecutionCommand) api.AgentReport {
		return api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id}, 201, &plan)
	resolve := poll(plan.OperationID)
	report(succeeded(resolve), &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)})
	b.call("GET", path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	report(succeeded(poll(op.Id)), &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)})

	secretKey, password := "storage-secret-parked-canary", "repository-password-parked-canary"
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Bucket", "kind": "s3", "serverId": enrolled.Server.Id, "root": "", "endpoint": "https://storage.example.test", "bucket": "parked-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key-id", "secretKey": secretKey, "password": ""}}, 201, &dest)
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Daily backup", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	b.call("POST", path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}, 202, &op)

	// The agent journals the check with its access, as delivered, and a gate parks it before it
	// starts (the gates themselves are covered in the agent's tests).
	journal, err := agent.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var generation int64
	if err = f.st.Pool.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1", enrolled.Server.Id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: f.server.URL, ControllerGeneration: generation, ServerID: enrolled.Server.Id, Credential: *in.Credential, Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	holds := func() bool {
		t.Helper()
		r, err := journal.Record(op.Id)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := stablejson.Marshal(r)
		if err != nil {
			t.Fatal(err)
		}
		return strings.Contains(string(raw), password) || strings.Contains(string(raw), secretKey)
	}
	check := poll(op.Id)
	if check.SecretRelease == nil || !check.SecretRelease.Values {
		t.Fatal("the check came without its access")
	}
	if _, err = journal.Accept(check); err != nil || !holds() {
		t.Fatal("the journal did not take the check's access", err)
	}
	gateNote := "An interrupted local schedule requires inspection and resolve-run before another host operation."
	if err = journal.Mutate(op.Id, func(r *agent.Record) { r.Report.State, r.Report.Note = "needs_attention", gateNote }); err != nil {
		t.Fatal(err)
	}
	parked, err := journal.Record(op.Id)
	if err != nil || holds() {
		t.Fatal("a parked check kept its repository access", err)
	}
	report(parked.Report, nil)
	operationState := func() string {
		t.Helper()
		var state string
		if err := f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	if state := operationState(); state != "needs_attention" {
		t.Fatal("management did not record the check as parked:", state)
	}
	releases := func() int {
		t.Helper()
		var n int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secret_access WHERE operation_id=$1 AND purpose='deliver' AND reason='redelivery'", op.Id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if releases() != 0 {
		t.Fatal("the access was released again before anything asked for it")
	}

	// The operator retries it, and the agent runs.
	if err = journal.Retry(op.Id); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(ctx)
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker, ResticCLI: fakeRestic(t, password, secretKey)})
	host.HTTP = &http.Client{Transport: f.server.Client().Transport}
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- host.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = docker.Close()
		_ = journal.Close()
	})
	for deadline := time.Now().Add(30 * time.Second); operationState() != "succeeded"; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			r, _ := journal.Record(op.Id)
			t.Fatalf("the retried check did not complete: management %s, agent %s %q", operationState(), r.Report.State, r.Report.Note)
		}
	}
	if releases() != 1 {
		t.Fatal("the retried check's access was not released again through the broker exactly once:", releases())
	}
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if r, err := journal.Record(op.Id); err == nil && r.Acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the completed check was never acknowledged")
		}
	}
	if holds() {
		t.Fatal("the finished check kept its repository access")
	}
}
