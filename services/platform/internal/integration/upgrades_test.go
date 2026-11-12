package integration

import (
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/pquerna/otp/totp"
	"github.com/pressly/goose/v3"
)

// upgradeHost is a deployment agent with an applied PostgreSQL deployment, ready to upgrade.
type upgradeHost struct {
	f              *fixture
	b              *browser
	ws, env, pg    uuid.UUID
	prefix, path   string
	secret         api.SecretVersion
	draft          api.Revision
	deploymentPlan deployment.Status
	in             api.AgentEnrollment
	token          string
}

func newUpgradeHost(t *testing.T, f *fixture) *upgradeHost {
	t.Helper()
	h := &upgradeHost{f: f, b: f.browser(t)}
	b := h.b
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Upgrade"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "PostgreSQL"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	b.call("POST", path+"/secrets", map[string]string{"name": "Database", "value": "upgrade-database-canary"}, 201, &h.secret)
	pg, vol := uuid.New(), uuid.New()
	image := domain.Postgres17Image
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: h.secret.Id}, Mounts: []domain.VolumeMount{{VolumeID: vol, Target: domain.PostgresMount(image)}}}}, Volumes: []domain.Volume{{ID: vol, Name: "Data"}}}
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &h.draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	in.Capabilities.Backups = &yes
	in.Capabilities.Upgrades = &yes
	in.Capabilities.Schedules = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	h.ws, h.env, h.pg, h.prefix, h.path, h.in, h.token = ws, env.Id, pg, prefix, path, in, token
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: h.draft.Id}, 201, &h.deploymentPlan)
	h.report(t, h.poll(t, h.deploymentPlan.OperationID), "succeeded", &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{pg.String(): image}, StateHash: strings.Repeat("a", 64)}, nil)
	b.call("GET", path+"/deployment-plans/"+h.deploymentPlan.ID.String(), nil, 200, &h.deploymentPlan)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+h.deploymentPlan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 201, &op)
	h.report(t, h.poll(t, op.Id), "succeeded", &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: pg, Kind: "service", State: "healthy", Image: image}}, StateHash: strings.Repeat("b", 64)}, nil)
	return h
}
func (h *upgradeHost) poll(t *testing.T, id uuid.UUID) api.ExecutionCommand {
	t.Helper()
	var p api.AgentPoll
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/poll", h.token, nil, 200, &p)
	if p.Command == nil || p.Command.Id != id {
		t.Fatal("command not dispatched")
	}
	return *p.Command
}
func (h *upgradeHost) report(t *testing.T, c api.ExecutionCommand, state api.ExecutionState, obs *deployment.Observation, status *upgrade.Status) {
	t.Helper()
	r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: state, Steps: []api.ExecutionStep{}}
	if obs != nil {
		raw, _ := stablejson.Marshal(obs)
		r.Observation = (*jsontext.Value)(&raw)
	}
	if status != nil {
		raw, _ := stablejson.Marshal(status)
		r.Upgrade = (*jsontext.Value)(&raw)
	}
	agentRequest(t, h.f.server.Client(), h.f.server.URL, "/agent/v1/report", h.token, r, 204, nil)
}

func TestUpgradeApprovalRecoveryAndTenantBoundary(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	b, pg, prefix, path, secret, draft, deploymentPlan, in, token := h.b, h.pg, h.prefix, h.path, h.secret, h.draft, h.deploymentPlan, h.in, h.token
	yes := true
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		return h.poll(t, id)
	}
	report := func(c api.ExecutionCommand, state api.ExecutionState, obs *deployment.Observation, status *upgrade.Status) {
		t.Helper()
		h.report(t, c, state, obs, status)
	}
	var op api.Operation
	var dest backup.Destination
	b.call("POST", prefix+"/backup-destinations", map[string]any{"name": "Upgrade storage", "kind": "filesystem", "root": "/srv/backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &dest)
	password := "upgrade-repository-canary"
	cron, interval := api.Cron, api.Interval
	expression, minutes, timeout := "*/15 * * * *", 15, 60
	for _, test := range []struct {
		name     string
		schedule api.DailySchedule
		timeout  *int
	}{
		{"cron", api.DailySchedule{Kind: &cron, Expression: &expression, Timezone: "Europe/London"}, nil},
		{"interval", api.DailySchedule{Kind: &interval, IntervalMinutes: &minutes, Timezone: "UTC"}, nil},
		{"custom timeout", api.DailySchedule{Timezone: "UTC"}, &timeout},
	} {
		t.Log("upgrade delivery with", test.name)
		var policy backup.Policy
		b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: test.name, ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: test.schedule, TimeoutSeconds: test.timeout, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
		var view upgrade.View
		b.call("POST", path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: pg, BaseRevisionId: draft.Id, RetainDays: 30}, 201, &view)
		b.call("POST", path+"/upgrades/"+view.Plan.ID.String()+"/actions", api.UpgradeActionRequest{Action: "prepare", RequestKey: uuid.New(), ExpectedPhase: "planned"}, 202, &op)
		if test.name == "cron" {
			in.Capabilities.Schedules = nil
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 400, nil)
			in.Capabilities.Schedules = &yes
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
		}
		command := poll(op.Id)
		var hydrated upgrade.Command
		if command.Kind != "upgrade" || command.ResourceId != view.Plan.ID || command.Backup != nil || command.Upgrade == nil || json.Unmarshal(*command.Upgrade, &hydrated) != nil || hydrated.Digest() != command.PlanHash {
			t.Fatal("backup credential hydration changed the upgrade command identity")
		}
		if hydrated.Installation.Policy.Schedule != policy.Schedule || hydrated.Installation.Policy.TimeoutSeconds != policy.TimeoutSeconds || hydrated.Installation.Access.Password != password || hydrated.Secrets[secret.Id.String()] != "upgrade-database-canary" {
			t.Fatal("upgrade delivery lost policy settings or assigned credentials")
		}
		var persisted []byte
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", command.Id).Scan(&persisted); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(persisted), "canary") {
			t.Fatal("hydrated upgrade credentials were persisted")
		}
		report(command, "failed", nil, &upgrade.Status{Phase: "rejected", Serving: "old", KeepUntil: time.Now().Add(30 * 24 * time.Hour)})
	}
	in.Capabilities.Schedules = nil
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	var policy backup.Policy
	b.call("POST", path+"/backup-policies", api.BackupPolicyRequest{Name: "Recovery", ResourceId: pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var view upgrade.View
	b.call("POST", path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: pg, BaseRevisionId: draft.Id, RetainDays: 30}, 201, &view)
	wrong := prefix + "/projects/" + uuid.NewString() + "/environments/" + h.env.String() + "/upgrades"
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
	b.call("POST", path+"/deployment-plans/"+deploymentPlan.ID.String()+"/apply", api.ApplyVisualPlan{IdempotencyKey: uuid.New(), ApproveHostChanges: approved()}, 409, nil)
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
	rawStatus, _ := stablejson.Marshal(status)
	emptyObservation, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, StateHash: strings.Repeat("d", 64)})
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: completed.Id, Generation: completed.Generation, PlanHash: completed.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Upgrade: (*jsontext.Value)(&rawStatus), Observation: (*jsontext.Value)(&emptyObservation)}, 400, nil)
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

func (h *upgradeHost) plan(t *testing.T) (upgrade.View, string) {
	t.Helper()
	password := "upgrade-repository-canary"
	var dest backup.Destination
	h.b.call("POST", h.prefix+"/backup-destinations", map[string]any{"name": "Refusal storage " + uuid.NewString(), "kind": "filesystem", "root": "/srv/backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}, 201, &dest)
	var policy backup.Policy
	h.b.call("POST", h.path+"/backup-policies", api.BackupPolicyRequest{Name: "Refusal " + uuid.NewString(), ResourceId: h.pg, DestinationId: dest.ID, Method: "postgres-logical", Schedule: api.DailySchedule{Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}, 201, &policy)
	var view upgrade.View
	h.b.call("POST", h.path+"/upgrades", api.UpgradeRequest{PolicyId: policy.ID, ResourceId: h.pg, BaseRevisionId: h.draft.Id, RetainDays: 30}, 201, &view)
	return view, h.path + "/upgrades/" + view.Plan.ID.String() + "/actions"
}
func (h *upgradeHost) action(t *testing.T, actions string, action api.UpgradeActionRequestAction, phase string) uuid.UUID {
	t.Helper()
	var op api.Operation
	h.b.call("POST", actions, api.UpgradeActionRequest{Action: action, RequestKey: uuid.New(), ExpectedPhase: phase}, 202, &op)
	return op.Id
}
func (h *upgradeHost) status(t *testing.T, plan uuid.UUID) upgrade.Status {
	t.Helper()
	var raw []byte
	if err := h.f.st.Pool.QueryRow(context.Background(), "SELECT status FROM supported_upgrades WHERE id=$1", plan).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var status upgrade.Status
	if err := json.Unmarshal(raw, &status); err != nil {
		t.Fatal(err)
	}
	return status
}
func (h *upgradeHost) ready(t *testing.T, actions string) upgrade.Status {
	t.Helper()
	status := upgrade.Status{Phase: "ready", Serving: "none", Step: "verified", KeepUntil: time.Now().Add(30 * 24 * time.Hour), Snapshot: strings.Repeat("c", 64)}
	h.report(t, h.poll(t, h.action(t, actions, "prepare", "planned")), "succeeded", nil, &status)
	return status
}

// lapseOnNextQueue ends the subscription in the transaction that queues the next
// upgrade action, so the worker always finds a queued command it may not dispatch.
func (h *upgradeHost) lapseOnNextQueue(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	var enrollment api.MFAEnrollment
	h.b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var codes api.RecoveryCodes
	h.b.call("POST", "/api/v1/auth/mfa/confirm", map[string]any{"code": code, "current": map[string]string{"password": h.f.password}}, 200, &codes)
	h.b.csrf = codes.Session.CsrfToken
	if _, err = h.f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=now() WHERE id=$1", h.f.owner); err != nil {
		t.Fatal(err)
	}
	h.f.st.Hosted = true
	if err = h.f.st.GrantManagement(ctx, h.ws, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err = h.f.st.Pool.Exec(ctx, `CREATE OR REPLACE FUNCTION lapse_on_upgrade_queue() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			UPDATE hosted_subscriptions SET management_until=now()-interval '1 minute' WHERE workspace_id=NEW.workspace_id;
			RETURN NEW;
		END;
		$$;
		DROP TRIGGER IF EXISTS lapse_on_upgrade_queue ON supported_upgrades;
		CREATE TRIGGER lapse_on_upgrade_queue AFTER UPDATE OF status ON supported_upgrades
		FOR EACH ROW WHEN (NEW.status->>'phase'='running') EXECUTE FUNCTION lapse_on_upgrade_queue()`); err != nil {
		t.Fatal(err)
	}
}
func (h *upgradeHost) renew(t *testing.T) {
	t.Helper()
	if _, err := h.f.st.Pool.Exec(context.Background(), `DROP TRIGGER lapse_on_upgrade_queue ON supported_upgrades; UPDATE hosted_subscriptions SET management_until=now()+interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
}
func (h *upgradeHost) awaitCancelled(t *testing.T, op uuid.UUID) {
	t.Helper()
	state := ""
	for deadline := time.Now().Add(20 * time.Second); state != "cancelled"; time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("queued upgrade was not cancelled by the lapse", state)
		}
		if err := h.f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op).Scan(&state); err != nil {
			t.Fatal(err)
		}
	}
}

// startAgent runs the real client and journal with no state for any upgrade plan.
func (h *upgradeHost) startAgent(t *testing.T) (*agent.Journal, *reportStatuses) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	journal, err := agent.Open(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	var generation int64
	if err = h.f.st.Pool.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1", h.in.ServerId).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: h.f.server.URL, ControllerGeneration: generation, ServerID: h.in.ServerId, Credential: *h.in.Credential, Enrolled: true, Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	statuses := &reportStatuses{base: h.f.server.Client().Transport}
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker})
	host.HTTP = &http.Client{Transport: statuses}
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: h.in.Capabilities, Preflight: h.in.Preflight}
	}
	stopped := make(chan error, 1)
	go func() { stopped <- host.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-stopped
		_ = docker.Close()
		_ = journal.Close()
	})
	return journal, statuses
}

type reportStatuses struct {
	base http.RoundTripper
	mu   sync.Mutex
	seen []int
}

func (s *reportStatuses) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := s.base.RoundTrip(r)
	if err == nil && r.URL.Path == "/agent/v1/report" {
		s.mu.Lock()
		s.seen = append(s.seen, response.StatusCode)
		s.mu.Unlock()
	}
	return response, err
}
func (s *reportStatuses) statuses() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.seen...)
}
func awaitAcknowledged(t *testing.T, journal *agent.Journal, statuses *reportStatuses, op uuid.UUID) {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		if record, err := journal.Record(op); err == nil && record.Acknowledged {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("refusal never acknowledged, so the journal refuses new commands; report responses %v", statuses.statuses())
		}
	}
}

// The hosted route: a lapse cancels a queued prepare, and after renewal a plan left
// at running accepts a return that an agent with no state for it can only refuse.
func TestLapsedUpgradeCannotWedgeRenewedHost(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	view, actions := h.plan(t)
	h.lapseOnNextQueue(t)
	h.awaitCancelled(t, h.action(t, actions, "prepare", "planned"))
	h.renew(t)
	if status := h.status(t, view.Plan.ID); status.Phase != "rejected" || !strings.Contains(status.Note, "before") {
		t.Fatalf("undelivered prepare left the plan at %s: %q", status.Phase, status.Note)
	}
	h.b.call("POST", actions, api.UpgradeActionRequest{Action: "return", RequestKey: uuid.New(), ExpectedPhase: "rejected"}, 409, nil)

	// A plan stranded before this was fixed and outside its migration still reaches the agent.
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `UPDATE supported_upgrades SET status=jsonb_set(status,'{phase}','"running"') WHERE id=$1`, view.Plan.ID); err != nil {
		t.Fatal(err)
	}
	journal, statuses := h.startAgent(t)
	refused := h.action(t, actions, "return", "running")
	awaitAcknowledged(t, journal, statuses, refused)
	// The controller cannot prove nothing ran before the plan was stranded.
	if status := h.status(t, view.Plan.ID); status.Phase != "attention" || status.WritesPossible {
		t.Fatalf("refused return left %+v", status)
	}
	var note string
	if err := f.st.Pool.QueryRow(ctx, "SELECT note FROM execution_operations WHERE id=$1", refused).Scan(&note); err != nil || note != "Upgrade journal is unavailable." {
		t.Fatalf("operation does not say why: %q %v", note, err)
	}
	// The journal is free, so the next command runs and is acknowledged too.
	awaitAcknowledged(t, journal, statuses, h.action(t, actions, "return", "attention"))
}

// A lapse before dispatch leaves the host untouched, so the approval can be repeated.
func TestLapseBeforeCutoverKeepsPlanReady(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	view, actions := h.plan(t)
	ready := h.ready(t, actions)
	h.lapseOnNextQueue(t)
	h.awaitCancelled(t, h.action(t, actions, "cutover", "ready"))
	h.renew(t)
	status := h.status(t, view.Plan.ID)
	if status.Phase != "ready" || status.Snapshot != ready.Snapshot || status.WritesPossible {
		t.Fatalf("undelivered cutover left %+v", status)
	}
	h.poll(t, h.action(t, actions, "cutover", "ready"))
}

// Agents refuse commands without a status when they cannot read or trust the plan.
func TestUpgradeEndedWithoutStatusSettlesPlan(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	refuse := func(c api.ExecutionCommand, steps ...string) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "failed", Note: "Upgrade journal is unavailable.", Steps: []api.ExecutionStep{}}
		for _, step := range steps {
			r.Steps = append(r.Steps, api.ExecutionStep{Name: step, State: "intent"})
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", h.token, r, 204, nil)
	}
	expect := func(plan uuid.UUID, phase, serving string, writes bool) {
		t.Helper()
		if status := h.status(t, plan); status.Phase != phase || status.Serving != serving || status.WritesPossible != writes || !strings.Contains(status.Note, "operation's note") {
			t.Fatalf("want %s serving %s writes %v, got %+v", phase, serving, writes, status)
		}
	}
	returned := func(c api.ExecutionCommand) {
		t.Helper()
		h.report(t, c, "succeeded", &deployment.Observation{ObservedAt: time.Now(), Applied: true, Resources: []deployment.Resource{{ID: h.pg, Kind: "service", State: "healthy", Image: domain.Postgres17Image}}, StateHash: strings.Repeat("e", 64)}, &upgrade.Status{Phase: "returned", Serving: "old", Step: "complete", KeepUntil: time.Now().Add(30 * 24 * time.Hour)})
	}

	t.Log("a prepare refused before admission changed nothing and frees the server")
	view, actions := h.plan(t)
	c := h.poll(t, h.action(t, actions, "prepare", "planned"))
	claim := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", h.token, claim, 400, nil)
	refuse(c)
	expect(view.Plan.ID, "rejected", "old", false)
	var refreshed deployment.Status
	h.b.call("POST", h.path+"/deployment-plans", api.VisualPlanRequest{ServerId: h.in.ServerId, BaseRevisionId: h.draft.Id}, 201, &refreshed)
	h.report(t, h.poll(t, refreshed.OperationID), "failed", nil, nil)

	t.Log("a prepare that stopped after admission may have stopped writers")
	view, actions = h.plan(t)
	c = h.poll(t, h.action(t, actions, "prepare", "planned"))
	refuse(c, "upgrade-preflight")
	expect(view.Plan.ID, "attention", "unknown", false)
	h.b.call("POST", h.path+"/deployment-plans", api.VisualPlanRequest{ServerId: h.in.ServerId, BaseRevisionId: h.draft.Id}, 409, nil)
	returned(h.poll(t, h.action(t, actions, "return", "attention")))

	t.Log("a refused cutover keeps the prepared host and its way back")
	view, actions = h.plan(t)
	h.ready(t, actions)
	refuse(h.poll(t, h.action(t, actions, "cutover", "ready")))
	expect(view.Plan.ID, "attention", "none", false)
	h.b.call("POST", actions, api.UpgradeActionRequest{Action: "reconcile", RequestKey: uuid.New(), ExpectedPhase: "attention"}, 409, nil)
	returned(h.poll(t, h.action(t, actions, "return", "attention")))

	t.Log("only the agent can raise the write boundary, and a refusal keeps it")
	view, actions = h.plan(t)
	status := h.ready(t, actions)
	refuse(h.poll(t, h.action(t, actions, "cutover", "ready")), "upgrade-reconcile")
	expect(view.Plan.ID, "attention", "unknown", false)
	status.Phase, status.Serving, status.WritesPossible = "attention", "unknown", true
	h.report(t, h.poll(t, h.action(t, actions, "return", "attention")), "failed", nil, &status)
	h.b.call("POST", actions, api.UpgradeActionRequest{Action: "return", RequestKey: uuid.New(), ExpectedPhase: "attention"}, 409, nil)
	refuse(h.poll(t, h.action(t, actions, "reconcile", "attention")))
	expect(view.Plan.ID, "attention", "unknown", true)
	h.poll(t, h.action(t, actions, "reconcile", "attention"))
}

// Plans stranded before these were settled stop blocking their servers on upgrade.
func TestStrandedUpgradeMigration(t *testing.T) {
	f := setup(t)
	h := newUpgradeHost(t, f)
	view, actions := h.plan(t)
	h.lapseOnNextQueue(t)
	op := h.action(t, actions, "prepare", "planned")
	h.awaitCancelled(t, op)
	h.renew(t)
	ctx := context.Background()
	conn, err := sql.Open("pgx", f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, conn, os.DirFS("../../db/migrations"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	var upgradeCommand []byte
	if err = f.st.Pool.QueryRow(ctx, "SELECT command FROM execution_operations WHERE id=$1", op).Scan(&upgradeCommand); err != nil {
		t.Fatal(err)
	}
	const lapse = "Subscription expired before dispatch."
	const fenced = "Fenced after management recovery; inspect the host before resuming."
	const checkpoint = "Cancelled before host reservation or writer maintenance began."
	coordinated := `{"crosshost":{"maintenance":{"effect":{"upgrade":{"plan":{"id":"` + view.Plan.ID.String() + `"}}}}}}`
	action := func(name string) (command string) {
		t.Helper()
		if err := f.st.Pool.QueryRow(ctx, `SELECT jsonb_set($1::jsonb,'{upgrade,action}',to_jsonb($2::text))::text`, upgradeCommand, name).Scan(&command); err != nil {
			t.Fatal(err)
		}
		return command
	}
	for _, test := range []struct{ name, kind, command, note, parent, want string }{
		{"fenced may have been delivered", "upgrade", action("prepare"), fenced, "NULL", "running"},
		{"coordinated child stays with its parent", "upgrade", action("prepare"), lapse, "id", "running"},
		{"lapsed prepare", "upgrade", action("prepare"), lapse, "NULL", "rejected"},
		{"lapsed cutover", "upgrade", action("cutover"), lapse, "NULL", "ready"},
		{"lapsed return", "upgrade", action("return"), lapse, "NULL", "attention"},
		{"fenced coordinated prepare", "crosshost", coordinated, fenced, "NULL", "running"},
		{"coordinated prepare ended at its checkpoint", "crosshost", coordinated, checkpoint, "NULL", "rejected"},
	} {
		if _, err = f.st.Pool.Exec(ctx, `UPDATE supported_upgrades SET status=jsonb_set(status,'{phase}','"running"') WHERE id=$1`, view.Plan.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.Pool.Exec(ctx, `UPDATE execution_operations SET kind=$1,command=$2::jsonb,note=$3,parent_id=`+test.parent+` WHERE id=$4`, test.kind, test.command, test.note, op); err != nil {
			t.Fatal(test.name, err)
		}
		if _, err = provider.ApplyVersion(ctx, 25, false); err != nil {
			t.Fatal(err)
		}
		if _, err = provider.ApplyVersion(ctx, 25, true); err != nil {
			t.Fatal(err)
		}
		if phase := h.status(t, view.Plan.ID).Phase; phase != test.want {
			t.Fatalf("%s: phase %s, want %s", test.name, phase, test.want)
		}
	}
	if err = dbmigrate.Check(ctx, f.st.Pool.Config().ConnString()); err != nil {
		t.Fatal(err)
	}
}

// A prepare cancelled before its recovery checkpoint leaves no parent to recover
// through, so its plan must not keep blocking the server for the next plan.
func planAfterCancelledCoordinatedPrepare(t *testing.T, f *fixture, b *browser, refresh func(), wait func(func() bool), path string, plan api.UpgradeRequest) upgrade.View {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `CREATE FUNCTION cancel_upgrade_parent() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			NEW.cancel_requested := true;
			RETURN NEW;
		END;
		$$;
		CREATE TRIGGER cancel_upgrade_parent BEFORE INSERT ON execution_operations
		FOR EACH ROW WHEN (NEW.kind='crosshost' AND NEW.parent_id IS NULL) EXECUTE FUNCTION cancel_upgrade_parent()`); err != nil {
		t.Fatal(err)
	}
	var cancelled upgrade.View
	refresh()
	b.call("POST", path+"/upgrades", plan, 201, &cancelled)
	var parent api.Operation
	refresh()
	b.call("POST", path+"/upgrades/"+cancelled.Plan.ID.String()+"/actions", api.UpgradeActionRequest{Action: "prepare", ExpectedPhase: "planned", RequestKey: uuid.New()}, 202, &parent)
	wait(func() bool {
		var settled bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT o.state='cancelled' AND u.status->>'phase'='rejected' FROM execution_operations o, supported_upgrades u WHERE o.id=$1 AND u.id=$2`, parent.Id, cancelled.Plan.ID).Scan(&settled); err != nil {
			t.Fatal(err)
		}
		return settled
	})
	if _, err := f.st.Pool.Exec(ctx, `DROP TRIGGER cancel_upgrade_parent ON execution_operations`); err != nil {
		t.Fatal(err)
	}
	var view upgrade.View
	refresh()
	b.call("POST", path+"/upgrades", plan, 201, &view)
	return view
}

// A cutover cancelled before its command exists leaves the prepared host untouched,
// so the owner can approve it again.
func cancelCoordinatedCutover(t *testing.T, f *fixture, b *browser, refresh func(), wait func(func() bool), actions string, parent, plan uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	if _, err := f.st.Pool.Exec(ctx, `CREATE TRIGGER cancel_upgrade_approval BEFORE UPDATE ON execution_operations
		FOR EACH ROW WHEN (NEW.note='Owner-approved upgrade action: cutover') EXECUTE FUNCTION cancel_upgrade_parent()`); err != nil {
		t.Fatal(err)
	}
	refresh()
	b.call("POST", actions, api.UpgradeActionRequest{Action: "cutover", ExpectedPhase: "ready", RequestKey: uuid.New()}, 202, nil)
	wait(func() bool {
		var settled bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT h.phase='maintenance-owner' AND u.status->>'phase'='ready' FROM cross_host_operations h, supported_upgrades u WHERE h.id=$1 AND u.id=$2`, parent, plan).Scan(&settled); err != nil {
			t.Fatal(err)
		}
		return settled
	})
	if _, err := f.st.Pool.Exec(ctx, `DROP TRIGGER cancel_upgrade_approval ON execution_operations`); err != nil {
		t.Fatal(err)
	}
}
