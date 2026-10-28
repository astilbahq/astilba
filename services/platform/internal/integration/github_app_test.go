package integration

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/githubapp"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
)

func TestGitHubOnboardingDelegationAndRecoveryBoundaries(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	key, e := rsa.GenerateKey(rand.Reader, 2048)
	if e != nil {
		t.Fatal(e)
	}
	var exchanged, minted, metadata atomic.Int64
	var revoked, notAdmin atomic.Bool
	var challenge string
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/login/oauth/access_token":
			exchanged.Add(1)
			_ = r.ParseForm()
			h := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if r.Form.Get("code") != "fixture-code" || base64.RawURLEncoding.EncodeToString(h[:]) != challenge || r.Form.Get("redirect_uri") != f.server.URL+"/github/callback" {
				w.WriteHeader(401)
				return
			}
			json.NewEncoder(w).Encode(map[string]string{"access_token": "user-proof-canary", "refresh_token": "discard-this-refresh"})
		case r.URL.Path == "/user":
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "login": "repository-admin"})
		case r.URL.Path == "/user/installations":
			json.NewEncoder(w).Encode(map[string]any{"installations": []map[string]any{{"id": 2, "app_id": 1, "account": map[string]string{"login": "team"}}}})
		case r.URL.Path == "/user/installations/2/repositories":
			json.NewEncoder(w).Encode(map[string]any{"repositories": []map[string]any{{"id": 12, "full_name": "team/app", "private": true, "permissions": map[string]bool{"admin": !notAdmin.Load()}}}})
		case r.URL.Path == "/app":
			json.NewEncoder(w).Encode(map[string]any{"id": 1, "client_id": "fixture-client"})
		case r.URL.Path == "/repos/team/app/installation":
			if revoked.Load() {
				w.WriteHeader(404)
				return
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 2, "app_id": 1, "permissions": map[string]string{"contents": "write", "pull_requests": "write"}})
		case r.URL.Path == "/repos/team/app":
			metadata.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": 12, "full_name": "team/app", "private": true, "permissions": map[string]bool{"admin": !notAdmin.Load()}})
		case r.URL.Path == "/repos/team/app/branches/main" || r.URL.Path == "/repos/team/app/branches/other":
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": strings.Repeat("a", 40)}})
		case r.URL.Path == "/app/installations/2/access_tokens":
			var in struct {
				IDs         []int64           `json:"repository_ids"`
				Permissions map[string]string `json:"permissions"`
			}
			_ = json.NewDecoder(r.Body).Decode(&in)
			if len(in.IDs) != 1 || in.IDs[0] != 12 {
				t.Error("unscoped token requested")
				w.WriteHeader(400)
				return
			}
			minted.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"token": "installation-" + in.Permissions["contents"] + "-canary", "expires_at": time.Now().Add(time.Hour), "repositories": []map[string]int{{"id": 12}}, "permissions": in.Permissions})
		case r.URL.Path == "/applications/fixture-client/token":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected GitHub request %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer provider.Close()
	app, e := githubapp.New(githubapp.Config{ID: 1, ClientID: "fixture-client", Slug: "fixture-app", ClientSecret: strings.Repeat("c", 32), WebhookSecret: strings.Repeat("w", 32), PrivateKey: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), APIOrigin: provider.URL, WebOrigin: provider.URL}, f.server.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	f.execution.GitHub = app
	f.execution.Policy.GitHubFixture = provider.URL
	webhook := func(delivery, kind, body string, status int) {
		t.Helper()
		req, _ := http.NewRequest("POST", f.server.URL+"/hooks/github-app", strings.NewReader(body))
		mac := hmac.New(sha256.New, []byte(strings.Repeat("w", 32)))
		_, _ = mac.Write([]byte(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("X-GitHub-Event", kind)
		req.Header.Set("X-GitHub-Delivery", delivery)
		res, e := f.server.Client().Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != status {
			raw, _ := io.ReadAll(res.Body)
			t.Fatalf("webhook status %d: %s", res.StatusCode, raw)
		}
	}
	var start api.GitHubAuthorizationStart
	b.call("POST", prefix+"/github-app/authorizations", nil, 200, &start)
	u, _ := url.Parse(start.AuthorizationUrl)
	state := u.Query().Get("state")
	challenge = u.Query().Get("code_challenge")
	other := f.browser(t)
	other.login("owner@example.test", f.password)
	other.call("POST", "/api/v1/github-app/complete", map[string]string{"state": state, "code": "fixture-code"}, 403, nil)
	if exchanged.Load() != 0 {
		t.Fatal("wrong session reached code exchange")
	}
	var grant execution.GitHubAuthorization
	b.call("POST", "/api/v1/github-app/complete", map[string]string{"state": state, "code": "fixture-code"}, 200, &grant)
	b.call("POST", "/api/v1/github-app/complete", map[string]string{"state": state, "code": "fixture-code"}, 200, nil)
	if exchanged.Load() != 1 {
		t.Fatal("callback retry repeated external code exchange")
	}
	if grant.User.ID != 7 || grant.WorkspaceID != ws {
		t.Fatal("grant lost immutable user or workspace identity")
	}
	grantPath := prefix + "/github-app/authorizations/" + grant.ID.String()
	webhook("old-user-revocation", "github_app_authorization", `{"action":"revoked","sender":{"id":7}}`, 202)
	var userEvents int
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM github_app_deliveries WHERE delivery_id='old-user-revocation'").Scan(&userEvents); e != nil || userEvents != 0 {
		t.Fatal("ordering-only user revocation created stale work", e)
	}
	b.call("GET", grantPath+"/installations", nil, 200, nil)

	// The authorization remains valid across the real five-minute step-up path.
	now := time.Now()
	f.auth.Now = func() time.Time { return now.Add(6 * time.Minute) }
	var session api.Session
	b.call("POST", "/api/v1/auth/reauthenticate", map[string]string{"password": f.password}, 200, &session)
	b.csrf = session.CsrfToken
	b.call("GET", grantPath+"/installations", nil, 200, nil)
	old := f.st.Keys
	next, e := cryptobox.New("next")
	if e != nil {
		t.Fatal(e)
	}
	next.Keys["test"] = old.Keys["test"]
	f.st.Keys = next
	if e = f.st.Rewrap(ctx); e != nil {
		t.Fatal(e)
	}
	delete(next.Keys, "test")
	b.call("GET", grantPath+"/repositories?installationId=2", nil, 200, nil)
	in := map[string]any{"name": "App repository", "repository": "team/app", "repositoryId": 12, "installationId": 2}
	notAdmin.Store(true)
	b.call("POST", grantPath+"/connections", in, 403, nil)
	notAdmin.Store(false)
	in["repositoryId"] = 99
	b.call("POST", grantPath+"/connections", in, 403, nil)
	in["repositoryId"] = 12
	var connection execution.ConnectionSummary
	raw := b.call("POST", grantPath+"/connections", in, 201, &connection)
	if connection.Authentication != "github_app" || strings.Contains(string(raw), "canary") {
		t.Fatal("connection leaked credentials or lost authentication type")
	}
	b.call("GET", grantPath+"/installations", nil, 403, nil)
	list := b.call("GET", prefix+"/git-connections", nil, 200, nil)
	if strings.Contains(string(list), "canary") {
		t.Fatal("list leaked credentials")
	}
	// Importing only the exportable configuration cannot grant this controller's App authority.
	var config, cipher []byte
	if e = f.st.Pool.QueryRow(ctx, "SELECT configuration,encrypted_access FROM git_connections WHERE id=$1", connection.ID).Scan(&config, &cipher); e != nil {
		t.Fatal(e)
	}
	imported := uuid.New()
	var values map[string]any
	_ = json.Unmarshal(config, &values)
	values["id"] = imported.String()
	config, _ = json.Marshal(values)
	access, _ := json.Marshal(pipeline.Access{})
	cipher, e = f.st.Keys.Seal(access, pipeline.EncryptionIdentity(ws, imported, "git"))
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)", imported, ws, config, cipher); e != nil {
		t.Fatal(e)
	}
	list = b.call("GET", prefix+"/git-connections", nil, 200, nil)
	if !strings.Contains(string(list), "reauthorization_required") {
		t.Fatal("imported marker acquired authority")
	}
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "App workflow"}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Production", "configurationOwner": "compose"}, 201, &env)
	path := prefix + "/projects/" + project.Id.String() + "/environments/" + env.Id.String()
	enrolled, input, token := enrollment(t, f, b, ws, "combined")
	yes := true
	input.Capabilities.Build = &yes
	input.Capabilities.Compose = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	source := pipeline.Source{ConnectionID: imported, Branch: "main", RunnerID: enrolled.Server.Id, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": source}, 400, nil)
	source.ConnectionID = connection.ID
	var op api.Operation
	b.call("PUT", path+"/source", map[string]any{"baseRevisionId": env.DraftRevisionId, "configuration": source}, 202, &op)
	before := metadata.Load()
	for range 5 {
		var poll api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &poll)
		if poll.Command == nil || poll.Command.Id != op.Id {
			t.Fatal("App import not delivered")
		}
		payload := string(*poll.Command.Pipeline)
		if !strings.Contains(payload, "installation-read-canary") || strings.Contains(payload, "user-proof-canary") || strings.Contains(payload, "installation-write-canary") {
			t.Fatal("runner received wrong GitHub credential scope")
		}
	}
	if metadata.Load()-before > 1 || minted.Load() != 1 {
		t.Fatalf("repeated active polls amplified GitHub calls: metadata=%d mints=%d", metadata.Load()-before, minted.Load())
	}

	push := fmt.Sprintf(`{"installation":{"id":2},"repository":{"id":12},"ref":"refs/heads/other","after":%q}`, strings.Repeat("a", 40))
	webhook("push-one", "push", push, 202)
	webhook("push-one", "push", push, 202)
	webhook("push-one", "push", push+" ", 409)
	// Stop the unrelated dispatcher before holding its operation row; its
	// workspace lock would block the reconciliation whose lock order is tested.
	var dispatchJobID int64
	if e = f.st.Pool.QueryRow(ctx, "SELECT job_id FROM execution_operations WHERE id=$1", op.Id).Scan(&dispatchJobID); e != nil {
		t.Fatal(e)
	}
	dispatchJob, e := f.execution.Queue.JobCancel(ctx, dispatchJobID)
	if e != nil {
		t.Fatal(e)
	}
	dispatchDeadline := time.Now().Add(8 * time.Second)
	for dispatchJob.State != "cancelled" {
		if time.Now().After(dispatchDeadline) {
			t.Fatal("dispatcher did not stop before lock-order probe")
		}
		time.Sleep(10 * time.Millisecond)
		dispatchJob, e = f.execution.Queue.JobGet(ctx, dispatchJobID)
		if e != nil {
			t.Fatal(e)
		}
	}
	// A processed installation revocation disables the binding, cancels assigned
	// work and permits credential-free polling even while GitHub is unavailable.
	held, e := f.st.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer held.Rollback(ctx)
	if _, e = held.Exec(ctx, "SELECT id FROM execution_operations WHERE id=$1 FOR UPDATE", op.Id); e != nil {
		t.Fatal(e)
	}
	revoked.Store(true)
	webhook("removed", "installation_repositories", `{"action":"removed","installation":{"id":2,"app_id":1}}`, 202)

	// A report owns the operation before its source/binding. Hold that operation
	// and prove cancellation does not take the opposing row locks while waiting.
	lockDeadline := time.Now().Add(8 * time.Second)
	for {
		var waiting bool
		if e = f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'UPDATE execution_operations SET cancel_requested=true%')`).Scan(&waiting); e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		if time.Now().After(lockDeadline) {
			t.Fatal("reconciliation did not reach held operation")
		}
		time.Sleep(10 * time.Millisecond)
	}
	probe, e := f.st.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = probe.Exec(ctx, "SELECT environment_id FROM git_sources WHERE workspace_id=$1 AND environment_id=$2 FOR UPDATE NOWAIT", ws, env.Id); e != nil {
		probe.Rollback(ctx)
		t.Fatal("cancellation inverted report/source lock order", e)
	}
	if _, e = probe.Exec(ctx, "SELECT connection_id FROM github_app_connections WHERE connection_id=$1 FOR UPDATE NOWAIT", connection.ID); e != nil {
		probe.Rollback(ctx)
		t.Fatal("cancellation inverted report/binding lock order", e)
	}
	if e = probe.Rollback(ctx); e != nil {
		t.Fatal(e)
	}
	if e = held.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(8 * time.Second)
	for {
		var active bool
		if e = f.st.Pool.QueryRow(ctx, "SELECT active FROM github_app_connections WHERE connection_id=$1", connection.ID).Scan(&active); e != nil {
			t.Fatal(e)
		}
		if !active {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("revocation fanout did not disable connection")
		}
		time.Sleep(20 * time.Millisecond)
	}
	var poll api.AgentPoll
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &poll)
	if poll.Command == nil || !poll.Command.CancelRequested || strings.Contains(string(*poll.Command.Pipeline), "canary") {
		t.Fatal("revocation did not produce credential-free cancellation")
	}
	// A fresh login never inherits the old authorization nonce.
	b.login("owner@example.test", f.password)
	b.call("POST", "/api/v1/github-app/complete", map[string]string{"state": state, "code": "fixture-code"}, 403, nil)
	if _, e = f.st.Pool.Exec(ctx, "UPDATE github_app_authorizations SET expires_at=now()-interval '1 second'"); e != nil {
		t.Fatal(e)
	}
	if e = f.st.PruneHistory(ctx); e != nil {
		t.Fatal(e)
	}
	var retained int
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM github_app_authorizations").Scan(&retained); e != nil || retained != 0 {
		t.Fatal("expired authorization credentials retained", e)
	}
}
