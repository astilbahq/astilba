package integration

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// reportWindow holds the agent's reports until its poll after first delivery, the
// window between a poll and the next two-second report cycle.
type reportWindow struct {
	base      http.RoundTripper
	delivered func() error
	release   chan struct{}
	once      sync.Once
	mu        sync.Mutex
	seen      bool
	reports   []int
}

func (w *reportWindow) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Path == "/agent/v1/report" {
		select {
		case <-w.release:
		case <-r.Context().Done():
			return nil, r.Context().Err()
		}
		response, err := w.base.RoundTrip(r)
		if err == nil {
			w.mu.Lock()
			w.reports = append(w.reports, response.StatusCode)
			w.mu.Unlock()
		}
		return response, err
	}
	response, err := w.base.RoundTrip(r)
	if err != nil || r.URL.Path != "/agent/v1/poll" {
		return response, err
	}
	w.mu.Lock()
	seen := w.seen
	w.mu.Unlock()
	if seen {
		w.once.Do(func() { close(w.release) })
		return response, nil
	}
	raw, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(raw))
	var poll api.AgentPoll
	if json.Unmarshal(raw, &poll) == nil && poll.Command != nil {
		if err = w.delivered(); err != nil {
			return nil, err
		}
		w.mu.Lock()
		w.seen = true
		w.mu.Unlock()
	}
	return response, nil
}

func (w *reportWindow) statuses() []int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]int(nil), w.reports...)
}

// A subscription renewed just after a credential-free cancellation must not turn
// the journaled command's report into a permanent rejection on the real agent.
func TestRenewedSubscriptionCannotRejectCancelledBuildDelivery(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": false})
	}))
	defer provider.Close()
	var connection struct {
		Connection pipeline.Connection
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Renewal", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Renewal"}, 201, &project)
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

	canary := "renewal-runtime-canary"
	config := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{"TOKEN": canary}, Secrets: map[string]uuid.UUID{}}
	var op api.Operation
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": config}, 202, &op)
	// The public argument becomes a runtime secret, so hydration now rejects the build.
	b.call("POST", path+"/secrets", map[string]string{"name": "RENEWAL_TOKEN", "value": canary}, 201, nil)
	state := ""
	for deadline := time.Now().Add(20 * time.Second); state != "dispatched"; time.Sleep(25 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("build did not become ready for delivery", state)
		}
		if err := f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
	}
	f.st.Hosted = true
	if err := f.st.GrantManagement(ctx, ws, time.Now().Add(time.Hour), 100); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()-interval '1 minute' WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}

	journal, err := agent.Open(t.TempDir() + "/state")
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	var generation int64
	if err = f.st.Pool.QueryRow(ctx, "SELECT controller_generation FROM servers WHERE id=$1", enrolled.Server.Id).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	if err = journal.SetConfig(agent.Config{Origin: f.server.URL, ControllerGeneration: generation, ServerID: enrolled.Server.Id, Credential: in.Credential, Enrolled: true, Role: "build"}); err != nil {
		t.Fatal(err)
	}
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	window := &reportWindow{base: f.server.Client().Transport, release: make(chan struct{}), delivered: func() error {
		_, err := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()+interval '1 hour' WHERE workspace_id=$1", ws)
		return err
	}}
	host := agent.NewClient(journal, &agent.Engine{Journal: journal, Docker: docker})
	host.HTTP = &http.Client{Transport: window}
	host.Observe = func(context.Context) api.AgentHeartbeat {
		return api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}
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
	defer halt()

	for deadline := time.Now().Add(30 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		record, err := journal.Record(op.Id)
		if err == nil && record.Acknowledged {
			break
		}
		if time.Now().After(deadline) {
			pending, _ := journal.Pending()
			var note string
			var sequence int64
			if err = f.st.Pool.QueryRow(ctx, "SELECT state,note,report_sequence FROM execution_operations WHERE id=$1", op.Id).Scan(&state, &note, &sequence); err != nil {
				t.Fatal(err)
			}
			t.Fatalf("report never acknowledged: controller %s sequence %d %q; agent report responses %v; %d pending records", state, sequence, note, window.statuses(), len(pending))
		}
	}
	if err = f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != "cancelled" {
		t.Fatal("agent's cancellation was not recorded", state)
	}
	halt()
	// The journal gate that refuses work while a report awaits acknowledgement.
	next := api.ExecutionCommand{Id: uuid.New(), ServerId: enrolled.Server.Id, EnvironmentId: uuid.New(), ResourceId: uuid.New(), Kind: "test", Generation: 1, PlanHash: strings.Repeat("a", 64), Deadline: time.Now().Add(time.Hour)}
	if _, err = journal.Accept(next); err != nil {
		t.Fatal("agent refuses later commands", err)
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
	resolved, _ := json.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: map[string]string{app.String(): image}, StateHash: strings.Repeat("a", 64)})
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: (*json.RawMessage)(&resolved)}, 204, nil)
	var op api.Operation
	b.call("POST", path+"/deployment-plans/"+plan.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &op)
	c = poll(op.Id)
	// The escaped backslash is literal text, not an escaped NUL.
	observed, _ := json.Marshal(deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{{ID: app, Kind: "application", State: "failed", Logs: "boot\x00log \\u0000"}}, StateHash: strings.Repeat("b", 64)})
	// Encoded host output can pass the contract's character limit and exceed its byte limit.
	long := "compose\x00failed" + strings.Repeat("\ufffd", 400)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "failed", Note: long, Steps: []api.ExecutionStep{}, Observation: (*json.RawMessage)(&observed)}, 204, nil)
	var note, logs string
	if err := f.st.Pool.QueryRow(ctx, "SELECT o.note,r.observation->'resources'->0->>'logs' FROM execution_operations o JOIN visual_runtime r ON r.environment_id=o.environment_id WHERE o.id=$1", op.Id).Scan(&note, &logs); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(note, "compose failed\ufffd") || len(note) > 1024 || !utf8.ValidString(note) || logs != "boot log \\u0000" {
		t.Fatalf("stored note %q logs %q", note, logs)
	}
}
