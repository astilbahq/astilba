package integration

import (
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
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func TestPreviewIsolationForkApprovalAndClosure(t *testing.T) { previewIsolationJourney(t, false) }
func TestPreviewCleanupReceiptSurvivesControllerRecovery(t *testing.T) {
	previewIsolationJourney(t, true)
}
func previewIsolationJourney(t *testing.T, transfer bool) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var head atomic.Value
	head.Store(strings.Repeat("a", 40))
	var open atomic.Bool
	open.Store(true)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, "/branches/"):
			json.NewEncoder(w).Encode(map[string]any{"commit": map[string]string{"sha": head.Load().(string)}})
		case strings.HasSuffix(r.URL.Path, "/pulls/7"):
			state := "open"
			if !open.Load() {
				state = "closed"
			}
			json.NewEncoder(w).Encode(map[string]any{"number": 7, "state": state, "base": map[string]any{"ref": "main", "repo": map[string]any{"id": 12}}, "head": map[string]any{"sha": head.Load().(string), "repo": map[string]any{"id": 99}}})
		case strings.HasSuffix(r.URL.Path, "/pulls"):
			rows := []map[string]int{}
			if open.Load() {
				rows = append(rows, map[string]int{"number": 7})
			}
			json.NewEncoder(w).Encode(rows)
		default:
			json.NewEncoder(w).Encode(map[string]any{"id": 12, "private": true})
		}
	}))
	defer provider.Close()
	var connection struct {
		Connection pipeline.Connection `json:"connection"`
		Secret     string              `json:"webhookSecret"`
	}
	b.call("POST", prefix+"/git-connections", map[string]any{"name": "Preview source", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app", "api": map[string]string{"username": "api", "password": "preview-api-canary"}, "clone": map[string]string{"username": "reader", "password": "preview-clone-canary"}}, 201, &connection)
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Preview project"}, 201, &project)
	base := prefix + "/projects/" + project.Id.String() + "/environments"
	var production, template api.Environment
	b.call("POST", base, map[string]string{"name": "Production"}, 201, &production)
	b.call("POST", base+"/"+production.Id.String()+"/secrets", map[string]string{"name": "PRODUCTION", "value": "production-secret-canary"}, 201, nil)
	b.call("POST", base, map[string]string{"name": "PR template", "purpose": "preview_template"}, 201, &template)
	path := base + "/" + template.Id.String()
	var runtime, password api.SecretVersion
	b.call("POST", path+"/secrets", map[string]string{"name": "APP_TOKEN", "value": "preview-runtime-canary"}, 201, &runtime)
	b.call("POST", path+"/secrets", map[string]string{"name": "DB_PASSWORD", "value": "template-password-canary"}, 201, &password)
	app, pg, volume := uuid.New(), uuid.New(), uuid.New()
	image := pipelineTestImage
	pgImage := domain.PostgresImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "App", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}, {ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgImage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: password.Id}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(pgImage)}}}}, Variables: []domain.Variable{{Name: "APP_TOKEN", SecretVersionID: &runtime.Id}}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Variable: "DATABASE_URL"}}}
	var revision api.Revision
	b.call("PUT", path+"/draft", map[string]any{"baseRevisionId": template.DraftRevisionId, "specification": spec}, 200, &revision)
	b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision.Id, ServerId: uuid.New()}, 403, nil)
	var enrollment api.Enrollment
	b.call("POST", prefix+"/servers", map[string]any{"name": "Preview host", "role": "combined", "previewOnly": true}, 201, &enrollment)
	credential, _ := cryptobox.Token()
	yes := true
	input := api.AgentEnrollment{ServerId: enrollment.Server.Id, Token: enrollment.Token, Credential: credential, MachineId: uuid.New(), Capabilities: api.AgentCapabilities{Protocol: 1, Version: "preview-fixture", Os: "linux", Arch: "arm64", Docker: true, Build: &yes, BuildControls: &yes, BuildCache: &yes, Visual: &yes, Previews: &yes, ServiceOutputs: &yes, SafeReleases: &yes, CredentialFreeVisualCancel: &yes}, Preflight: api.HostPreflight{Ready: true, OsRelease: "ubuntu 24.04", FreeBytes: 8 << 30, Notes: []string{}}}
	input.Preflight.ExistingContainers = 1
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 400, nil)
	input.Preflight.ExistingContainers = 0
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
	token := enrollment.Server.Id.String() + "." + credential
	cfg := pipeline.BuildConfig{Builder: "dockerfile", ConnectionID: connection.Connection.ID, Branch: "main", RunnerID: enrollment.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, Budget: &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}}
	policyConfig := map[string]any{"baseRevisionId": revision.Id, "applicationId": app, "build": cfg, "maxActive": 1, "maxConcurrentBuilds": 1, "limits": map[string]int{"cpus": 1, "memoryMiB": 256, "pids": 64}, "closeGraceHours": 24, "generatedDomains": false}
	var policy api.PreviewPolicy
	b.call("PUT", path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": policyConfig}, 200, &policy)
	hook := func(delivery string) {
		t.Helper()
		body := []byte(`{"number":7,"repository":{"id":12}}`)
		mac := hmac.New(sha256.New, []byte(connection.Secret))
		mac.Write(body)
		r, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.Connection.ID.String(), strings.NewReader(string(body)))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Gitea-Event", "pull_request")
		r.Header.Set("X-Gitea-Delivery", delivery)
		r.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
		response, err := f.server.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 202 {
			t.Fatal("hook", response.StatusCode)
		}
	}
	wait := func(state string, previous ...uuid.UUID) api.PullRequestPreview {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		var list []api.PullRequestPreview
		for time.Now().Before(deadline) {
			list = nil
			b.call("GET", path+"/previews", nil, 200, &list)
			if len(list) > 0 && list[0].State == state && (len(previous) == 0 || list[0].OperationId != nil && *list[0].OperationId != previous[0]) {
				return list[0]
			}
			time.Sleep(30 * time.Millisecond)
		}
		t.Fatalf("preview did not reach %s: %+v", state, list)
		return api.PullRequestPreview{}
	}
	// No hook: policy discovery must find the fork and allocate nothing yet.
	preview := wait("approval_required")
	if preview.EnvironmentId != nil {
		t.Fatal("fork allocated before approval")
	}
	action := func(kind string, code int) {
		t.Helper()
		b.call("POST", path+"/previews/"+preview.Id.String()+"/actions", map[string]any{"action": kind, "head": preview.Head, "headRepository": preview.HeadRepository}, code, nil)
	}
	head.Store(strings.Repeat("b", 40))
	action("approve", 409)
	action("refresh", 200)
	deadline := time.Now().Add(10 * time.Second)
	for preview.Head != head.Load().(string) && time.Now().Before(deadline) {
		time.Sleep(30 * time.Millisecond)
		preview = wait("approval_required")
	}
	action("approve", 200)
	preview = wait("building")
	if preview.EnvironmentId == nil {
		t.Fatal("approved preview has no environment")
	}

	var sentinel uuid.UUID
	if !transfer {
		// Workspace archives can preserve colliding environment IDs. A preview
		// closure must not cancel another workspace's operation with that same ID.
		other, projectID, revisionID, unitID, serverID := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
		sentinel = uuid.New()
		tx, err := f.st.Pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		statements := []struct {
			sql  string
			args []any
		}{
			{"INSERT INTO workspaces(id,name) VALUES($1,'Sentinel workspace')", []any{other}},
			{"INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'owner')", []any{other, f.owner}},
			{"INSERT INTO projects(workspace_id,id,name) VALUES($1,$2,'Sentinel project')", []any{other, projectID}},
			{"INSERT INTO environments(workspace_id,project_id,id,name,draft_revision_id) VALUES($1,$2,$3,'Sentinel environment',$4)", []any{other, projectID, *preview.EnvironmentId, revisionID}},
			{`INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) VALUES($1,$2,$3,$4,'{"version":1,"applications":[],"variables":[]}',decode(repeat('00',32),'hex'))`, []any{other, *preview.EnvironmentId, revisionID, f.owner}},
			{"INSERT INTO deployment_units(workspace_id,id,environment_id) VALUES($1,$2,$3)", []any{other, unitID, *preview.EnvironmentId}},
			{"INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Sentinel server','deployment')", []any{serverID, other}},
			{`INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,$4,1,$5,'{}','test','dispatched')`, []any{sentinel, other, serverID, *preview.EnvironmentId, uuid.New()}},
		}
		for _, statement := range statements {
			if _, err = tx.Exec(ctx, statement.sql, statement.args...); err != nil {
				tx.Rollback(ctx)
				t.Fatal(err)
			}
		}
		if err = tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}
	previewPath := base + "/" + preview.EnvironmentId.String()
	b.call("PUT", previewPath+"/draft", map[string]any{"baseRevisionId": revision.Id, "specification": spec}, 403, nil)
	var draft api.Revision
	b.call("GET", previewPath+"/draft", nil, 200, &draft)
	var cloned domain.Specification
	json.Unmarshal(draft.Specification, &cloned)
	for _, a := range cloned.Applications {
		if a.ID == app || a.ID == pg {
			t.Fatal("template app ID reused")
		}
	}
	if len(cloned.Volumes) != 1 || cloned.Volumes[0].ID == volume {
		t.Fatal("template storage reused")
	}
	poll := func() api.ExecutionCommand {
		t.Helper()
		var p api.AgentPoll
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/poll", token, nil, 200, &p)
		if p.Command == nil {
			t.Fatal("preview command missing")
		}
		return *p.Command
	}
	report := func(c api.ExecutionCommand, result *pipeline.Result, observation *deployment.Observation, state ...api.ExecutionState) {
		t.Helper()
		r := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
		if len(state) > 0 {
			r.State = state[0]
		}
		if result != nil {
			raw, _ := json.Marshal(result)
			msg := json.RawMessage(raw)
			r.Pipeline = &msg
		}
		if observation != nil {
			raw, _ := json.Marshal(observation)
			msg := json.RawMessage(raw)
			r.Observation = &msg
		}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/report", token, r, 204, nil)
	}
	build := poll()
	if !transfer {
		var saved api.PreviewPolicy
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": policyConfig}, 200, &saved)
		if saved.Generation != policy.Generation {
			t.Fatal("unchanged enabled policy invalidated active previews")
		}
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": false, "generation": policy.Generation, "configuration": policyConfig}, 200, nil)
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": policyConfig}, 200, nil)
		cancelled := poll()
		if cancelled.Id != build.Id || !cancelled.CancelRequested {
			t.Fatal("pause did not retain cancellation until acknowledgement")
		}
		report(build, nil, nil, "cancelled")
		preview = wait("building", build.Id)
		build = poll()

		// A replay after cancellation acknowledgement must preserve resume intent.
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": false, "generation": policy.Generation, "configuration": policyConfig}, 200, nil)
		report(build, nil, nil, "cancelled")
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": false, "generation": policy.Generation, "configuration": policyConfig}, 200, nil)
		b.call("PUT", path+"/preview-policy", map[string]any{"enabled": true, "generation": policy.Generation, "configuration": policyConfig}, 200, nil)
		preview = wait("building", build.Id)
		build = poll()
	} else {
		// Deadline expiry reports cancelled without a lifecycle resume grant.
		report(build, nil, nil, "cancelled")
		preview = wait("failed")
		action("refresh", 200)
		preview = wait("building", build.Id)
		build = poll()
		b.call("POST", prefix+"/operations/"+build.Id.String()+"/cancel", nil, 204, nil)
		report(build, nil, nil, "cancelled")
		preview = wait("failed")
		action("refresh", 200)
		preview = wait("building", build.Id)
		build = poll()
	}

	var command pipeline.Command
	json.Unmarshal(*build.Pipeline, &command)
	if command.Preview == nil || command.Commit != head.Load().(string) || command.CacheScope == pipeline.Hash([]uuid.UUID{ws, project.Id}) {
		t.Fatal("commit/cache isolation failed")
	}
	for _, secret := range []string{"production-secret-canary", "preview-runtime-canary", "template-password-canary", "preview-api-canary"} {
		if strings.Contains(string(*build.Pipeline), secret) {
			t.Fatal("build received unrelated secret")
		}
	}
	if !strings.Contains(string(*build.Pipeline), "preview-clone-canary") {
		t.Fatal("clone access missing")
	}
	result := pipeline.Result{Commit: command.Commit, Image: "astilba-build-" + build.Id.String() + ":built", Digest: "sha256:" + strings.Repeat("c", 64), Architecture: "arm64"}
	report(build, &result, nil)
	preview = wait("deploying")
	resolve := poll()
	if !transfer {
		open.Store(false)
		hook("close-during-resolution")
		preview = wait("closing")
		open.Store(true)
		hook("reopen-during-resolution")
		report(resolve, nil, nil, "cancelled")
		preview = wait("deploying", resolve.Id)
		resolve = poll()
	}

	var payload deployment.Payload
	json.Unmarshal(*resolve.Visual, &payload)
	images := map[string]string{}
	for _, a := range payload.Plan.Specification.Applications {
		if a.Postgres != nil {
			images[a.ID.String()] = *a.Source.Image
		} else {
			images[a.ID.String()] = result.Digest
		}
	}
	report(resolve, nil, &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("d", 64)})
	deploy := poll()
	if !transfer {
		open.Store(false)
		hook("close-during-deployment")
		preview = wait("closing")
		open.Store(true)
		hook("reopen-during-deployment")
		report(deploy, nil, nil, "cancelled")
		preview = wait("deploying", deploy.Id)
		resolve = poll()
		report(resolve, nil, &deployment.Observation{ObservedAt: time.Now(), Resources: []deployment.Resource{}, Images: images, StateHash: strings.Repeat("d", 64)})
		deploy = poll()
	}

	json.Unmarshal(*deploy.Visual, &payload)
	if payload.Action != "deploy" || payload.Plan.Preview == nil || payload.Plan.Preview.Limits.MemoryMiB != 256 {
		t.Fatal("preview limits missing")
	}
	values := []string{}
	for _, v := range payload.Secrets {
		values = append(values, v)
	}
	joined := strings.Join(values, "\n")
	if strings.Contains(joined, "production-secret-canary") || strings.Contains(joined, "template-password-canary") || !strings.Contains(joined, "preview-runtime-canary") {
		t.Fatal("runtime secret isolation failed")
	}
	resources := []deployment.Resource{}
	for _, a := range payload.Plan.Specification.Applications {
		resources = append(resources, deployment.Resource{ID: a.ID, Kind: "service", State: "running", Image: payload.Plan.Images[a.ID.String()]})
	}
	report(deploy, nil, &deployment.Observation{ObservedAt: time.Now(), Resources: resources, Images: payload.Plan.Images, Applied: true, StateHash: strings.Repeat("e", 64)})
	preview = wait("ready")

	if !transfer {
		account, err := f.st.Queries.AccountByID(ctx, f.owner)
		if err != nil {
			t.Fatal(err)
		}
		archive, err := (&portability.Service{Store: f.st}).Export(ctx, store.Principal{Account: account, Recent: true, MFA: true}, ws, "preview transfer passphrase")
		if err != nil {
			t.Fatal(err)
		}
		destination := setup(t)
		importer, err := destination.st.Queries.AccountByID(ctx, destination.owner)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = (&portability.Service{Store: destination.st}).Import(ctx, store.Principal{Account: importer, Recent: true, MFA: true}, "preview transfer passphrase", archive, ""); err != nil {
			t.Fatal(err)
		}
		var purpose string
		var enabled, reserved bool
		if err = destination.st.Pool.QueryRow(ctx, "SELECT purpose FROM environments WHERE workspace_id=$1 AND id=$2", ws, *preview.EnvironmentId).Scan(&purpose); err != nil || purpose != "preview" {
			t.Fatal("import lost preview restriction", purpose, err)
		}
		if err = destination.st.Pool.QueryRow(ctx, "SELECT p.enabled,s.preview_only FROM preview_policies p JOIN servers s ON s.id=p.server_id WHERE p.id=$1", policy.Id).Scan(&enabled, &reserved); err != nil || enabled || !reserved {
			t.Fatal("import replayed a grant or lost host reservation", err)
		}
	}

	open.Store(false)
	hook("close")
	hook("close")
	preview = wait("closing")

	if sentinel != uuid.Nil {
		var cancelled bool
		if err := f.st.Pool.QueryRow(ctx, "SELECT cancel_requested FROM execution_operations WHERE id=$1", sentinel).Scan(&cancelled); err != nil || cancelled {
			t.Fatal("preview closure cancelled another workspace's operation", err)
		}
	}
	if preview.CleanupAt == nil || time.Until(*preview.CleanupAt) < 23*time.Hour {
		t.Fatal("closure grace lost")
	}
	retained := *preview.EnvironmentId
	open.Store(true)
	hook("reopen-before-cleanup")
	preview = wait("ready")
	if preview.EnvironmentId == nil || *preview.EnvironmentId != retained || preview.CleanupAt != nil {
		t.Fatal("reopening before cleanup failed to preserve the preview")
	}
	open.Store(false)
	hook("close-again")
	preview = wait("closing")
	if _, err := f.st.Pool.Exec(ctx, "UPDATE preview_requests SET cleanup_at=now()-interval '1 second' WHERE id=$1", preview.Id); err != nil {
		t.Fatal(err)
	}
	hook("cleanup-due")
	preview = wait("deleting")
	cleanup := poll()
	payload = deployment.Payload{}
	json.Unmarshal(*cleanup.Visual, &payload)
	if payload.Action != "preview-cleanup" || len(payload.Secrets) != 0 {
		t.Fatal("cleanup received credentials")
	}

	if transfer {
		// The agent finished cleanup, but the old controller never received its report.
		if err := f.st.PrepareRecovery(ctx, enrollment.Server.Id, 1); err != nil {
			t.Fatal(err)
		}
		var ticket api.TransferTicket
		b.call("POST", prefix+"/servers/"+enrollment.Server.Id.String()+"/transfer", nil, 201, &ticket)
		credential, _ = cryptobox.Token()
		input.Credential = credential
		input.Token = ticket.Token
		input.ControllerGeneration = &ticket.Generation
		proofs := []api.PreviewCleanupReceipt{{OperationId: cleanup.Id, Digest: strings.Repeat("0", 64)}}
		input.Inventory = &api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}, PreviewCleanups: &proofs}
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 403, nil)
		proofs[0].Digest = cleanup.PlanHash
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", input, 200, nil)
		b.call("GET", path+"/preview-policy", nil, 200, &policy)
		if policy.Enabled {
			t.Fatal("recovery replayed a preview execution grant")
		}
	} else {
		report(cleanup, nil, nil)
	}
	preview = wait("deleted")
	var count int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM visual_runtime WHERE environment_id=$1", *preview.EnvironmentId).Scan(&count); err != nil || count != 0 {
		t.Fatal("active inventory retained", count, err)
	}
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secrets WHERE environment_id=$1", production.Id).Scan(&count); err != nil || count != 1 {
		t.Fatal("production touched", count, err)
	}
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2", ws, *preview.EnvironmentId).Scan(&count); err != nil || count != 0 {
		t.Fatal("preview secret material retained after confirmed cleanup", count, err)
	}
	if !transfer {
		open.Store(true)
		hook("reopen-after-cleanup")
		preview = wait("building")
		if preview.EnvironmentId == nil || *preview.EnvironmentId == retained {
			t.Fatal("reopening after cleanup reused the deleted environment")
		}
	}
}
