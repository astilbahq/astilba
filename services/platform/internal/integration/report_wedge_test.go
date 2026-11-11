package integration

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	"github.com/pressly/goose/v3"
)

// laggingClock shifts polled deadlines, which an agent cannot tell from its own
// clock running behind management's by the same amount.
type laggingClock struct {
	base http.RoundTripper
	lag  time.Duration
}

func (l laggingClock) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := l.base.RoundTrip(r)
	if err != nil || r.URL.Path != "/agent/v1/poll" {
		return response, err
	}
	var poll api.AgentPoll
	err = json.UnmarshalDecode(jsontext.NewDecoder(response.Body), &poll)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	if poll.Command != nil {
		poll.Command.Deadline = poll.Command.Deadline.Add(l.lag)
	}
	raw, err := stablejson.Marshal(poll)
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	response.ContentLength = int64(len(raw))
	return response, nil
}

type dispatchedBuild struct {
	op     api.Operation
	server uuid.UUID
	in     api.AgentEnrollment
}

// dispatchBuild leaves a build ready for its runner's first delivery.
func dispatchBuild(t *testing.T, ctx context.Context, f *fixture, arguments map[string]string, runtimeSecret string) dispatchedBuild {
	t.Helper()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			answerBranch(w, "gitea", strings.Repeat("a", 40))
			return
		}
		answerRepository(w, "gitea", 12, false)
	}))
	t.Cleanup(provider.Close)
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Delivery", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Delivery"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Build"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	config := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: arguments, Secrets: map[string]uuid.UUID{}}
	var op api.Operation
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": config}, 202, &op)
	if runtimeSecret != "" {
		b.call("POST", path+"/secrets", map[string]string{"name": "NOW_SECRET", "value": runtimeSecret}, 201, nil)
	}
	awaitDispatch(t, ctx, f, op.Id)
	return dispatchedBuild{op: op, server: enrolled.Server.Id, in: in}
}

func awaitDispatch(t *testing.T, ctx context.Context, f *fixture, id uuid.UUID) {
	t.Helper()
	state := ""
	for deadline := time.Now().Add(20 * time.Second); state != "dispatched"; time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("build did not become ready for delivery", state)
		}
		if err := f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", id).Scan(&state); err != nil {
			t.Fatal(err)
		}
	}
}

func expireCommand(t *testing.T, ctx context.Context, f *fixture, id uuid.UUID) {
	t.Helper()
	if _, err := f.st.Pool.Exec(ctx, "UPDATE execution_operations SET command=jsonb_set(command,'{deadline}',to_jsonb($2::text)) WHERE id=$1", id, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
}

// runAgent runs the real agent client until the returned function stops it.
func runAgent(t *testing.T, ctx context.Context, f *fixture, build dispatchedBuild, transport http.RoundTripper) (*agent.Journal, func()) {
	t.Helper()
	journal, err := agent.Open(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = journal.Close() })
	var generation int64
	if err = f.st.Pool.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1", build.server).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: f.server.URL, ControllerGeneration: generation, ServerID: build.server, Credential: *build.in.Credential, Enrolled: true, Role: "build"}); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = docker.Close() })
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker})
	host.HTTP = &http.Client{Transport: transport}
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: build.in.Capabilities, Preflight: build.in.Preflight}
	}
	stopped := make(chan error, 1)
	running, stop := context.WithCancel(ctx)
	go func() { stopped <- host.Run(running) }()
	var halting sync.Once
	halt := func() {
		halting.Do(func() {
			stop()
			<-stopped
		})
	}
	t.Cleanup(halt)
	return journal, halt
}

// A lapsed subscription must not send the real agent a build it would then have
// no accepted way to report, once a renewal lets input rejection fail that build.
func TestLapsedSubscriptionFinishesUndeliveredBuildOnController(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	canary := "lapsed-runtime-canary"
	build := dispatchBuild(t, ctx, f, map[string]string{"TOKEN": canary}, canary)
	var ws uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT workspace_id FROM execution_operations WHERE id=$1", build.op.Id).Scan(&ws); err != nil {
		t.Fatal(err)
	}
	f.st.Hosted = true
	if err := f.st.GrantManagement(ctx, ws, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()-interval '1 minute' WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	journal, halt := runAgent(t, ctx, f, build, f.server.Client().Transport)
	state, note := "", ""
	var sequence int64
	for deadline := time.Now().Add(30 * time.Second); state != "cancelled"; time.Sleep(100 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("build did not finish: controller %s sequence %d %q", state, sequence, note)
		}
		if err := f.st.Pool.QueryRow(ctx, "SELECT state,note,report_sequence FROM execution_operations WHERE id=$1", build.op.Id).Scan(&state, &note, &sequence); err != nil {
			t.Fatal(err)
		}
	}
	halt()
	if sequence != 0 {
		t.Fatal("an agent reported a build that should not have reached it", sequence, note)
	}
	if _, err := journal.Record(build.op.Id); err == nil {
		t.Fatal("undelivered build reached the agent journal")
	}
}

// A run left unfinished by a controller that did not record cancelled deliveries may
// be journaled by its agent, so finishing it here would reject that agent's reports.
func TestMigrationTreatsUnfinishedRunsAsPossiblyDelivered(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := dispatchBuild(t, ctx, f, map[string]string{}, "")
	conn, err := sql.Open("pgx", f.st.Pool.Config().ConnString())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	provider, err := goose.NewProvider(goose.DialectPostgres, conn, os.DirFS("../../db/migrations"), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ApplyVersion(ctx, 24, false); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.ApplyVersion(ctx, 24, true); err != nil {
		t.Fatal(err)
	}
	var started *bool
	if err = f.st.Pool.QueryRow(ctx, "SELECT delivery_started FROM pipeline_runs WHERE id=$1", build.op.Id).Scan(&started); err != nil {
		t.Fatal(err)
	}
	if started != nil {
		t.Fatal("unfinished run kept its proof of non-delivery", *started)
	}
}

// An expired command carries no credentials, and an agent with a lagging clock that
// started one could dereference them. Admission's own expiry note would mean the
// agent's clock stopped it rather than the controller.
func TestExpiredCommandStopsAgentWithLaggingClock(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	build := dispatchBuild(t, ctx, f, map[string]string{}, "")
	// Unknown delivery history is what still reaches a runner after expiry.
	if _, err := f.st.Pool.Exec(ctx, "UPDATE pipeline_runs SET delivery_started=NULL WHERE id=$1", build.op.Id); err != nil {
		t.Fatal(err)
	}
	expireCommand(t, ctx, f, build.op.Id)
	journal, _ := runAgent(t, ctx, f, build, laggingClock{base: f.server.Client().Transport, lag: time.Hour})
	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		record, err := journal.Record(build.op.Id)
		if err == nil && record.Acknowledged {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("agent's report was never acknowledged")
		}
	}
	var state, note string
	if err := f.st.Pool.QueryRow(ctx, "SELECT state,note FROM execution_operations WHERE id=$1", build.op.Id).Scan(&state, &note); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" || note != "Build/source assignment was cancelled or expired." {
		t.Fatalf("agent ran an expired command: %s %q", state, note)
	}
}

// PostgreSQL cannot store NUL, but build and container output can contain it.
func TestReportsAcceptHostOutputContainingNUL(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Output"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	yes := true
	in.Capabilities.Visual = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(20 * time.Millisecond) {
			var p api.AgentPoll
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
			if p.Command != nil && p.Command.Id == id {
				return *p.Command
			}
		}
		t.Fatal("operation not dispatched")
		return api.ExecutionCommand{}
	}
	var plan deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{ServerId: enrolled.Server.Id, BaseRevisionId: draft.Id, AllowHostChanges: true}, 201, &plan)
	c := poll(plan.OperationID)
	resolved, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): image}, StateHash: strings.Repeat("a", 64)})
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&resolved)}, 204, nil)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	c = poll(op.Id)
	// The escaped backslash is literal text, not an escaped NUL.
	observed, _ := stablejson.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "application", State: "failed", Logs: "boot\x00log \\u0000"}}, StateHash: strings.Repeat("b", 64)})
	// Encoded host output can pass the contract's character limit and exceed its byte limit.
	long := "compose\x00failed" + strings.Repeat("\ufffd", 400)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "failed", Note: long, Steps: []api.ExecutionStep{}, Observation: (*jsontext.Value)(&observed)}, 204, nil)
	var note, logs string
	if err := f.st.Pool.QueryRow(ctx, "SELECT o.note,r.observation->'resources'->0->>'logs' FROM execution_operations o JOIN visual_runtime r ON r.environment_id=o.environment_id WHERE o.id=$1", op.Id).Scan(&note, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "compose failed\ufffd") || len(note) > 1024 || !utf8.ValidString(note) || logs != "boot log \\u0000" {
		t.Fatalf("stored note %q logs %q", note, logs)
	}
}
