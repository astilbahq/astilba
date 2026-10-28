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

func TestBuildDeliveryAdmissionLifecycle(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ctx := context.Background()
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
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Lifecycle", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Lifecycle"}, 201, &project)
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "web", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	createEnvironment := func(name string) (string, api.Revision) {
		t.Helper()
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name}, 201, &env)
		path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
		var draft api.Revision
		b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
		return path, draft
	}
	path, draft := createEnvironment("Guarded inputs")
	healthyPath, healthyDraft := createEnvironment("Healthy queue")
	enrolled, in, token := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	create := func(t *testing.T, path string, draft api.Revision, config pipeline.BuildConfig) api.Operation {
		t.Helper()
		client := *b
		client.t = t
		var op api.Operation
		client.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": config}, 202, &op)
		return op
	}
	poll := func(t *testing.T) api.ExecutionCommand {
		t.Helper()
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/heartbeat", token, api.AgentHeartbeat{Capabilities: in.Capabilities, Preflight: in.Preflight}, 200, nil)
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &out)
		if out.Command == nil {
			diagnosticCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var diagnostic []byte
			err := f.st.Pool.QueryRow(diagnosticCtx, `SELECT jsonb_build_object(
 'server', (SELECT jsonb_build_object('lastSeen',last_seen,'online',last_seen>now()-interval '45 seconds') FROM servers WHERE id=$1),
 'operations', COALESCE((SELECT jsonb_agg(v) FROM (SELECT id,state,report_sequence,cancel_requested,command->>'deadline' AS deadline FROM execution_operations WHERE server_id=$1 ORDER BY created_at DESC LIMIT 5) v),'[]'::jsonb),
 'refreshJobs', COALESCE((SELECT jsonb_agg(v) FROM (SELECT id,state,attempt,scheduled_at,attempted_at FROM river_job WHERE kind='platform_git_refresh_v1' AND args->>'workspace'=$2::text ORDER BY id DESC LIMIT 3) v),'[]'::jsonb))`, enrolled.Server.Id, ws).Scan(&diagnostic)
			t.Fatalf("expected queued build; bounded queue snapshot: %s; snapshot error: %v", diagnostic, err)
		}
		return *out.Command
	}
	finish := func(t *testing.T, c api.ExecutionCommand) {
		t.Helper()
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	}
	for _, mode := range []string{"never delivered", "delivered without report", "legacy unknown"} {
		t.Run(mode, func(t *testing.T) {
			b := *b
			b.t = t
			config := cfg
			canary := "runtime-canary-" + strings.ReplaceAll(mode, " ", "-")
			config.Arguments = map[string]string{"TOKEN": canary}
			op := create(t, path, draft, config)
			if mode == "delivered without report" {
				if c := poll(t); c.Id != op.Id {
					t.Fatal("wrong initial delivery", c.Id)
				}
			} else if mode == "legacy unknown" {
				if _, err := f.st.Pool.Exec(ctx, "UPDATE pipeline_runs SET delivery_started=NULL WHERE id=$1", op.Id); err != nil {
					t.Fatal(err)
				}
			}
			b.call("POST", path+"/secrets", map[string]string{"name": "TOKEN_" + strings.ReplaceAll(mode, " ", "_"), "value": canary}, 201, nil)
			if mode == "never delivered" {
				// River workers need not dispatch queued builds in creation order.
				deadline := time.Now().Add(8 * time.Second)
				state := ""
				for time.Now().Before(deadline) {
					if err := f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); err != nil {
						t.Fatal(err)
					}
					if state == "dispatched" {
						break
					}
					time.Sleep(25 * time.Millisecond)
				}
				if state != "dispatched" {
					t.Fatal("guarded build did not become ready for delivery", state)
				}
				healthy := create(t, healthyPath, healthyDraft, cfg)
				c := poll(t)
				if c.Id != healthy.Id {
					t.Fatal("rejected undelivered build held the runner queue", c.Id)
				}
				finish(t, c)
			} else {
				agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 400, nil)
			}
			var state, note string
			var sequence int64
			var started *bool
			if err := f.st.Pool.QueryRow(ctx, "SELECT o.state,o.note,o.report_sequence,r.delivery_started FROM execution_operations o JOIN pipeline_runs r ON r.id=o.id WHERE o.id=$1", op.Id).Scan(&state, &note, &sequence, &started); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(note, canary) || sequence != 0 {
				t.Fatal("rejection retained secret data or manufactured an agent report", note, sequence)
			}
			if mode == "never delivered" {
				if state != "failed" || started == nil || *started || !strings.Contains(note, "Public build arguments") {
					t.Fatal("undelivered rejection was not terminal", state, started, note)
				}
				return
			}
			if state != "dispatched" || mode == "delivered without report" && (started == nil || !*started) || mode == "legacy unknown" && started != nil {
				t.Fatal("possibly delivered build was falsely failed", state, started)
			}
			b.call("POST", prefix+"/operations/"+op.Id.String()+"/cancel", nil, 204, nil)
			c := poll(t)
			var command pipeline.Command
			if err := json.Unmarshal(*c.Pipeline, &command); err != nil || !c.CancelRequested || command.Access != nil || command.BuildSecrets != nil {
				t.Fatal("rejection obstructed credential-free cancellation", c, err)
			}
			finish(t, c)
		})
	}
	t.Run("database failure remains retryable", func(t *testing.T) {
		b := *b
		b.t = t
		var secret pipeline.BuildSecret
		b.call("POST", prefix+"/build-secrets", map[string]string{"name": "DECLARED", "value": "build-canary"}, 201, &secret)
		config := cfg
		config.Secrets = map[string]uuid.UUID{"TOKEN": secret.ID}
		config.AutoBuild = true
		initial := create(t, path, draft, config)
		finish(t, poll(t))
		if _, err := f.st.Pool.Exec(ctx, "ALTER TABLE build_secret_versions RENAME COLUMN encrypted_value TO temporarily_unavailable"); err != nil {
			t.Fatal(err)
		}
		restored := false
		t.Cleanup(func() {
			if !restored {
				_, _ = f.st.Pool.Exec(ctx, "ALTER TABLE build_secret_versions RENAME COLUMN temporarily_unavailable TO encrypted_value")
			}
		})
		commit := strings.Repeat("b", 40)
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
		var state string
		var refreshID int64
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			if err := f.st.Pool.QueryRow(ctx, "SELECT id,state FROM river_job WHERE kind='platform_git_refresh_v1' ORDER BY id DESC LIMIT 1").Scan(&refreshID, &state); err == nil && state == "retryable" {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		if state != "retryable" {
			t.Fatal("database failure was completed as invalid input", state)
		}
		var rejections []api.BuildRejection
		b.call("GET", path+"/build-rejections", nil, 200, &rejections)
		if len(rejections) != 0 {
			t.Fatal("database failure created permanent input diagnostics", rejections)
		}
		if _, err := f.st.Pool.Exec(ctx, "ALTER TABLE build_secret_versions RENAME COLUMN temporarily_unavailable TO encrypted_value"); err != nil {
			t.Fatal(err)
		}
		restored = true
		// Recovery is the contract here; River's increasing retry delays are not.
		if _, err := f.execution.Queue.JobRetry(ctx, refreshID); err != nil {
			t.Fatal(err)
		}
		c := poll(t)
		if c.Id == initial.Id {
			t.Fatal("automatic retry reused finished operation")
		}
		finish(t, c)
	})
}
