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
	"sync/atomic"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// policyFixture is a Git source and two build targets, one that only builds automatically and one
// that also deploys, all approved by a second owner. Each target has an environment of its own, so a
// push builds both at once; the source shares the builder's.
type policyFixture struct {
	f                 *fixture
	owner, approver   *browser
	ws, approverID    uuid.UUID
	path              string
	paths             map[uuid.UUID]string
	connection        uuid.UUID
	secret            string
	builder, deployer uuid.UUID
	commit            atomic.Value
	deliveries        int
}

func newPolicyFixture(t *testing.T) *policyFixture {
	t.Helper()
	f := setup(t)
	p := &policyFixture{f: f, owner: f.browser(t)}
	p.commit.Store(strings.Repeat("0", 40))
	p.ws = p.owner.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + p.ws.String()
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			answerBranch(w, "gitea", p.commit.Load().(string))
			return
		}
		answerRepository(w, "gitea", 12, false)
	}))
	t.Cleanup(provider.Close)
	var connection struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	p.owner.call("POST", prefix+"/git-connections", map[string]string{"name": "Policies", "provider": "gitea", "baseUrl": provider.URL, "repository": "team/app"}, 201, &connection)
	p.connection, p.secret = connection.Connection.ID, connection.WebhookSecret
	enrolled, in, _ := enrollment(t, f, p.owner, p.ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	var project api.Project
	p.owner.call("POST", prefix+"/projects", map[string]string{"name": "Policies"}, 201, &project)
	p.builder, p.deployer = uuid.New(), uuid.New()
	p.paths = map[uuid.UUID]string{}
	envs := map[uuid.UUID]uuid.UUID{}
	image := pipelineTestImage
	for app, name := range map[uuid.UUID]string{p.builder: "builder", p.deployer: "deployer"} {
		var env api.Environment
		p.owner.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": name}, 201, &env)
		p.paths[app], envs[app] = prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String(), env.Id
		spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: name, Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}}
		p.owner.call("PUT", p.paths[app]+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, nil)
	}
	p.path = p.paths[p.builder]

	p.approver, p.approverID = secondOwner(t, f, p.ws, "approver@example.test")
	ctx := context.Background()
	for _, target := range []struct {
		app    uuid.UUID
		deploy bool
	}{{p.builder, false}, {p.deployer, true}} {
		cfg := pipeline.BuildConfig{ConnectionID: p.connection, Branch: "main", RunnerID: enrolled.Server.Id, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true, AutoDeploy: target.deploy}
		raw, _ := stablejson.Marshal(cfg)
		if _, err := f.st.Pool.Exec(ctx, "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor,policy_auth_version,policy_owner) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1,true)", p.ws, envs[target.app], target.app, p.connection, raw, strings.Repeat("a", 64), strings.Repeat("a", 40), p.approverID); err != nil {
			t.Fatal(err)
		}
	}
	// The source tracks a branch of its own, so a push to main exercises only the targets; its
	// approval is read through the API and policy_approver_holds.
	source, _ := stablejson.Marshal(pipeline.Source{ConnectionID: p.connection, Branch: "release", RunnerID: enrolled.Server.Id, Entrypoints: []string{"compose.yaml"}, Paths: []string{"compose.yaml"}, AutoDeploy: true})
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO git_sources(workspace_id,environment_id,connection_id,configuration,status,policy_actor,policy_auth_version) VALUES($1,$2,$3,$4,'ready',$5,1)", p.ws, envs[p.builder], p.connection, source, p.approverID); err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *policyFixture) source(t *testing.T, b *browser) api.GitSourceStatus {
	t.Helper()
	var v api.GitSourceStatus
	b.call("GET", p.path+"/source", nil, 200, &v)
	return v
}

func (p *policyFixture) targets(t *testing.T, b *browser) map[uuid.UUID]api.ConfiguredBuildTarget {
	t.Helper()
	out := map[uuid.UUID]api.ConfiguredBuildTarget{}
	for _, path := range p.paths {
		var list []api.ConfiguredBuildTarget
		b.call("GET", path+"/build-targets", nil, 200, &list)
		for _, v := range list {
			out[v.ApplicationId] = v
		}
	}
	return out
}

func (p *policyFixture) want(t *testing.T, b *browser, source, builder, deployer api.PolicyApproval) {
	t.Helper()
	if got := p.source(t, b).Approval; got != source {
		t.Fatalf("source approval %s, want %s", got, source)
	}
	targets := p.targets(t, b)
	for app, want := range map[uuid.UUID]api.PolicyApproval{p.builder: builder, p.deployer: deployer} {
		if got := targets[app].Approval; got == nil || *got != want {
			t.Fatalf("target %s approval %v, want %s", app, deref(got), want)
		}
	}
}

func (p *policyFixture) role(t *testing.T, role string) {
	t.Helper()
	members := "/api/v1/workspaces/" + p.ws.String() + "/members/" + p.approverID.String()
	if role == "" {
		p.owner.call("DELETE", members, nil, 204, nil)
		return
	}
	p.owner.call("PUT", members, map[string]string{"role": role}, 204, nil)
}

// push delivers a push and waits for its refresh, then reports the build targets it started.
func (p *policyFixture) push(t *testing.T) map[uuid.UUID]bool {
	t.Helper()
	ctx := context.Background()
	if _, err := p.f.st.Pool.Exec(ctx, "UPDATE execution_operations SET state='cancelled',cancel_requested=true WHERE workspace_id=$1", p.ws); err != nil {
		t.Fatal(err)
	}
	var before int64
	if err := p.f.st.Pool.QueryRow(ctx, "SELECT COALESCE(max(id),0) FROM river_job").Scan(&before); err != nil {
		t.Fatal(err)
	}
	var runs int
	if err := p.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE workspace_id=$1", p.ws).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	p.deliveries++
	commit := strings.Repeat(string("0123456789abcdef"[p.deliveries%16]), 40)
	p.commit.Store(commit)
	body, _ := stablejson.Marshal(map[string]any{"ref": "refs/heads/main", "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(p.secret))
	_, _ = mac.Write(body)
	req, _ := http.NewRequest("POST", p.f.server.URL+"/hooks/git/"+p.connection.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", "policy-"+string(rune('a'+p.deliveries)))
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := p.f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatal("webhook rejected", res.StatusCode)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		var done bool
		if err = p.f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM river_job WHERE id>$1 AND kind='platform_git_refresh_v1' AND state='completed')", before).Scan(&done); err != nil {
			t.Fatal(err)
		}
		if done {
			break
		}
		if time.Now().After(deadline) {
			var jobs string
			_ = p.f.st.Pool.QueryRow(ctx, "SELECT COALESCE(string_agg(id||' '||kind||' '||state||' att='||attempt||' created='||created_at||' sched='||scheduled_at||' q='||queue||' '||COALESCE(errors::text,''),'; ' ORDER BY id),'') FROM river_job").Scan(&jobs)
			t.Fatal("refresh did not finish: ", jobs)
		}
		time.Sleep(50 * time.Millisecond)
	}
	rows, err := p.f.st.Pool.Query(ctx, "SELECT application_id FROM pipeline_runs WHERE workspace_id=$1 AND configuration->>'action'='build' ORDER BY created_at OFFSET $2", p.ws, runs)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	started := map[uuid.UUID]bool{}
	for rows.Next() {
		var app uuid.UUID
		if err = rows.Scan(&app); err != nil {
			t.Fatal(err)
		}
		started[app] = true
	}
	return started
}

func (p *policyFixture) approveSource(t *testing.T, b *browser, status int) {
	t.Helper()
	b.call("POST", p.path+"/source/approval", map[string]string{"sourceHash": p.source(t, b).SourceHash}, status, nil)
}

func (p *policyFixture) approveTarget(t *testing.T, b *browser, app uuid.UUID, status int) {
	t.Helper()
	target := p.targets(t, b)[app]
	b.call("POST", p.paths[app]+"/build-targets/"+app.String()+"/approval", map[string]string{"targetHash": target.TargetHash}, status, nil)
}

func TestStandingAutomationPausesWhenItsApproverIsDemoted(t *testing.T) {
	p := newPolicyFixture(t)
	p.want(t, p.owner, api.Current, api.Current, api.Current)
	if started := p.push(t); !started[p.builder] || !started[p.deployer] {
		t.Fatal("approved targets did not build on push", started)
	}

	p.role(t, "operator")
	// An operator may build automatically, but not import or deploy.
	p.want(t, p.owner, api.ApproverRole, api.Current, api.ApproverRole)
	if started := p.push(t); !started[p.builder] || started[p.deployer] {
		t.Fatal("demotion did not pause exactly the deploying target", started)
	}
	for _, action := range []string{"git.source.approval_withdraw", "build.approval_withdraw"} {
		if auditedBy(t, p.f, p.ws, p.f.owner, action) == 0 {
			t.Fatalf("%s was not recorded", action)
		}
	}

	p.role(t, "owner")
	// Ownership given back is new authority: what was paused stays paused.
	p.want(t, p.owner, api.ApprovalRequired, api.Current, api.ApprovalRequired)
	if started := p.push(t); started[p.deployer] {
		t.Fatal("re-promotion revived a paused target")
	}

	// A current owner approves as theirs, which resumes.
	p.approveSource(t, p.owner, 204)
	p.approveTarget(t, p.owner, p.deployer, 204)
	p.want(t, p.owner, api.Current, api.Current, api.Current)
	if started := p.push(t); !started[p.deployer] {
		t.Fatal("an approved target did not resume")
	}
	var actor uuid.UUID
	if err := p.f.st.Pool.QueryRow(context.Background(), "SELECT policy_actor FROM git_sources WHERE workspace_id=$1", p.ws).Scan(&actor); err != nil || actor != p.f.owner {
		t.Fatal("approval did not make the source the approving owner's", err)
	}
}

func TestStandingAutomationPausesWhenItsApproverLeavesOrLosesWrite(t *testing.T) {
	for _, role := range []string{"viewer", ""} {
		t.Run("to "+role, func(t *testing.T) {
			p := newPolicyFixture(t)
			p.role(t, role)
			reason := api.ApproverRole
			if role == "" {
				reason = api.ApproverRemoved
			}
			p.want(t, p.owner, reason, reason, reason)
			if started := p.push(t); len(started) != 0 {
				t.Fatal("a paused target built on push", started)
			}
		})
	}
}

func TestApproverCredentialChangePausesStandingAutomation(t *testing.T) {
	p := newPolicyFixture(t)
	operator, _ := secondOwner(t, p.f, p.ws, "operator@example.test")
	if _, err := p.f.st.Pool.Exec(context.Background(), "UPDATE memberships SET role='operator' WHERE workspace_id=$1 AND account_id=(SELECT id FROM accounts WHERE email='operator@example.test')", p.ws); err != nil {
		t.Fatal(err)
	}
	var renewed api.Session
	p.approver.call("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": p.f.password}}, 200, &renewed)
	p.approver.csrf = renewed.CsrfToken
	// Only owners learn why; others see that it needs approval.
	p.want(t, p.owner, api.ApproverCredentials, api.ApproverCredentials, api.ApproverCredentials)
	p.want(t, operator, api.ApprovalRequired, api.ApprovalRequired, api.ApprovalRequired)
	if started := p.push(t); len(started) != 0 {
		t.Fatal("a target built on an approval from before its approver's credential change", started)
	}
	// An operator can approve automatic building, but not a target that deploys or a source.
	p.approveTarget(t, operator, p.deployer, 403)
	p.approveSource(t, operator, 403)
	p.approveTarget(t, operator, p.builder, 204)
	// The approver can approve again under their new credentials.
	p.approveTarget(t, p.approver, p.deployer, 204)
	p.approveSource(t, p.approver, 204)
	p.want(t, p.owner, api.Current, api.Current, api.Current)
	if started := p.push(t); !started[p.builder] || !started[p.deployer] {
		t.Fatal("approved targets did not resume", started)
	}
}

func TestTurningOffSourceDeploymentApprovesNothing(t *testing.T) {
	p := newPolicyFixture(t)
	p.role(t, "operator")
	p.role(t, "owner")
	p.owner.call("PUT", p.path+"/source/policy", map[string]any{"autoDeploy": false}, 204, nil)
	source := p.source(t, p.owner)
	if source.Approval != api.ApprovalRequired || source.Configuration.AutoDeploy {
		t.Fatal("turning automatic deployment off approved the source or left it on", source.Approval)
	}
	var actor uuid.UUID
	if err := p.f.st.Pool.QueryRow(context.Background(), "SELECT policy_actor FROM git_sources WHERE workspace_id=$1", p.ws).Scan(&actor); err != nil || actor != p.approverID {
		t.Fatal("turning automatic deployment off changed the approver", err)
	}
}

func TestAutomationListsOnlyTargetsItCanRun(t *testing.T) {
	p := newPolicyFixture(t)
	token := *issueAutomation(t, p.owner, p.path, domain.BuildRun).Token
	list := func() int {
		var v []api.ConfiguredBuildTarget
		bearerCall(t, p.f, token, "GET", p.path+"/build-targets", nil, 200, &v)
		for _, target := range v {
			if target.Approval != nil || target.AutomaticBuild != nil || target.Runnable != nil {
				t.Fatal("an automation credential was shown approval details")
			}
		}
		return len(v)
	}
	runnable := func() bool {
		t.Helper()
		v := p.targets(t, p.owner)[p.builder].Runnable
		return v != nil && *v
	}
	if n := list(); n != 1 || !runnable() {
		t.Fatal("automation did not list its approved target", n)
	}
	p.role(t, "operator")
	// The target keeps building for the operator, but running it on demand needs an owner's
	// approval, so it isn't listed, and the owner is told.
	if n := list(); n != 0 || runnable() {
		t.Fatal("automation listed a target no owner approves", n)
	}
	if got := p.targets(t, p.owner)[p.builder].Approval; got == nil || *got != api.Current {
		t.Fatal("demotion to operator paused automatic builds", deref(got))
	}
	bearerCall(t, p.f, token, "POST", p.path+"/build-targets/"+p.builder.String()+"/runs", map[string]any{"baseRevisionId": uuid.New(), "idempotencyKey": uuid.New(), "targetHash": strings.Repeat("0", 64)}, 404, nil)
	p.role(t, "owner")
	// Ownership given back is new authority: the old approval stays withdrawn.
	if n := list(); n != 0 || runnable() {
		t.Fatal("re-promotion revived on-demand runs", n)
	}
	p.approveTarget(t, p.owner, p.builder, 204)
	if n := list(); n != 1 || !runnable() {
		t.Fatal("an owner's approval did not make the target runnable", n)
	}
	// An operator's approval never counts as an owner's, even once they are promoted.
	p.role(t, "operator")
	p.approveTarget(t, p.approver, p.builder, 204)
	p.role(t, "owner")
	if n := list(); n != 0 || runnable() {
		t.Fatal("an operator's approval let automation run the target after promotion", n)
	}
}

func TestSourceApprovalCoversWhatWasShown(t *testing.T) {
	p := newPolicyFixture(t)
	p.role(t, "operator")
	p.role(t, "owner")
	shown := p.source(t, p.owner)
	// Another owner turns automatic deployment off after the source was shown.
	p.approver.call("PUT", p.path+"/source/policy", map[string]any{"autoDeploy": false}, 204, nil)
	p.owner.call("POST", p.path+"/source/approval", map[string]string{"sourceHash": shown.SourceHash}, 409, nil)
	p.approveSource(t, p.owner, 204)
	if source := p.source(t, p.owner); source.Approval != api.Current || source.Configuration.AutoDeploy {
		t.Fatal("approval changed the configuration it was shown", source.Approval)
	}
}

// A fork commit's preview approval and a cleanup request are the owner's who gave them, so they
// end with that owner's ownership or credentials.
func TestPreviewApprovalsEndWithTheirOwner(t *testing.T) {
	for _, end := range []string{"removed", "credentials"} {
		t.Run(end, func(t *testing.T) {
			p := newPolicyFixture(t)
			ctx := context.Background()
			var project, revision, server uuid.UUID
			if err := p.f.st.Pool.QueryRow(ctx, "SELECT e.project_id,e.draft_revision_id,(SELECT id FROM servers WHERE workspace_id=e.workspace_id LIMIT 1) FROM environments e JOIN build_targets b ON b.workspace_id=e.workspace_id AND b.environment_id=e.id WHERE b.application_id=$1", p.builder).Scan(&project, &revision, &server); err != nil {
				t.Fatal(err)
			}
			var env uuid.UUID
			if err := p.f.st.Pool.QueryRow(ctx, "SELECT environment_id FROM build_targets WHERE application_id=$1", p.builder).Scan(&env); err != nil {
				t.Fatal(err)
			}
			policy, request := uuid.New(), uuid.New()
			if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,configuration)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,1,'{}')`, policy, p.ws, project, env, revision, p.builder, p.connection, server, p.f.owner); err != nil {
				t.Fatal(err)
			}
			if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO preview_requests(id,workspace_id,policy_id,number,head,fork,approved_head,approved_by,approved_auth_version,cleanup_requested,cleanup_actor,cleanup_auth_version,cleanup_controller,next_check_at)
 VALUES($1,$2,$3,1,'abc',true,'abc',$4,1,true,$4,1,1,now()+interval '1 day')`, request, p.ws, policy, p.approverID); err != nil {
				t.Fatal(err)
			}
			// The fork approval is read through the query the preview worker uses. A cleanup request
			// is checked by previewCleanupAuthority, which compares the same role and version.
			held := func() (fork, cleanup bool) {
				t.Helper()
				approval, err := db.New(p.f.st.Pool).PreviewForkApproval(ctx, db.PreviewForkApprovalParams{WorkspaceID: p.ws, ID: request})
				if err != nil {
					t.Fatal(err)
				}
				if err = p.f.st.Pool.QueryRow(ctx, "SELECT cleanup_actor IS NOT NULL AND policy_approver_holds(workspace_id,cleanup_actor,cleanup_auth_version,true) FROM preview_requests WHERE id=$1", request).Scan(&cleanup); err != nil {
					t.Fatal(err)
				}
				return approval.Head == "abc", cleanup
			}
			if fork, cleanup := held(); !fork || !cleanup {
				t.Fatal("a current owner's approvals were refused")
			}
			if end == "removed" {
				p.role(t, "")
				for _, action := range []string{"preview.fork_approval_withdraw", "preview.cleanup_withdraw"} {
					if auditedBy(t, p.f, p.ws, p.f.owner, action) == 0 {
						t.Fatalf("%s was not recorded", action)
					}
				}
				var approver, cleaner *uuid.UUID
				if err := p.f.st.Pool.QueryRow(ctx, "SELECT approved_by,cleanup_actor FROM preview_requests WHERE id=$1", request).Scan(&approver, &cleaner); err != nil || approver != nil || cleaner != nil {
					t.Fatal("removal left the owner's preview approvals in place", err)
				}
			} else {
				p.approver.call("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": p.f.password}}, 200, nil)
			}
			if fork, cleanup := held(); fork || cleanup {
				t.Fatal("a preview approval outlived its owner's authority")
			}
		})
	}
}

// An automatic deployment not yet planned stops with its approver's ownership, and doesn't run on
// an approval from before the approver's credential change.
func TestPendingAutomaticDeploymentsNeedTheirApproval(t *testing.T) {
	for _, end := range []string{"removed", "credentials"} {
		t.Run(end, func(t *testing.T) {
			p := newPolicyFixture(t)
			ctx := context.Background()
			if started := p.push(t); !started[p.deployer] {
				t.Fatal("no build to deploy")
			}
			var run uuid.UUID
			if err := p.f.st.Pool.QueryRow(ctx, "SELECT id FROM pipeline_runs WHERE workspace_id=$1 AND application_id=$2", p.ws, p.deployer).Scan(&run); err != nil {
				t.Fatal(err)
			}
			if _, err := p.f.st.Pool.Exec(ctx, `INSERT INTO pipeline_auto_deployments(id,workspace_id,environment_id,revision_id,actor_id,actor_auth_version)
 SELECT r.id,r.workspace_id,r.environment_id,r.revision_id,$2,1 FROM pipeline_runs r WHERE r.id=$1`, run, p.approverID); err != nil {
				t.Fatal(err)
			}
			q := db.New(p.f.st.Pool)
			approved := func() bool {
				t.Helper()
				ok, err := q.AutomaticDeploymentApproved(ctx, db.AutomaticDeploymentApprovedParams{WorkspaceID: p.ws, ID: run})
				if err != nil {
					t.Fatal(err)
				}
				return ok
			}
			if !approved() {
				t.Fatal("a current approval was refused")
			}
			if end == "removed" {
				p.role(t, "")
				var state string
				if err := p.f.st.Pool.QueryRow(ctx, "SELECT state FROM pipeline_auto_deployments WHERE id=$1", run).Scan(&state); err != nil || state != "blocked" {
					t.Fatal("a removed owner's pending automatic deployment was not blocked", state, err)
				}
				if auditedBy(t, p.f, p.ws, p.f.owner, "build.automatic_deployment_block") == 0 {
					t.Fatal("blocking was not recorded")
				}
			} else {
				p.approver.call("POST", "/api/v1/auth/password", map[string]any{"password": "replacement password here", "current": map[string]string{"password": p.f.password}}, 200, nil)
			}
			if approved() {
				t.Fatal("an automatic deployment kept an approval that ended")
			}
		})
	}
}

func deref[T any](v *T) any {
	if v == nil {
		return nil
	}
	return *v
}
