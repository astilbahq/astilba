package integration

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

// An owner's build network allowlist reaches the runner frozen into each build, and a build
// queued before the owner removes a destination never runs with it.
func TestBuildNetworkAllowlist(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ctx := context.Background()
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
	defer provider.Close()
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Mirror", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Egress"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	app := uuid.New()
	image := pipelineTestImage
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}}, 200, &draft)

	deployment, _, _ := enrollment(t, f, b, ws, "deployment")
	b.call("PUT", prefix+"/servers/"+deployment.Server.Id.String()+"/build-network", api.BuildNetwork{Allow: []api.BuildDestination{{Prefix: "10.0.4.12", Port: 443}}}, 400, nil)

	enrolled, in, token := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	route := prefix + "/servers/" + enrolled.Server.Id.String() + "/build-network"
	heartbeat := func() {
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	}
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	request := func(status int, out any) []byte {
		t.Helper()
		return b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, status, out)
	}
	build := func() api.Operation {
		t.Helper()
		var op api.Operation
		request(202, &op)
		return op
	}
	// An agent that can't fence builds off the host's networks gets none.
	if body := request(400, nil); !strings.Contains(string(body), "runner.buildNetworkUpgradeRequired") {
		t.Fatal("a build was offered to an agent without the build network", string(body))
	}
	in.Capabilities.BuildNetwork = &yes
	heartbeat()

	var current api.BuildNetwork
	b.call("GET", route, nil, 200, &current)
	if len(current.Allow) != 0 {
		t.Fatal("a new runner allows private destinations", current)
	}
	for _, refused := range []api.BuildDestination{{Prefix: "169.254.169.254", Port: 80}, {Prefix: "169.254.0.0/16", Port: 80}, {Prefix: "127.0.0.1", Port: 5432}, {Prefix: "8.8.8.8", Port: 53}, {Prefix: "10.0.0.0/7", Port: 443}, {Prefix: "10.0.4.12", Port: 0}, {Prefix: "fd00::1", Port: 443}, {Prefix: "mirror.lan", Port: 443}} {
		b.call("PUT", route, api.BuildNetwork{Allow: []api.BuildDestination{refused}}, 400, nil)
	}
	tooMany := api.BuildNetwork{Allow: []api.BuildDestination{}}
	for i := range pipeline.MaxEgressRules + 1 {
		tooMany.Allow = append(tooMany.Allow, api.BuildDestination{Prefix: fmt.Sprintf("10.0.%d.1", i), Port: 443})
	}
	b.call("PUT", route, tooMany, 400, nil)
	b.call("PUT", route, api.BuildNetwork{Allow: []api.BuildDestination{{Prefix: "10.0.5.0/24", Port: 8081}, {Prefix: "10.0.4.12", Port: 443}, {Prefix: "10.0.4.12/32", Port: 443}}}, 200, &current)
	want := []pipeline.EgressRule{{Prefix: "10.0.4.12/32", Port: 443}, {Prefix: "10.0.5.0/24", Port: 8081}}
	if fmt.Sprint(current.Allow) != fmt.Sprint([]api.BuildDestination{{Prefix: "10.0.4.12/32", Port: 443}, {Prefix: "10.0.5.0/24", Port: 8081}}) {
		t.Fatal("allowlist was not stored canonically", current)
	}
	var audited int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='server.build_network.configure' AND resource_id=$2 AND actor_id=$3", ws, enrolled.Server.Id, f.owner).Scan(&audited); err != nil || audited != 1 {
		t.Fatal("allowlist change was not audited once", audited, err)
	}

	poll := func() *api.ExecutionCommand {
		t.Helper()
		heartbeat()
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &out)
		return out.Command
	}
	finish := func(c api.ExecutionCommand) {
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	}
	frozen := func(c api.ExecutionCommand) pipeline.Command {
		t.Helper()
		var p pipeline.Command
		if err := json.Unmarshal(*c.Pipeline, &p); err != nil || p.Digest() != c.PlanHash {
			t.Fatal("invalid delivered build", err)
		}
		return p
	}
	op := build()
	awaitDispatch(t, ctx, f, op.Id)
	c := poll()
	if c == nil || c.Id != op.Id || fmt.Sprint(frozen(*c).BuildEgress) != fmt.Sprint(want) {
		t.Fatal("the build does not carry its runner's allowlist", c)
	}
	finish(*c)

	state := func(id uuid.UUID) (string, string) {
		t.Helper()
		var state, note string
		if err := f.st.Pool.QueryRow(ctx, "SELECT state,note FROM execution_operations WHERE id=$1", id).Scan(&state, &note); err != nil {
			t.Fatal(err)
		}
		return state, note
	}
	narrow := func(keep ...api.BuildDestination) {
		b.call("PUT", route, api.BuildNetwork{Allow: append([]api.BuildDestination{}, keep...)}, 200, nil)
	}
	// Queued and never delivered: it fails, and the runner's queue moves on.
	queued := build()
	awaitDispatch(t, ctx, f, queued.Id)
	narrow(api.BuildDestination{Prefix: "10.0.5.0/24", Port: 8081})
	if c := poll(); c != nil {
		t.Fatal("a build reached the runner with a destination the owner removed", c.Id)
	}
	if got, note := state(queued.Id); got != "failed" || !strings.Contains(note, "no longer includes a private destination") {
		t.Fatal("narrowed undelivered build was not settled", got, note)
	}
	// Possibly held by the runner: it is told to stop.
	narrow(api.BuildDestination{Prefix: "10.0.4.12", Port: 443}, api.BuildDestination{Prefix: "10.0.5.0/24", Port: 8081})
	delivered := build()
	awaitDispatch(t, ctx, f, delivered.Id)
	if c := poll(); c == nil || c.Id != delivered.Id || c.CancelRequested {
		t.Fatal("expected the build's first delivery", c)
	}
	narrow()
	deadline := time.Now().Add(10 * time.Second)
	for {
		c := poll()
		if c != nil && c.Id == delivered.Id && c.CancelRequested {
			if frozen(*c).Access != nil {
				t.Fatal("the stop carried credentials")
			}
			finish(*c)
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a delivered build kept a destination the owner removed", c)
		}
	}
	if got, _ := state(delivered.Id); got != "cancelled" {
		t.Fatal("stopped build did not finish cancelled", got)
	}

	// A wider entry that still covers a queued build's destination leaves it to run.
	narrow(api.BuildDestination{Prefix: "10.0.4.12", Port: 443})
	widened := build()
	awaitDispatch(t, ctx, f, widened.Id)
	narrow(api.BuildDestination{Prefix: "10.0.4.0/24", Port: 443})
	if c := poll(); c == nil || c.Id != widened.Id || c.CancelRequested {
		t.Fatal("a build was stopped although its destination is still allowed", c)
	}
	finish(*poll())

	// A runner that stops reporting build isolation, such as one rolled back to an older
	// agent, is never handed a queued build, which would run on its host's network.
	narrow()
	rolledBack := build()
	awaitDispatch(t, ctx, f, rolledBack.Id)
	in.Capabilities.BuildNetwork = nil
	if c := poll(); c != nil {
		t.Fatal("a build reached a runner without build isolation", c.Id)
	}
	if got, note := state(rolledBack.Id); got != "failed" || !strings.Contains(note, "no longer reports build network isolation") {
		t.Fatal("build for a runner without isolation was not settled", got, note)
	}
}
