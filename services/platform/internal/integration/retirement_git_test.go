package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A push to a branch a retired environment shares with an active one builds only the active one. A
// retired environment's build target would be refused at admission, failing the whole refresh, so
// the active environment would never build.
func TestGitPushSkipsARetiredEnvironmentOnTheBranch(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	commit := strings.Repeat("b", 40)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			answerBranch(w, "gitea", commit)
			return
		}
		answerRepository(w, "gitea", 12, false)
	}))
	defer provider.Close()
	var connection struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Shared", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	enrolled, in, _ := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Shared branch"}, 201, &project)
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true}
	raw, _ := stablejson.Marshal(cfg)
	target := func(name string) api.Environment {
		t.Helper()
		var env api.Environment
		b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name}, 201, &env)
		app := uuid.New()
		image := pipelineTestImage
		spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
		b.call("PUT", prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, nil)
		if _, err := f.st.Pool.Exec(context.Background(), "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor,policy_auth_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1)", ws, env.Id, app, connection.Connection.ID, raw, strings.Repeat("a", 64), strings.Repeat("a", 40), f.owner); err != nil {
			t.Fatal(err)
		}
		return env
	}
	active, retired := target("Active"), target("Retired")
	// Never deployed, so it is retired at once.
	var after api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments/"+retired.Id.String()+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Retired"}, 201, &after)
	if after.State != "retired" {
		t.Fatalf("a never-deployed environment was left %s", after.State)
	}

	body, _ := stablejson.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(connection.WebhookSecret))
	_, _ = mac.Write(body)
	req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "shared-branch")
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatal("webhook rejected", res.StatusCode)
	}
	runs := func(env uuid.UUID) int {
		t.Helper()
		var n int
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM pipeline_runs WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	deadline := time.Now().Add(10 * time.Second)
	for runs(active.Id) == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if runs(active.Id) != 1 {
		t.Fatal("the active environment did not build after the push")
	}
	if runs(retired.Id) != 0 {
		t.Fatal("the retired environment built after the push")
	}
}
