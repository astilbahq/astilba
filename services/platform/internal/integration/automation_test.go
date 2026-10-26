package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pquerna/otp/totp"
)

func automationEnvironment(t *testing.T, f *fixture) (*browser, api.AutomationContext, api.Environment, string) {
	t.Helper()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	base := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", base+"/projects", map[string]string{"name": "Automation"}, 201, &project)
	var env api.Environment
	base += "/projects/" + project.Id.String() + "/environments"
	b.call("POST", base, map[string]string{"name": "Test"}, 201, &env)
	return b, api.AutomationContext{WorkspaceId: ws, ProjectId: project.Id, EnvironmentId: env.Id}, env, base + "/" + env.Id.String()
}

func issueAutomation(t *testing.T, b *browser, path string, capabilities ...string) api.AutomationCredentialCreated {
	t.Helper()
	var out api.AutomationCredentialCreated
	b.call("POST", path+"/automation-credentials", map[string]any{"name": "CI", "expiresAt": time.Now().Add(time.Hour), "capabilities": capabilities}, 201, &out)
	if out.Token == nil || *out.Token == "" {
		t.Fatal("one-time automation credential missing")
	}
	return out
}

func bearerCall(t *testing.T, f *fixture, token, method, path string, input any, status int, out any) []byte {
	t.Helper()
	var raw []byte
	if input != nil {
		raw, _ = json.Marshal(input)
	}
	r, err := http.NewRequest(method, f.server.URL+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set("Authorization", "Bearer "+token)
	r.Header.Set("Content-Type", "application/json")
	res, err := f.server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, err = io.ReadAll(res.Body)
	if err != nil || res.StatusCode != status {
		t.Fatalf("%s %s: status %d, wanted %d; %s (%v)", method, path, res.StatusCode, status, raw, err)
	}
	if res.Header.Get("Cache-Control") != "no-store" || res.Header.Get("Set-Cookie") != "" {
		t.Fatal("automation response cached or established a browser session")
	}
	if out != nil && json.Unmarshal(raw, out) != nil {
		t.Fatal("invalid automation response")
	}
	return raw
}

func TestAutomationScopeLifecycleAndAttribution(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	grant := issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite)
	ctx := context.Background()
	var hash []byte
	if err := f.st.Pool.QueryRow(ctx, "SELECT secret_hash FROM automation_credentials WHERE id=$1", grant.Credential.Id).Scan(&hash); err != nil || len(hash) != 32 || bytes.Contains(hash, []byte(*grant.Token)) {
		t.Fatal("credential is not stored as a one-way hash", err)
	}
	metadata := b.call("GET", path+"/automation-credentials", nil, 200, nil)
	if bytes.Contains(metadata, []byte(*grant.Token)) || bytes.Contains(metadata, []byte("secret_hash")) {
		t.Fatal("metadata disclosed a credential")
	}
	var actual api.AutomationContext
	bearerCall(t, f, *grant.Token, "GET", "/api/v1/automation/context", nil, 200, &actual)
	if actual.EnvironmentId != env.Id || actual.WorkspaceId != scope.WorkspaceId || actual.CredentialId != grant.Credential.Id {
		t.Fatal("context escaped the exact grant")
	}
	writer := issueAutomation(t, b, path, domain.ConfigurationWrite)
	bearerCall(t, f, *writer.Token, "GET", path+"/draft", nil, 403, nil)
	var conflict api.Error
	bearerCall(t, f, *writer.Token, "PUT", path+"/draft", map[string]any{"baseRevisionId": uuid.New(), "specification": map[string]any{"version": 1, "applications": []any{}, "variables": []any{}}}, 409, &conflict)
	if conflict.CurrentRevision != nil {
		t.Fatal("write-only conflict disclosed the current configuration")
	}
	var draft api.Revision
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 200, &draft)
	bearerCall(t, f, *grant.Token, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: env.DraftRevisionId, Specification: draft.Specification}, 200, &draft)
	bearerCall(t, f, *grant.Token, "PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: env.DraftRevisionId, Specification: draft.Specification}, 409, nil)
	var attribution uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT automation_credential_id FROM audit_events WHERE workspace_id=$1 AND action='draft.save' ORDER BY created_at DESC LIMIT 1", scope.WorkspaceId).Scan(&attribution); err != nil || attribution != grant.Credential.Id {
		t.Fatal("mutation lost credential attribution", err)
	}
	for _, denied := range []struct {
		method, endpoint string
		body             any
	}{
		{"GET", strings.Replace(path, env.Id.String(), uuid.NewString(), 1) + "/draft", nil},
		{"GET", strings.Replace(path, scope.ProjectId.String(), uuid.NewString(), 1) + "/draft", nil},
		{"GET", "/api/v1/workspaces/" + scope.WorkspaceId.String() + "/operations", nil},
		{"GET", path + "/automation-credentials", nil},
		{"POST", path + "/secrets", map[string]string{"name": "TOKEN", "value": "blocked"}},
		{"POST", path + "/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft.Id, ServerId: uuid.New()}},
		{"POST", path + "/build-targets/" + uuid.NewString() + "/runs", api.RunConfiguredBuild{BaseRevisionId: draft.Id, IdempotencyKey: uuid.New(), TargetHash: strings.Repeat("a", 64)}},
	} {
		bearerCall(t, f, *grant.Token, denied.method, denied.endpoint, denied.body, 403, nil)
	}
	// A bearer never inherits the ambient owner's cookie permissions.
	r, _ := newRequest(b, "GET", path+"/draft", nil)
	r.Header.Set("Authorization", "Bearer "+*grant.Token)
	res, err := b.client.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("mixed credentials accepted", res.StatusCode)
	}
	b.call("DELETE", path+"/automation-credentials/"+grant.Credential.Id.String(), nil, 204, nil)
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 401, nil)
	changed := issueAutomation(t, b, path, domain.EnvironmentRead)
	if err = f.st.Queries.BumpAuthVersion(ctx, f.owner); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *changed.Token, "GET", path+"/draft", nil, 401, nil)
}

func TestAutomationRechecksAdmissionAfterWorkspaceWait(t *testing.T) {
	f := setup(t)
	b, scope, _, path := automationEnvironment(t, f)
	ctx := context.Background()
	for _, revoke := range []bool{true, false} {
		t.Run(map[bool]string{true: "revoked", false: "expired"}[revoke], func(t *testing.T) {
			grant := issueAutomation(t, b, path, domain.ConfigurationWrite)
			if !revoke {
				if _, err := f.st.Pool.Exec(ctx, "UPDATE automation_credentials SET expires_at=clock_timestamp()+interval '500 milliseconds' WHERE id=$1", grant.Credential.Id); err != nil {
					t.Fatal(err)
				}
			}
			p, err := f.st.AuthenticateAutomation(ctx, *grant.Token)
			if err != nil {
				t.Fatal(err)
			}
			lock, err := f.st.Pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer lock.Rollback(ctx)
			if _, err = lock.Exec(ctx, "SELECT id FROM workspaces WHERE id=$1 FOR UPDATE", scope.WorkspaceId); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			waitCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			go func() {
				result <- f.st.InWorkspaceTx(waitCtx, p, scope.WorkspaceId, true, false, false, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
					_, err := tx.Exec(waitCtx, "UPDATE projects SET name='must not change' WHERE workspace_id=$1 AND id=$2", scope.WorkspaceId, scope.ProjectId)
					return err
				})
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				var waiting bool
				if err = f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%workspaces%')").Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("mutation never reached the workspace lock")
				}
				time.Sleep(10 * time.Millisecond)
			}
			if revoke {
				_, err = lock.Exec(ctx, "UPDATE automation_credentials SET revoked_at=clock_timestamp() WHERE id=$1", grant.Credential.Id)
			} else {
				for {
					var expired bool
					if err = lock.QueryRow(ctx, "SELECT expires_at<clock_timestamp() FROM automation_credentials WHERE id=$1", grant.Credential.Id).Scan(&expired); err != nil || expired {
						break
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if err = lock.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err = <-result; !errors.Is(err, domain.Unauthenticated) {
				t.Fatal("grant survived workspace wait", err)
			}
			var name string
			if err = f.st.Pool.QueryRow(ctx, "SELECT name FROM projects WHERE id=$1", scope.ProjectId).Scan(&name); err != nil || name != "Automation" {
				t.Fatal("rejected mutation committed", err)
			}
		})
	}
}

func TestAutomationConfiguredBuildPreservesAutomaticDeployment(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	ctx := context.Background()
	prefix := "/api/v1/workspaces/" + scope.WorkspaceId.String()
	var head atomic.Value
	head.Store(strings.Repeat("a", 40))
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": head.Load().(string)}})
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": false})
	}))
	defer provider.Close()
	var connection struct {
		Connection    pipeline.Connection
		WebhookSecret string
	}
	b.call("POST", prefix+"/git-connections", map[string]string{"name": "Configured source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	enrolled, in, agentToken := enrollment(t, f, b, scope.WorkspaceId, "combined")
	yes := true
	in.Capabilities.Build, in.Capabilities.BuildControls, in.Capabilities.Visual = &yes, &yes, &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	var draft api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, &draft)
	cfg := pipeline.BuildConfig{ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: true, WatchPaths: []string{"app"}}
	var initial api.Operation
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, &initial)
	poll := func() api.ExecutionCommand {
		t.Helper()
		var out api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", agentToken, nil, 200, &out)
		if out.Command == nil {
			t.Fatal("expected assigned command")
		}
		return *out.Command
	}
	report := func(c api.ExecutionCommand, state api.ExecutionState, result pipeline.Result) {
		t.Helper()
		raw, _ := json.Marshal(result)
		msg := json.RawMessage(raw)
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", agentToken, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: state, Steps: []api.ExecutionStep{}, Pipeline: &msg}, 204, nil)
	}
	report(poll(), "cancelled", pipeline.Result{})
	// The fixture starts from an existing reviewed stateless deployment.
	if _, err := f.st.Pool.Exec(ctx, "UPDATE environments SET applied_revision_id=$2 WHERE id=$1", env.Id, draft.Id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE deployment_units SET server_id=$2 WHERE environment_id=$1", env.Id, enrolled.Server.Id); err != nil {
		t.Fatal(err)
	}
	grant := issueAutomation(t, b, path, domain.BuildRun)
	var targets []api.ConfiguredBuildTarget
	bearerCall(t, f, *grant.Token, "GET", path+"/build-targets", nil, 200, &targets)
	if len(targets) != 1 || targets[0].ApplicationId != app {
		t.Fatal("configured target missing")
	}
	var before []byte
	var actor uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT configuration,policy_actor FROM build_targets WHERE environment_id=$1", env.Id).Scan(&before, &actor); err != nil {
		t.Fatal(err)
	}
	commit := strings.Repeat("b", 40)
	head.Store(commit)
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 403, nil)
	request := api.RunConfiguredBuild{BaseRevisionId: targets[0].BaseRevisionId, IdempotencyKey: uuid.New(), TargetHash: targets[0].TargetHash}
	var run, replay api.Operation
	route := path + "/build-targets/" + app.String() + "/runs"
	bearerCall(t, f, *grant.Token, "POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 403, nil)
	bearerCall(t, f, *grant.Token, "POST", route, request, 202, &run)
	bearerCall(t, f, *grant.Token, "POST", route, request, 202, &replay)
	if run.Id != replay.Id {
		t.Fatal("one-shot build lost idempotency")
	}
	c := poll()
	var payload pipeline.Command
	json.Unmarshal(*c.Pipeline, &payload)
	if payload.Commit != commit || payload.Build.AutoBuild || payload.Build.AutoDeploy {
		t.Fatal("one-shot changed source or acquired policy authority")
	}
	result := pipeline.Result{Commit: commit, Architecture: "arm64", Image: "astilba-build-" + c.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("d", 64)}
	report(c, "succeeded", result)
	bearerCall(t, f, *grant.Token, "GET", path+"/operations/"+run.Id.String(), nil, 200, &replay)
	if replay.State != "succeeded" {
		t.Fatal("branch advance rejected a completed one-shot artifact", replay.State)
	}
	b.call("PUT", path+"/draft", api.DraftUpdate{BaseRevisionId: draft.Id, Specification: draft.Specification}, 200, &draft)
	bearerCall(t, f, *grant.Token, "POST", route, request, 202, &replay)
	if replay.Id != run.Id {
		t.Fatal("draft advancement lost accepted-key recovery")
	}
	var after []byte
	var afterActor uuid.UUID
	var automatic string
	if err := f.st.Pool.QueryRow(ctx, "SELECT configuration,policy_actor,automatic_requested_commit FROM build_targets WHERE environment_id=$1", env.Id).Scan(&after, &afterActor, &automatic); err != nil || !bytes.Equal(before, after) || actor != afterActor || automatic != strings.Repeat("a", 40) {
		t.Fatal("one-shot replaced configured policy or consumed automatic trigger", err)
	}
	var count int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_auto_deployments WHERE workspace_id=$1", scope.WorkspaceId).Scan(&count); err != nil || count != 0 {
		t.Fatal("one-shot deployed", err)
	}
	// A delayed webhook must still build and deploy, even though B is already a
	// successful artifact and the target has watch paths.
	raw, _ := json.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(connection.WebhookSecret))
	mac.Write(raw)
	req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", uuid.NewString())
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	response, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 202 {
		t.Fatal("delayed webhook rejected")
	}
	c = poll()
	payload = pipeline.Command{}
	json.Unmarshal(*c.Pipeline, &payload)
	if c.Kind != "pipeline" || payload.Commit != commit || payload.CompareCommit != "" || !payload.Build.AutoDeploy {
		t.Fatal("one-shot suppressed watched automatic deployment")
	}
	result.Image = "astilba-build-" + c.Id.String() + ":built"
	report(c, "succeeded", result)
	c = poll()
	if c.Kind != "visual" {
		t.Fatal("automatic image review missing")
	}
	bearerCall(t, f, *grant.Token, "GET", path+"/operations/"+c.Id.String(), nil, 403, nil)
	var visual deployment.Payload
	json.Unmarshal(*c.Visual, &visual)
	if visual.Action != "resolve" {
		t.Fatal("automatic deployment skipped review")
	}
	observation := deployment.Observation{Images: map[string]string{app.String(): result.Digest}, Resources: []deployment.Resource{}, StateHash: strings.Repeat("a", 64), ObservedAt: time.Now()}
	data, _ := json.Marshal(observation)
	message := json.RawMessage(data)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", agentToken, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}, Observation: &message}, 204, nil)
	c = poll()
	json.Unmarshal(*c.Visual, &visual)
	if c.Kind != "visual" || visual.Action != "deploy" {
		t.Fatal("configured automatic apply was lost")
	}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", agentToken, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "cancelled", Steps: []api.ExecutionStep{}}, 204, nil)
	bearerCall(t, f, *grant.Token, "GET", path+"/build-targets", nil, 200, &targets)
	draft.Id = targets[0].BaseRevisionId
	head.Store(strings.Repeat("c", 40))
	request.BaseRevisionId = targets[0].BaseRevisionId
	request.IdempotencyKey = uuid.New()
	bearerCall(t, f, *grant.Token, "POST", route, request, 202, &run)
	c = poll()
	b.call("POST", prefix+"/operations/"+run.Id.String()+"/cancel", nil, 204, nil)
	report(c, "cancelled", pipeline.Result{})
	cfg.Arguments["MODE"] = "changed"
	b.call("POST", path+"/builds", map[string]any{"baseRevisionId": draft.Id, "applicationId": app, "requestKey": uuid.New(), "configuration": cfg}, 202, nil)
	result.Commit = head.Load().(string)
	result.Image = "astilba-build-" + c.Id.String() + ":built"
	lateRaw, _ := json.Marshal(result)
	lateMessage := json.RawMessage(lateRaw)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", agentToken, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 3, State: "succeeded", Steps: []api.ExecutionStep{}, Pipeline: &lateMessage}, 409, nil)
	bearerCall(t, f, *grant.Token, "GET", path+"/operations/"+run.Id.String(), nil, 200, &run)
	if run.State != "cancelled" {
		t.Fatal("superseded one-shot became an artifact")
	}
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM build_artifacts WHERE id=$1", run.Id).Scan(&count); err != nil || count != 0 {
		t.Fatal("cancelled one-shot published a late artifact", err)
	}
	bearerCall(t, f, *grant.Token, "POST", route, request, 202, &replay)
	if replay.Id != run.Id {
		t.Fatal("target reconfiguration lost accepted-key recovery")
	}
	report(poll(), "cancelled", pipeline.Result{})
	request.IdempotencyKey = uuid.New()
	bearerCall(t, f, *grant.Token, "POST", route, request, 409, nil)
}

func TestAutomationDelegatedMFAAndHostedAdmission(t *testing.T) {
	f := setup(t)
	b, scope, env, path := automationEnvironment(t, f)
	ctx := context.Background()
	grant := issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite)
	var draft api.Revision
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 200, &draft)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=true WHERE id=$1", scope.WorkspaceId); err != nil {
		t.Fatal(err)
	}
	request := api.DraftUpdate{BaseRevisionId: env.DraftRevisionId, Specification: draft.Specification}
	bearerCall(t, f, *grant.Token, "PUT", path+"/draft", request, 403, nil)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=false WHERE id=$1", scope.WorkspaceId); err != nil {
		t.Fatal(err)
	}
	var enrollment api.MFAEnrollment
	b.call("POST", "/api/v1/auth/mfa/enroll", nil, 200, &enrollment)
	code, err := totp.GenerateCode(enrollment.Secret, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	var recovery api.RecoveryCodes
	b.call("POST", "/api/v1/auth/mfa/confirm", map[string]string{"code": code}, 200, &recovery)
	b.csrf = recovery.Session.CsrfToken
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 401, nil)
	delegated := issueAutomation(t, b, path, domain.EnvironmentRead, domain.ConfigurationWrite)
	if _, err = f.st.Pool.Exec(ctx, "UPDATE workspaces SET require_mfa=true WHERE id=$1", scope.WorkspaceId); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *delegated.Token, "PUT", path+"/draft", request, 200, &draft)
	request.BaseRevisionId = draft.Id
	f.st.Hosted = true
	var denied api.Error
	bearerCall(t, f, *delegated.Token, "PUT", path+"/draft", request, 400, &denied)
	if denied.Code != "email_verification_required" {
		t.Fatal("automation bypassed hosted email admission")
	}
	if _, err = f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=now() WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *delegated.Token, "PUT", path+"/draft", request, 400, &denied)
	if denied.Code != "subscription_required" {
		t.Fatal("automation bypassed hosted subscription admission")
	}
	if err = f.st.GrantManagement(ctx, scope.WorkspaceId, time.Now().Add(time.Hour), 2); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *delegated.Token, "PUT", path+"/draft", request, 200, nil)
	f.auth.Now = func() time.Time { return time.Now().Add(6 * time.Minute) }
	b.call("POST", path+"/automation-credentials", map[string]any{"name": "New grant", "expiresAt": time.Now().Add(time.Hour), "capabilities": []string{domain.EnvironmentRead}}, 403, nil)
	bearerCall(t, f, *delegated.Token, "GET", path+"/draft", nil, 200, nil)
}

func TestAutomationIsInstallationLocalAndRecoveryRevokesIt(t *testing.T) {
	f := setup(t)
	b, scope, _, path := automationEnvironment(t, f)
	grant := issueAutomation(t, b, path, domain.EnvironmentRead)
	passphrase := "unique automation archive test passphrase"
	var encrypted api.WorkspaceArchive
	b.call("POST", "/api/v1/workspaces/"+scope.WorkspaceId.String()+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
	plain, err := portability.Open(context.Background(), passphrase, encrypted.Archive)
	if err != nil {
		t.Fatal(err)
	}
	var archive portability.Archive
	if json.Unmarshal(plain, &archive) != nil || archive.Version != 4 || archive.Tables["automation_credentials"] != nil || bytes.Contains(plain, []byte(*grant.Token)) {
		t.Fatal("archive exported automation authority or an unexpected format")
	}
	destination := setup(t)
	owner := destination.browser(t)
	owner.login("owner@example.test", destination.password)
	var imported api.Workspace
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, &imported)
	if imported.Id != scope.WorkspaceId {
		t.Fatal("workspace transfer identity changed")
	}
	bearerCall(t, destination, *grant.Token, "GET", path+"/draft", nil, 401, nil)
	var credentials []api.AutomationCredential
	owner.call("GET", path+"/automation-credentials", nil, 200, &credentials)
	if len(credentials) != 0 {
		t.Fatal("installation credentials transferred")
	}
	enrolled, input, _ := enrollment(t, f, b, scope.WorkspaceId, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	if err = f.st.PrepareRecovery(context.Background(), enrolled.Server.Id, 1); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 401, nil)
}

func TestAutomationRequiresCurrentOwnerAndBoundedExpiry(t *testing.T) {
	f := setup(t)
	b, scope, _, path := automationEnvironment(t, f)
	ctx := context.Background()
	for _, expires := range []time.Time{time.Now().Add(10 * time.Second), time.Now().Add(91 * 24 * time.Hour)} {
		b.call("POST", path+"/automation-credentials", map[string]any{"name": "Invalid lifetime", "expiresAt": expires, "capabilities": []string{domain.EnvironmentRead}}, 400, nil)
	}
	grant := issueAutomation(t, b, path, domain.EnvironmentRead)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Name: "Second owner", Email: "second-owner@example.test", PasswordHash: account.PasswordHash})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.Queries.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: scope.WorkspaceId, AccountID: second.ID, Role: "owner"}); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.Pool.Exec(ctx, "UPDATE memberships SET role='operator' WHERE workspace_id=$1 AND account_id=$2", scope.WorkspaceId, f.owner); err != nil {
		t.Fatal(err)
	}
	bearerCall(t, f, *grant.Token, "GET", path+"/draft", nil, 401, nil)
	b.call("GET", path+"/automation-credentials", nil, 403, nil)
}
