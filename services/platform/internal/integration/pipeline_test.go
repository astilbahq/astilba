package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

func TestGitImportBuildIsolationAndArtifactAdmission(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	head := strings.Repeat("a", 40)
	var apiCalls atomic.Int64
	var repositoryIdentity atomic.Int64
	repositoryIdentity.Store(12)
	var providerHead, mergeHead atomic.Value
	providerHead.Store(head)
	mergeHead.Store("")
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		apiCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("Authorization") != "token api-write-canary" {
			w.WriteHeader(401)
			return
		}
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": providerHead.Load().(string)}})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/pulls/5") {
			_ = json.NewEncoder(w).Encode(map[string]any{"merged": mergeHead.Load().(string) != "", "merge_commit_sha": mergeHead.Load().(string)})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": repositoryIdentity.Load(), "private": true})
	}))
	defer provider.Close()
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
		Secret     string              `json:"webhookSecret"`
	}
	b.call("POST", prefix+"/git-connections", map[string]any{"name": "Source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app", "api": map[string]string{"username": "writer", "password": "api-write-canary"}, "clone": map[string]string{"username": "reader", "password": "clone-read-canary"}}, 201, &connection)
	// Lists cannot return stored integration values or the once-shown webhook key.
	list := b.call("GET", prefix+"/git-connections", nil, 200, nil)
	for _, secret := range []string{"api-write-canary", "clone-read-canary", connection.Secret} {
		if bytes.Contains(list, []byte(secret)) {
			t.Fatal("connection list exposed credential")
		}
	}
	var registry pipeline.Registry
	b.call("POST", prefix+"/registries", map[string]any{"name": "Images", "repository": "registry.example.test/" + strings.Repeat("a", 200-len("registry.example.test/")), "push": map[string]string{"username": "push", "password": "push-canary"}, "pull": map[string]string{"username": "pull", "password": "pull-canary"}}, 201, &registry)
	var buildSecret pipeline.BuildSecret
	b.call("POST", prefix+"/build-secrets", map[string]string{"name": "PACKAGE_TOKEN", "value": "build-only-canary"}, 201, &buildSecret)
	old := f.st.Keys
	nextKeys, err := cryptobox.New("next")
	if err != nil {
		t.Fatal(err)
	}
	nextKeys.Keys["test"] = old.Keys["test"]
	f.st.Keys = nextKeys
	if err = f.st.Rewrap(context.Background()); err != nil {
		t.Fatal(err)
	}
	delete(nextKeys.Keys, "test")
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Git builds"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	var runtime api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "RUNTIME_TOKEN", "value": "runtime-only-canary"}, 201, &runtime)
	enrolled, in, token := enrollment(t, f, b, ws, "combined")
	yes := true
	in.Capabilities.Build = &yes
	in.Capabilities.Visual = &yes
	in.Capabilities.Compose = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	var op api.Operation
	source := pipeline.Source{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": source}, 202, &op)
	poll := func(id uuid.UUID) api.ExecutionCommand {
		t.Helper()
		var v api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &v)
		if v.Command == nil || v.Command.Id != id {
			t.Fatal("assigned pipeline did not reach the runner")
		}
		return *v.Command
	}
	report := func(c api.ExecutionCommand, r pipeline.Result) {
		t.Helper()
		raw, _ := json.Marshal(r)
		msg := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &msg}, 204, nil)
	}
	c := poll(op.Id)
	for _, secret := range []string{"api-write-canary", connection.Secret, "runtime-only-canary", "build-only-canary", "push-canary", "pull-canary"} {
		if strings.Contains(string(*c.Pipeline), secret) {
			t.Fatal("import received unrelated credential", secret)
		}
	}
	if !strings.Contains(string(*c.Pipeline), "clone-read-canary") {
		t.Fatal("import omitted clone access")
	}
	files := []composeproject.File{{Path: "compose.yaml", Content: "# preserve me\nservices:\n  web:\n    image: " + pipelineTestImage + "\n"}}
	report(c, pipeline.Result{Commit: head, Files: files})
	var draft api.Revision
	b.call("GET", path+"/draft", nil, 200, &draft)
	var spec domain.Specification
	_ = json.Unmarshal(draft.Specification, &spec)
	if spec.Compose == nil || spec.Compose.Origin.Commit != head || spec.Compose.Files[0].Content != files[0].Content {
		t.Fatal("import lost exact identity or source")
	}
	app := spec.Applications[0].ID
	value := "production"
	spec.Variables = append(spec.Variables, domain.Variable{Name: "MODE", Value: &value})
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": draft.Id, "specification": spec}, 200, &draft)
	var sourceStatus api.GitSourceStatus
	b.call("GET", path+"/source", nil, 200, &sourceStatus)
	if !sourceStatus.Clean {
		t.Fatal("runtime-only edit became a dirty Git source")
	}
	var status deployment.Status
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: enrolled.Server.Id}, 201, &status)
	resolve := poll(status.OperationID)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: resolve.Id, Generation: resolve.Generation, PlanHash: resolve.PlanHash, Sequence: 2, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)

	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, RegistryID: &registry.ID, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{"package": buildSecret.ID}}
	key := uuid.New()
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": key, "configuration": cfg}, 202, &op)
	c = poll(op.Id)
	for _, secret := range []string{"api-write-canary", connection.Secret, "runtime-only-canary", "pull-canary"} {
		if strings.Contains(string(*c.Pipeline), secret) {
			t.Fatal("build received unrelated credential", secret)
		}
	}
	for _, secret := range []string{"clone-read-canary", "build-only-canary", "push-canary"} {
		if !strings.Contains(string(*c.Pipeline), secret) {
			t.Fatal("build omitted declared access", secret)
		}
	}
	digest := "sha256:" + strings.Repeat("b", 64)
	report(c, pipeline.Result{Commit: head, Image: registry.Repository + "@" + digest, Digest: digest, Architecture: "arm64", Logs: "build complete"})
	var runs []struct {
		ID       uuid.UUID          `json:"id"`
		Artifact *pipeline.Artifact `json:"artifact"`
	}
	b.call("GET", path+"/builds", nil, 200, &runs)
	if len(runs) != 2 || runs[0].Artifact == nil {
		t.Fatal("successful build did not record immutable artifact")
	}
	artifact := *runs[0].Artifact
	b.call("POST", path+"/artifacts/"+artifact.ID.String()+"/promote", api.PromoteArtifact{BaseRevisionId: draft.Id, ApplicationId: app, InputHash: artifact.InputHash}, 200, nil)
	var promoted api.Revision
	b.call("GET", path+"/draft", nil, 200, &promoted)
	var next domain.Specification
	_ = json.Unmarshal(promoted.Specification, &next)
	if !strings.Contains(next.Compose.Files[0].Content, digest) || !strings.Contains(next.Compose.Files[0].Content, "# preserve me") {
		t.Fatal("promotion lost source or immutable image")
	}
	// A Git draft is not deployable until the source branch has accepted it.
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: promoted.Id, ServerId: enrolled.Server.Id}, 400, nil)
	// Cancelled and expired work must never publish an artifact, even if the runner reports success.
	for _, expire := range []bool{false, true} {
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &op)
		c = poll(op.Id)
		if expire {
			c.Deadline = time.Now().Add(-time.Second)
			raw, _ := json.Marshal(c)
			if _, err := f.st.Pool.Exec(context.Background(), "UPDATE execution_operations SET command=$2 WHERE id=$1", c.Id, raw); err != nil {
				t.Fatal(err)
			}
		} else {
			b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
		}
		report(c, pipeline.Result{Commit: head, Image: registry.Repository + "@" + digest, Digest: digest, Architecture: "arm64"})
		var count int
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM build_artifacts WHERE id=$1", op.Id).Scan(&count)
		if count != 0 {
			t.Fatal("cancelled/expired result published artifact")
		}
	}
	// Authentication and repository identity are checked before delivery retention or queue insertion.
	delivery := []byte(`{"ref":"refs/heads/main","after":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","repository":{"id":12}}`)
	deliveryID := "one"
	send := func(body []byte, sign bool, status int) {
		t.Helper()
		req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Gitea-Event", "push")
		req.Header.Set("X-Gitea-Delivery", deliveryID)
		if sign {
			mac := hmac.New(sha256.New, []byte(connection.Secret))
			_, _ = mac.Write(body)
			req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
		res, err := f.server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != status {
			t.Fatalf("webhook: %d want %d %s", res.StatusCode, status, raw)
		}
	}
	send(delivery, false, 403)
	send(delivery, true, 202)
	send(delivery, true, 202)
	var count int
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM git_deliveries").Scan(&count)
	if count != 1 {
		t.Fatal("delivery replay was not deduplicated")
	}
	t.Run("secret arguments are rejected", func(t *testing.T) {
		escaped := "runtime&quote\"backslash\\newline\nend"
		b.call("POST", path+"/secrets", map[string]string{"name": "ESCAPED_RUNTIME", "value": escaped}, 201, nil)
		for _, value := range []string{"build-only-canary", "runtime-only-canary", escaped} {
			invalid := cfg
			invalid.Arguments = map[string]string{"TOKEN": value}
			b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": invalid}, 400, nil)
		}
	})
	t.Run("cancel and expiry deliver without provider credentials", func(t *testing.T) {
		for _, expired := range []bool{false, true} {
			b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &op)
			deadline := time.Now().Add(8 * time.Second)
			state := ""
			for time.Now().Before(deadline) {
				_ = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state)
				if state == "dispatched" {
					break
				}
				time.Sleep(25 * time.Millisecond)
			}
			if state != "dispatched" {
				t.Fatal("operation never dispatched", state)
			}
			repositoryIdentity.Store(13)
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 500, nil)
			if expired {
				var raw []byte
				_ = f.st.Pool.QueryRow(context.Background(), "SELECT command FROM execution_operations WHERE id=$1", op.Id).Scan(&raw)
				var frozen api.ExecutionCommand
				_ = json.Unmarshal(raw, &frozen)
				frozen.Deadline = time.Now().Add(-time.Second)
				raw, _ = json.Marshal(frozen)
				if _, err := f.st.Pool.Exec(context.Background(), "UPDATE execution_operations SET command=$2 WHERE id=$1", op.Id, raw); err != nil {
					t.Fatal(err)
				}
			} else {
				b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
			}
			command := poll(op.Id)
			for _, value := range []string{"clone-read-canary", "build-only-canary", "push-canary"} {
				if strings.Contains(string(*command.Pipeline), value) {
					t.Fatal("cancelled/expired delivery hydrated unnecessary credential")
				}
			}
			agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
			repositoryIdentity.Store(12)
		}
	})
	t.Run("offline primary queues without implicit fallback", func(t *testing.T) {
		if _, err := f.st.Pool.Exec(context.Background(), "UPDATE servers SET last_seen=now()-interval '2 minutes' WHERE id=$1", enrolled.Server.Id); err != nil {
			t.Fatal(err)
		}
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &op)
		time.Sleep(150 * time.Millisecond)
		var state string
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil {
			t.Fatal(err)
		}
		if state != "queued" || op.ServerId != enrolled.Server.Id {
			t.Fatal("offline work escaped the selected runner", state)
		}
		b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil || state != "cancelled" {
			t.Fatal("queued cancellation did not release work", state, err)
		}
	})
	t.Run("explicit fallback selects only an eligible runner", func(t *testing.T) {
		fallback, enroll, fallbackToken := enrollment(t, f, b, ws, "build")
		enroll.Capabilities.Build = &yes
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", enroll, 200, nil)
		cfg.FallbackRunnerID = &fallback.Server.Id
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &op)
		if op.ServerId != fallback.Server.Id {
			t.Fatal("explicit fallback was ignored")
		}
		var polled api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", fallbackToken, nil, 200, &polled)
		if polled.Command == nil || polled.Command.Id != op.Id {
			t.Fatal("fallback runner did not receive work")
		}
		command := *polled.Command
		// Another authenticated server cannot report the fallback's result.
		raw, _ := json.Marshal(pipeline.Result{Commit: head, Image: registry.Repository + "@" + digest, Digest: digest, Architecture: "arm64"})
		msg := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &msg}, 404, nil)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", fallbackToken, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
		cfg.FallbackRunnerID = nil
	})
	t.Run("stale build input cannot publish an artifact", func(t *testing.T) {
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
		b.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &op)
		command := poll(op.Id)
		if _, err := f.st.Pool.Exec(context.Background(), "UPDATE build_targets SET input_hash=$4 WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", ws, env.Id, app, strings.Repeat("f", 64)); err != nil {
			t.Fatal(err)
		}
		report(command, pipeline.Result{Commit: head, Image: registry.Repository + "@" + digest, Digest: digest, Architecture: "arm64"})
		var count int
		_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM build_artifacts WHERE id=$1", op.Id).Scan(&count)
		if count != 0 {
			t.Fatal("stale build published an artifact")
		}
	})
	t.Run("tenant and viewer boundaries precede provider access", func(t *testing.T) {
		var other api.Workspace
		b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)
		otherPath := strings.Replace(path, ws.String(), other.Id.String(), 1)
		b.call("GET", otherPath+"/builds", nil, 404, nil)
		b.call("POST", otherPath+"/artifacts/"+artifact.ID.String()+"/promote", api.PromoteArtifact{BaseRevisionId: promoted.Id, ApplicationId: app, InputHash: artifact.InputHash}, 404, nil)
		var otherArtifacts []pipeline.Artifact
		b.call("GET", "/api/v1/workspaces/"+other.Id.String()+"/artifacts", nil, 200, &otherArtifacts)
		if len(otherArtifacts) != 0 {
			t.Fatal("cross-workspace artifact disclosure")
		}
		b.call("POST", prefix+"/invitations", map[string]string{"email": "viewer@example.test", "role": "viewer"}, 201, nil)
		invite := tokenFromLinkMail(t, f, "/invitation")
		viewer := f.browser(t)
		var session api.Session
		viewer.call("POST", "/api/v1/auth/invitations/accept", map[string]string{"token": invite, "name": "Viewer", "password": f.password}, 200, &session)
		viewer.csrf = session.CsrfToken
		before := apiCalls.Load()
		viewer.call("POST", path+"/builds", map[string]any{"baseRevisionId": promoted.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 403, nil)
		viewer.call("POST", prefix+"/git-connections", map[string]any{"name": "Unauthorized", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 403, nil)
		if apiCalls.Load() != before {
			t.Fatal("unauthorized request contacted provider")
		}
	})

	t.Run("merge authority conflict preservation and opt-in deployment", func(t *testing.T) {
		waitSource := func(status string) {
			t.Helper()
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				var state string
				_ = f.st.Pool.QueryRow(context.Background(), "SELECT status FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", ws, env.Id).Scan(&state)
				if state == status {
					return
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("source status did not become", status)
		}
		push := func(commit, id string) {
			providerHead.Store(commit)
			deliveryID = id
			body, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
			send(body, true, 202)
		}
		push(strings.Repeat("c", 40), "conflict")
		waitSource("conflict")
		var current api.Revision
		b.call("GET", path+"/draft", nil, 200, &current)
		if current.Id != promoted.Id {
			t.Fatal("webhook overwrote pending draft")
		}
		b.call("POST", path+"/source/refresh", map[string]any{"baseRevisionId": current.Id, "discardDraft": false}, 409, nil)
		b.call("POST", path+"/source/refresh", map[string]any{"baseRevisionId": current.Id, "discardDraft": true}, 202, &op)
		imported := poll(op.Id)
		report(imported, pipeline.Result{Commit: strings.Repeat("c", 40), Files: files})
		b.call("GET", path+"/draft", nil, 200, &current)
		var edited domain.Specification
		_ = json.Unmarshal(current.Specification, &edited)
		edited.Compose.Files[0].Content += "# submitted change\n"
		edited.Compose.Files[0].Path = "deployment.yaml"
		edited.Compose.Entrypoints = []string{"deployment.yaml"}
		edited.Compose.Files = append(edited.Compose.Files, composeproject.File{Path: "settings.conf", Content: "example=true\n"})
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, &current)
		var submitted, again api.Operation
		key := uuid.New()
		b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": current.Id, "requestKey": key}, 202, &submitted)
		b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": current.Id, "requestKey": uuid.New()}, 202, &again)
		if again.Id != submitted.Id {
			t.Fatal("one draft created multiple submissions")
		}
		command := poll(submitted.Id)
		var first pipeline.Command
		_ = json.Unmarshal(*command.Pipeline, &first)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "failed", Steps: []api.ExecutionStep{}}, 204, nil)
		b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": current.Id, "requestKey": uuid.New()}, 202, &again)
		if again.Id == submitted.Id {
			t.Fatal("terminal submission could not retry")
		}
		command = poll(again.Id)
		var retry pipeline.Command
		_ = json.Unmarshal(*command.Pipeline, &retry)
		if first.SubmissionID != retry.SubmissionID || !first.SubmittedAt.Equal(retry.SubmittedAt) {
			t.Fatal("retry changed logical submission identity")
		}

		if strings.Contains(string(*command.Pipeline), "clone-read-canary") || strings.Contains(string(*command.Pipeline), "runtime-only-canary") {
			t.Fatal("submission received unrelated credentials")
		}
		merge := strings.Repeat("d", 40)
		report(command, pipeline.Result{Commit: merge, ReviewNumber: 5, ReviewURL: provider.URL + "/team/app/pulls/5"})
		// Platform-only edits after submission must retain the merged Git file identity.
		value := "updated-runtime"
		edited.Variables = append(edited.Variables, domain.Variable{Name: "AFTER_SUBMISSION", Value: &value})
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, &current)
		var duplicate api.Operation
		b.call("POST", path+"/source/submit", map[string]any{"baseRevisionId": current.Id, "requestKey": uuid.New()}, 202, &duplicate)
		newer := poll(duplicate.Id)
		report(newer, pipeline.Result{Commit: strings.Repeat("f", 40), ReviewNumber: 6, ReviewURL: provider.URL + "/team/app/pulls/6"})

		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: current.Id, ServerId: enrolled.Server.Id}, 400, nil)
		mergeHead.Store(merge)
		push(merge, "merge")
		// Wait for the background refresh to enqueue, then the assigned runner imports the authoritative merge.
		var polled api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		if polled.Command == nil || polled.Command.Kind != "pipeline" {
			t.Fatal("merge did not enqueue source import")
		}
		var mergedImport pipeline.Command
		_ = json.Unmarshal(*polled.Command.Pipeline, &mergedImport)
		if len(mergedImport.Source.Paths) != 2 || mergedImport.Source.Entrypoints[0] != "deployment.yaml" {
			t.Fatal("merged file selection was lost")
		}
		report(*polled.Command, pipeline.Result{Commit: merge, Files: edited.Compose.Files})
		b.call("GET", path+"/draft", nil, 200, &current)
		var merged domain.Specification
		_ = json.Unmarshal(current.Specification, &merged)
		if len(merged.Variables) != len(edited.Variables) || merged.Compose.Origin.Commit != merge || merged.Compose.Files[0].Content != edited.Compose.Files[0].Content {
			t.Fatal("merge source was not authoritative")
		}
		// Model an already reviewed deployment; automatic follow-ups still use normal plan/resolve/apply admission.
		if _, err := f.st.Pool.Exec(context.Background(), "UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2", ws, env.Id, current.Id); err != nil {
			t.Fatal(err)
		}
		if _, err := f.st.Pool.Exec(context.Background(), "UPDATE deployment_units SET server_id=$3 WHERE workspace_id=$1 AND environment_id=$2", ws, env.Id, enrolled.Server.Id); err != nil {
			t.Fatal(err)
		}
		b.call("PUT", path+"/source/policy", map[string]bool{"autoDeploy": true}, 204, nil)
		latest := strings.Repeat("e", 40)
		push(latest, "automatic")
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		if polled.Command == nil || polled.Command.Kind != "pipeline" {
			t.Fatal("automatic source import missing")
		}
		autoRun := polled.Command.Id
		report(*polled.Command, pipeline.Result{Commit: latest, Files: edited.Compose.Files})
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		if polled.Command == nil || polled.Command.Kind != "visual" {
			t.Fatal("automatic deployment omitted image review")
		}
		var payload deployment.Payload
		_ = json.Unmarshal(*polled.Command.Visual, &payload)
		if payload.Action != "resolve" || payload.Plan.HostChanges {
			t.Fatal("automatic deployment escalated host permissions")
		}
		observation := deployment.Observation{Images: map[string]string{app.String(): pipelineTestImage}, Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64), ObservedAt: time.Now()}
		raw, _ := json.Marshal(observation)
		message := json.RawMessage(raw)
		command = *polled.Command
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &message}, 204, nil)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &polled)
		if polled.Command == nil || polled.Command.Kind != "visual" {
			t.Fatal("automatic apply missing")
		}
		_ = json.Unmarshal(*polled.Command.Visual, &payload)
		if payload.Action != "deploy" {
			t.Fatal("automatic policy did not follow normal apply")
		}
		command = *polled.Command
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: command.Id, Generation: command.Generation, PlanHash: command.PlanHash, Sequence: 2, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
		var state string
		var deploymentID *uuid.UUID
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT state,operation_id FROM pipeline_auto_deployments WHERE id=$1", autoRun).Scan(&state, &deploymentID); err != nil || state != "queued" || deploymentID == nil || *deploymentID != command.Id {
			t.Fatal("automatic policy did not record its operation", state, err)
		}
		b.call("PUT", path+"/source/policy", map[string]bool{"autoDeploy": false}, 204, nil)
		// Removing a known secret still exposes it on the removed side of the review.
		var contaminated api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Contaminated source", "configurationOwner": "compose"}, 201, &contaminated)
		contaminatedPath := prefix + "/projects/" + project.Id.String() + "/environments/" + contaminated.Id.String()
		b.call("POST", contaminatedPath+"/secrets", map[string]string{"name": "TOKEN", "value": "baseline-secret-canary"}, 201, nil)
		b.call("PUT", contaminatedPath+"/source", map[string]any{"baseRevisionId": contaminated.DraftRevisionId, "configuration": source}, 202, &op)
		contaminatedFiles := []composeproject.File{{Path: "compose.yaml", Content: files[0].Content + "# baseline-secret-canary\n"}}
		report(poll(op.Id), pipeline.Result{Commit: latest, Files: contaminatedFiles})
		b.call("GET", contaminatedPath+"/draft", nil, 200, &current)
		_ = json.Unmarshal(current.Specification, &edited)
		edited.Compose.Files[0].Content = files[0].Content
		b.call("PUT", contaminatedPath+"/draft", map[string]any{"baseRevisionId": current.Id, "specification": edited}, 200, &current)
		b.call("POST", contaminatedPath+"/source/submit", map[string]any{"baseRevisionId": current.Id, "requestKey": uuid.New()}, 400, nil)

	})

}

const pipelineTestImage = "busybox:1.37.0@sha256:bdf57e528e45e4433820e045b29b4597825a1c9e38353532d90a01445013f82e"

// A webhook touching more targets than the queue cap must make partial progress rather than roll back every attempt.
func TestGitWebhookBurstMakesBoundedProgress(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	commit := strings.Repeat("b", 40)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			_ = json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": commit}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": false})
	}))
	defer provider.Close()
	var connection struct {
		Connection    pipeline.Connection
		WebhookSecret string
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Burst", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	enrolled, in, _ := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE servers SET last_seen=now()-interval '2 minutes' WHERE id=$1", enrolled.Server.Id); err != nil {
		t.Fatal(err)
	}
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Burst"}, 201, &project)
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	raw, _ := json.Marshal(cfg)
	for i := 0; i < 51; i++ {
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": fmt.Sprintf("Target %d", i)}, 201, &env)
		app := uuid.New()
		image := pipelineTestImage
		spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
		b.call("PUT", prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, nil)
		if _, err := f.st.Pool.Exec(context.Background(), "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor) VALUES($1,$2,$3,$4,$5,$6,$7,$8)", ws, env.Id, app, connection.Connection.ID, raw, strings.Repeat("a", 64), strings.Repeat("a", 40), f.owner); err != nil {
			t.Fatal(err)
		}
	}
	body, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(connection.WebhookSecret))
	_, _ = mac.Write(body)
	req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "burst")
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatal("webhook rejected", res.StatusCode)
	}
	wait := func(want int) {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			var count int
			_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM pipeline_runs WHERE workspace_id=$1", ws).Scan(&count)
			if count == want {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal("bounded refresh did not progress to", want)
	}
	wait(50)
	var active int
	_ = f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND state NOT IN ('succeeded','failed','cancelled')", ws).Scan(&active)
	if active != 50 {
		t.Fatal("queue cap not respected", active)
	}
	if _, err = f.st.Pool.Exec(context.Background(), "UPDATE execution_operations SET state='cancelled',cancel_requested=true WHERE workspace_id=$1", ws); err != nil {
		t.Fatal(err)
	}
	wait(51)
}
