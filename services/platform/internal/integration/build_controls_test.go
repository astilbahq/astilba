package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

func TestBuildControlsAndSuccessfulWatchBaseline(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var head atomic.Value
	head.Store(strings.Repeat("a", 40))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": head.Load().(string)}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": false})
	}))
	defer provider.Close()
	var connection struct {
		Connection    pipeline.Connection
		WebhookSecret string
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Watch", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Monorepo"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Web"}, 201, &env)
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
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, WatchPaths: []string{"apps/web", "lockfile"}, Budget: &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 2}}
	create := func(status int) api.Operation {
		t.Helper()
		var op api.Operation
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, status, &op)
		return op
	}
	create(400)
	in.Capabilities.BuildControls = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	cfg.Budget.MemoryMiB = 512
	var validation api.Error
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 400, &validation)
	if validation.Fields == nil || (*validation.Fields)[0] != "build.budget" || !strings.Contains(validation.Message, "96 MiB") {
		t.Fatal("budget error lost corrective diagnostics", validation)
	}
	cfg.Budget.MemoryMiB = 1024
	op := create(202)
	poll := func() api.ExecutionCommand {
		t.Helper()
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &out)
		if out.Command == nil {
			t.Fatal("build not dispatched")
		}
		return *out.Command
	}
	report := func(c api.ExecutionCommand, state api.ExecutionState, result pipeline.Result) {
		t.Helper()
		raw, _ := json.Marshal(result)
		msg := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Steps: []api.ExecutionStep{}, Pipeline: &msg}, 204, nil)
	}
	c := poll()
	if c.Id != op.Id || time.Until(c.Deadline) > 2*time.Minute || time.Until(c.Deadline) < time.Minute {
		t.Fatal("assignment budget not frozen", c.Deadline)
	}
	var p pipeline.Command
	p = pipeline.Command{}
	_ = json.Unmarshal(*c.Pipeline, &p)
	if p.CompareCommit != "" || p.Build.Budget.MemoryMiB != 1024 || p.CacheScope != "" {
		t.Fatal("manual build was filtered or lost limits")
	}
	// A skipped report cannot manufacture successful manual work or an artifact.
	raw, _ := json.Marshal(pipeline.Result{Commit: p.Commit, Skipped: true})
	msg := json.RawMessage(raw)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &msg}, 400, nil)
	report(c, "succeeded", pipeline.Result{Commit: p.Commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)})
	baseline := p.Commit
	in.Capabilities.BuildCache = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	push := func(commit string) {
		t.Helper()
		head.Store(commit)
		body, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
		mac := hmac.New(sha256.New, []byte(connection.WebhookSecret))
		_, _ = mac.Write(body)
		req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitea-Event", "push")
		req.Header.Set("X-Gitea-Delivery", uuid.NewString())
		req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 202 {
			t.Fatal("push rejected", res.StatusCode)
		}
	}
	for i, state := range []api.ExecutionState{"succeeded", "failed", "cancelled", "succeeded"} {
		commit := strings.Repeat(string(rune('b'+i)), 40)
		push(commit)
		c = poll()
		p = pipeline.Command{}
		_ = json.Unmarshal(*c.Pipeline, &p)
		if p.Commit != commit || p.CompareCommit != baseline || p.CacheScope != pipeline.Hash([]uuid.UUID{ws, project.Id}) {
			t.Fatal("watch baseline advanced past the last real artifact", p.CompareCommit)
		}
		report(c, state, pipeline.Result{Commit: commit, Architecture: "arm64", Skipped: state == "succeeded"})
	}
	var count int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM build_artifacts WHERE workspace_id=$1", ws).Scan(&count); err != nil || count != 1 {
		t.Fatal("skipped work published artifact", count, err)
	}
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM pipeline_auto_deployments WHERE workspace_id=$1", ws).Scan(&count); err != nil || count != 0 {
		t.Fatal("skipped work deployed", count, err)
	}
	// Changed image inputs require a build even when source changes appear unrelated.
	if _, err := f.st.Pool.Exec(context.Background(), `UPDATE build_targets SET configuration=jsonb_set(configuration,'{arguments}', '{"MODE":"changed"}') WHERE workspace_id=$1`, ws); err != nil {
		t.Fatal(err)
	}
	push(strings.Repeat("f", 40))
	c = poll()
	p = pipeline.Command{}
	_ = json.Unmarshal(*c.Pipeline, &p)
	if p.CompareCommit != "" {
		t.Fatal("changed image inputs reused watch baseline")
	}
	report(c, "failed", pipeline.Result{})
	// Railpack is an opt-in contract; older and downgraded agents must never
	// receive hydrated build inputs they cannot execute.
	cfg.Builder, cfg.Dockerfile, cfg.WatchPaths = "railpack", "", nil
	cfg.Budget = &pipeline.BuildBudget{CPUs: 2, MemoryMiB: 6144, SourceMiB: 1024, StateMiB: 4096, Minutes: 10}
	create(400)
	version := "0.42.0"
	in.Capabilities.RailpackVersion = &version
	heartbeat := func() {
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
	}
	heartbeat()
	create(400)
	version = pipeline.RailpackVersion
	heartbeat()
	op = create(202)
	c = poll()
	p = pipeline.Command{}
	_ = json.Unmarshal(*c.Pipeline, &p)
	if p.Railpack == nil || *p.Railpack != pipeline.RailpackFor("arm64") || p.Build.Dockerfile != "" || p.Build.Builder != "railpack" {
		t.Fatal("Railpack assignment lost frozen inputs")
	}
	badReport := pipeline.Result{Commit: p.Commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)}
	for _, summary := range []*pipeline.RailpackSummary{nil, {Version: "0.42.0", Frontend: pipeline.RailpackFrontend, Provider: "node", PlanHash: strings.Repeat("b", 64), Packages: map[string]string{}}} {
		badReport.Railpack = summary
		raw, _ := json.Marshal(badReport)
		msg := json.RawMessage(raw)
		var rejection api.Error
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &msg}, 400, &rejection)
		if rejection.Fields == nil || len(*rejection.Fields) != 1 || (*rejection.Fields)[0] != "build.railpack" {
			t.Fatal("summary was not rejected by assigned-input validation", rejection)
		}
	}
	in.Capabilities.RailpackVersion = nil
	heartbeat()
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 400, nil)
	b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
	c = poll()
	p = pipeline.Command{}
	_ = json.Unmarshal(*c.Pipeline, &p)
	if !c.CancelRequested || p.Access != nil || p.BuildSecrets != nil || p.RegistryAccess != nil {
		t.Fatal("downgraded runner cancellation was not credential-free")
	}
	report(c, "cancelled", pipeline.Result{})
	in.Capabilities.RailpackVersion = &version
	heartbeat()
	push(strings.Repeat("1", 40))
	c = poll()
	p = pipeline.Command{}
	_ = json.Unmarshal(*c.Pipeline, &p)
	if p.Railpack == nil || *p.Railpack != pipeline.RailpackFor("arm64") || p.Build.Builder != "railpack" {
		t.Fatal("automatic build omitted Railpack frozen inputs")
	}
	report(c, "succeeded", pipeline.Result{Commit: p.Commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64), Railpack: &pipeline.RailpackSummary{Version: pipeline.RailpackVersion, Frontend: pipeline.RailpackFrontend, Provider: "node", PlanHash: strings.Repeat("b", 64), Packages: map[string]string{"node": "24.18.1"}}})
	t.Run("runtime secrets introduced after build configuration", func(t *testing.T) {
		ctx := context.Background()
		cfg.Arguments = map[string]string{"TOKEN": "later-runtime-canary"}
		op := create(202)
		b.call("POST", path+"/secrets", map[string]string{"name": "LATER_TOKEN", "value": "later-runtime-canary"}, 201, nil)
		// A previously admitted command must not receive newly secret public inputs.
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 400, nil)
		b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
		c := poll()
		if !c.CancelRequested {
			t.Fatal("secret rejection prevented credential-free cancellation")
		}
		report(c, "cancelled", pipeline.Result{})

		var sibling api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Unaffected sibling"}, 201, &sibling)
		siblingPath := prefix + "/projects/" + project.Id.String() + "/environments/" + sibling.Id.String()
		var siblingDraft api.Revision
		b.call("PUT", siblingPath+"/draft", map[string]any{"baseRevisionId": sibling.DraftRevisionId, "specification": spec}, 200, &siblingDraft)
		valid := cfg
		valid.Arguments = map[string]string{}
		b.call("POST", siblingPath+"/builds", map[string]any{"baseRevisionId": siblingDraft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": valid}, 202, nil)
		initial := poll()
		report(initial, "failed", pipeline.Result{})

		var before int64
		if err := f.st.Pool.QueryRow(ctx, "SELECT COALESCE(max(id),0) FROM river_job").Scan(&before); err != nil {
			t.Fatal(err)
		}
		commit := strings.Repeat("2", 40)
		push(commit)
		var state string
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if err := f.st.Pool.QueryRow(ctx, "SELECT state FROM river_job WHERE id>$1 AND kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1", before).Scan(&state); err == nil && (state == "retryable" || state == "completed") {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		var admitted int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE environment_id=$1 AND configuration->>'commit'=$2", env.Id, commit).Scan(&admitted); err != nil {
			t.Fatal(err)
		}
		if state != "retryable" || admitted != 0 {
			t.Fatal("automatic admission bypassed runtime-secret exclusion", state, admitted)
		}
		var siblings int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE environment_id=$1 AND configuration->>'commit'=$2", sibling.Id, commit).Scan(&siblings); err != nil || siblings != 1 {
			t.Fatal("rejected target prevented valid sibling progress", siblings, err)
		}
	})
}
