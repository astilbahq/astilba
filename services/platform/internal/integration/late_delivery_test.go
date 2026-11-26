package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
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

// laggingNote is how the agent ends a command that management delivered after its deadline while
// the host's clock said it was still timely.
const laggingNote = "The admission deadline passed before execution by management's clock. This host's clock is behind management's."

// lateDeliveryHost is a deployment server with one PostgreSQL database deployed on it, whose agent
// a test polls by hand until it runs the real agent.
type lateDeliveryHost struct {
	f      *fixture
	b      *browser
	ws     uuid.UUID
	prefix string
	path   string
	server uuid.UUID
	in     api.AgentEnrollment
	token  string
	secret string
	pg     uuid.UUID
	draft  api.Revision
}

func newLateDeliveryHost(t *testing.T, secret string) *lateDeliveryHost {
	t.Helper()
	h := &lateDeliveryHost{f: setup(t), secret: secret, pg: uuid.New()}
	h.b = h.f.browser(t)
	h.ws = h.b.login("owner@example.test", h.f.password).Workspaces[0].Id
	h.prefix = "/api/v1/workspaces/" + h.ws.String()
	var project api.Project
	h.b.call("POST", h.prefix+"/projects", map[string]string{"name": "Late"}, 201, &project)
	var env api.Environment
	h.b.call("POST", h.prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Database"}, 201, &env)
	h.path = h.prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var version api.SecretVersion
	h.b.call("POST", h.path+"/secrets", map[string]string{"name": "Database password", "value": secret}, 201, &version)
	vol := uuid.New()
	image := domain.PostgresImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: h.pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Username: "app", Database: "app", PasswordSecretVersionID: version.Id, Roles: domain.PostgresOwner}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: "/var/lib/postgresql"}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	h.b.call("PUT", h.path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &h.draft)
	enrolled, in, token := enrollment(t, h.f, h.b, h.ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Backups = &yes
	in.Capabilities.ExpiredCommands = &yes
	in.Capabilities.PostgresOwnerRole = agentPostgresOwnerRole()
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	h.server, h.in, h.token = enrolled.Server.Id, in, token
	return h
}

func (h *lateDeliveryHost) poll(t *testing.T, id uuid.UUID) api.ExecutionCommand {
	t.Helper()
	var p api.AgentPoll
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &p)
	if p.Command == nil || p.Command.Id != id {
		t.Fatal("command not dispatched")
	}
	return *p.Command
}

func (h *lateDeliveryHost) succeed(t *testing.T, c api.ExecutionCommand, obs deployment.Observation) {
	t.Helper()
	raw, _ := stablejson.Marshal(obs)
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&raw)}, 204, nil)
}

// plan resolves a deployment plan for the draft, reporting the resolution by hand.
func (h *lateDeliveryHost) plan(t *testing.T) deployment.Status {
	t.Helper()
	var plan deployment.Status
	h.b.call("POST", h.path+"/deployment-plans", api.VisualPlanRequest{ServerId: h.server, BaseRevisionId: h.draft.Id}, 201, &plan)
	h.succeed(t, h.poll(t, plan.OperationID), deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{h.pg.String(): domain.PostgresImage}, StateHash: strings.Repeat("a", 64)})
	h.b.call("GET", h.path+"/deployment-plans/"+plan.ID.String(), nil, 200, &plan)
	return plan
}

func (h *lateDeliveryHost) apply(t *testing.T, plan deployment.Status) api.Operation {
	t.Helper()
	var op api.Operation
	h.b.call("POST", h.path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	return op
}

// releases counts the values management released for an operation.
func (h *lateDeliveryHost) releases(t *testing.T, id uuid.UUID) int {
	t.Helper()
	var n int
	if err := h.f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM secret_access WHERE operation_id=$1 AND purpose='deliver'", id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// late checks that a command management delivers after its deadline says so and comes without
// values, and that a real agent whose clock lags by an hour ends it unrun, as management's clock
// says, without touching the host.
func (h *lateDeliveryHost) late(t *testing.T, id uuid.UUID, values []string, engine func(*agent.Engine)) {
	t.Helper()
	ctx := context.Background()
	expireCommand(t, ctx, h.f, id)
	c := h.poll(t, id)
	raw, err := stablejson.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range values {
		if strings.Contains(string(raw), v) {
			t.Fatal("a command delivered after its deadline carried a value")
		}
	}
	if c.Expired == nil || !*c.Expired || c.CancelRequested || c.SecretRelease != nil {
		t.Fatalf("a command delivered after its deadline was not marked expired, or claimed a release: %s", raw)
	}

	journal, err := agent.Open(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err = h.f.st.Pool.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1", h.server).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: h.f.server.URL, ControllerGeneration: generation, ServerID: h.server, Credential: *h.in.Credential, Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	e := &agent.Engine{Journal: journal, Docker: docker}
	if engine != nil {
		engine(e)
	}
	runCtx, cancel := context.WithCancel(ctx)
	host := agent.NewClient(journal, e)
	host.HTTP = &http.Client{Transport: laggingClock{base: h.f.server.Client().Transport, lag: time.Hour}}
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: h.in.Capabilities, Preflight: h.in.Preflight}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- host.Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = docker.Close()
		_ = journal.Close()
	})
	for end := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if r, err := journal.Record(id); err == nil && r.Acknowledged {
			break
		}
		if time.Now().After(end) {
			r, _ := journal.Record(id)
			t.Fatalf("the agent's outcome was never acknowledged: %s %q", r.Report.State, r.Report.Note)
		}
	}
	var state, note string
	if err = h.f.st.Pool.QueryRow(ctx, "SELECT state,note FROM execution_operations WHERE id=$1", id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || note != laggingNote {
		t.Fatalf("a lagging agent ran a command management delivered after its deadline: %s %q", state, note)
	}
	r, err := journal.Record(id)
	if err != nil {
		t.Fatal(err)
	}
	if r.Command.Expired == nil || !*r.Command.Expired {
		t.Fatal("the journal did not keep management's verdict")
	}
}

// PLAT-138. A backup command delivered after its deadline, as to an agent that was silent, comes
// marked expired and without its repository access. An agent whose clock lags an hour, and so
// thinks it timely, ends it cancelled as management's clock says, and never runs restic.
func TestExpiredBackupCommandReachesALaggingAgentCancelled(t *testing.T) {
	h := newLateDeliveryHost(t, "database-password-late-canary")
	plan := h.plan(t)
	op := h.apply(t, plan)
	h.succeed(t, h.poll(t, op.Id), deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: h.pg, Kind: "service", State: "healthy", Image: domain.PostgresImage}}, StateHash: strings.Repeat("b", 64)})

	secretKey, password := "storage-secret-late-canary", "repository-password-late-canary"
	var dest backup.Destination
	h.b.call("POST", h.prefix+"/backup-destinations", map[string]any{"name": "Bucket", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "late-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key-id", "secretKey": secretKey, "password": ""}}, 201, &dest)
	var policy backup.Policy
	h.b.call("POST", h.path+"/backup-policies", api.BackupPolicyRequest{Name: "Daily backup", ResourceId: h.pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var check api.Operation
	h.b.call("POST", h.path+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}, 202, &check)

	// restic records every call it gets, and answers none.
	calls := filepath.Join(t.TempDir(), "calls")
	restic := filepath.Join(t.TempDir(), "restic")
	if err := os.WriteFile(restic, []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> '"+calls+"'\nexit 5\n"), 0700); err != nil {
		t.Fatal(err)
	}
	h.late(t, check.Id, []string{password, secretKey}, func(e *agent.Engine) { e.ResticCLI = restic })
	if _, err := os.Stat(calls); !os.IsNotExist(err) {
		raw, _ := os.ReadFile(calls)
		t.Fatalf("the agent ran restic for an expired backup command: %s", raw)
	}
	if n := h.releases(t, check.Id); n != 0 {
		t.Fatal("management released an expired backup command's access", n)
	}
}

// PLAT-138. A deployment delivered after its deadline comes marked expired and without its
// secrets, and an agent whose clock lags ends it cancelled, unrun.
func TestLateDeploymentCarriesNoSecrets(t *testing.T) {
	h := newLateDeliveryHost(t, "deployment-secret-late-canary")
	op := h.apply(t, h.plan(t))
	h.late(t, op.Id, []string{h.secret}, nil)
	if n := h.releases(t, op.Id); n != 0 {
		t.Fatal("management released an expired deployment's secrets", n)
	}
}

// PLAT-138. A cancelled deployment comes as the same command without its secrets, to an agent that
// doesn't advertise taking credential-free cancellation.
func TestCancelledDeploymentCarriesNoSecrets(t *testing.T) {
	h := newLateDeliveryHost(t, "deployment-secret-cancel-canary")
	op := h.apply(t, h.plan(t))
	if c := h.poll(t, op.Id); !strings.Contains(string(*c.Visual), h.secret) {
		t.Fatal("a timely deployment came without its secrets")
	}
	h.b.call("POST", h.prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
	c := h.poll(t, op.Id)
	var payload deployment.Payload
	if !c.CancelRequested || c.Expired != nil || c.SecretRelease != nil || strings.Contains(string(*c.Visual), h.secret) || json.Unmarshal(*c.Visual, &payload) != nil || payload.Digest() != c.PlanHash {
		t.Fatal("a cancelled deployment was not delivered as the same command without its secrets")
	}
}

// An agent that doesn't say it reads the expired mark rejects a response that carries it, so a late
// deployment reaches it unmarked, and still without its secrets.
func TestLateDeploymentToAnAgentWithoutTheMark(t *testing.T) {
	h := newLateDeliveryHost(t, "deployment-secret-unmarked-canary")
	h.in.Capabilities.ExpiredCommands = nil
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/heartbeat", h.token, api.AgentHeartbeat{Capabilities: h.in.Capabilities, Preflight: h.in.Preflight}, 200, nil)
	op := h.apply(t, h.plan(t))
	expireCommand(t, context.Background(), h.f, op.Id)
	c := h.poll(t, op.Id)
	if c.Expired != nil || c.SecretRelease != nil || strings.Contains(string(*c.Visual), h.secret) {
		t.Fatal("a late deployment to an agent that doesn't read the mark was marked, or carried its secrets")
	}
	if n := h.releases(t, op.Id); n != 0 {
		t.Fatal("management released a late deployment's secrets", n)
	}
}
