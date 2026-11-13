package integration

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A workspace whose alert webhook never answers delays only its own alerts: another
// workspace's delivery, due later, is sent while the first attempt still hangs.
func TestAlertWebhookThatHangsDelaysOnlyItsWorkspace(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	slow := b.login("owner@example.test", f.password).Workspaces[0].Id
	var other api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)

	var hanging atomic.Int32
	release := make(chan struct{})
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hanging.Add(1)
		defer hanging.Add(-1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer ok.Close()

	secret := strings.Repeat("s", 40)
	resources := map[uuid.UUID][2]uuid.UUID{}
	for ws, url := range map[uuid.UUID]string{slow: hang.URL + "/events", other.Id: ok.URL + "/events"} {
		b.call("PUT", "/api/v1/workspaces/"+ws.String()+"/alert-settings", api.SaveAlertSettings{WebhookEnabled: true, WebhookUrl: &url, WebhookSecret: &secret}, 200, nil)
		enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
		resources[ws] = [2]uuid.UUID{enrolled.Server.Id, envForExecution(t, f, b, ws)}
	}
	for range 4 {
		alertOperation(t, f, slow, resources[slow][0], resources[slow][1], "failed")
	}
	alertSweep(t, f, slow)
	alertOperation(t, f, other.Id, resources[other.Id][0], resources[other.Id][1], "failed")
	alertSweep(t, f, other.Id)
	// The slow workspace's deliveries all fall due first, as a backlog would.
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE alert_deliveries SET next_attempt_at=next_attempt_at-interval '1 minute' WHERE workspace_id=$1", slow); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.apiServer.Alerts.Deliver(ctx) }()
	// The other workspace's delivery is sent and recorded while the first attempt still hangs.
	deadline := time.Now().Add(3 * time.Second)
	for {
		var sent bool
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT EXISTS(SELECT 1 FROM alert_deliveries WHERE workspace_id=$1 AND state='sent')", other.Id).Scan(&sent); err != nil {
			t.Fatal(err)
		}
		if sent {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("another workspace's hanging webhook delayed this workspace's alert")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if n := hanging.Load(); n != 1 {
		t.Fatalf("the hanging destination had %d attempts in flight, want 1", n)
	}
	// The hanging workspace's owner sees why its delivery failed.
	deadline = time.Now().Add(10 * time.Second)
	for {
		var failure string
		if err := f.st.Pool.QueryRow(context.Background(), "SELECT COALESCE(max(failure),'') FROM alert_deliveries WHERE workspace_id=$1", slow).Scan(&failure); err != nil {
			t.Fatal(err)
		}
		if failure == "The receiver did not answer within 5 seconds." {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the timed-out delivery says %q", failure)
		}
		time.Sleep(50 * time.Millisecond)
	}
	var inbox api.AlertInbox
	b.call("GET", "/api/v1/workspaces/"+slow.String()+"/alerts", nil, 200, &inbox)
	shown := false
	for _, alert := range inbox.Alerts {
		for _, delivery := range alert.Deliveries {
			shown = shown || delivery.Failure == "The receiver did not answer within 5 seconds."
		}
	}
	if !shown {
		t.Fatal("the alert inbox does not show why the delivery failed")
	}
	cancel()
	<-done
}

// pushGitea signs a Gitea push for branch with the connection's own webhook secret, as the
// workspace that owns the connection can.
func pushGitea(t *testing.T, f *fixture, connection uuid.UUID, secret, branch, delivery, commit string) {
	t.Helper()
	body, _ := stablejson.Marshal(map[string]any{"ref": "refs/heads/" + branch, "after": commit, "repository": map[string]int{"id": 12}})
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(body)
	req, _ := http.NewRequest("POST", f.server.URL+"/hooks/git/"+connection.String(), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gitea-Event", "push")
	req.Header.Set("X-Gitea-Delivery", delivery)
	req.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
	res, err := f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 202 {
		t.Fatal("push refused", res.StatusCode)
	}
}

// autoBuildTarget makes a target that builds branch on every push, as saving one does.
func autoBuildTarget(t *testing.T, f *fixture, b *browser, ws, connection, runner uuid.UUID, branch string) {
	t.Helper()
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Builds " + branch}, 201, &project)
	var env api.Environment
	b.call("POST", prefix+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Target"}, 201, &env)
	app := uuid.New()
	image := pipelineTestImage
	spec := domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &image}}}, Variables: []domain.Variable{}}
	b.call("PUT", prefix+"/projects/"+project.Id.String()+"/environments/"+env.Id.String()+"/draft", map[string]any{"baseRevisionId": env.DraftRevisionId, "specification": spec}, 200, nil)
	raw, _ := stablejson.Marshal(pipeline.BuildConfig{ConnectionID: connection, Branch: branch, RunnerID: runner, Context: ".", Dockerfile: "Dockerfile", Architecture: "arm64", Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, AutoBuild: true})
	if _, err := f.st.Pool.Exec(context.Background(), "INSERT INTO build_targets(workspace_id,environment_id,application_id,connection_id,configuration,input_hash,requested_commit,policy_actor,policy_auth_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,1)", ws, env.Id, app, connection, raw, strings.Repeat("a", 64), strings.Repeat("a", 40), f.owner); err != nil {
		t.Fatal(err)
	}
}

// One workspace's signed pushes start work only for branches it tracks, leave one of the Git
// queue's workers free however many hang, and run apart from every other queue. Another
// workspace's push is built while the first workspace's refreshes all hang at a provider.
func TestGitPushFloodDelaysNoOtherWorkspace(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	flooding := b.login("owner@example.test", f.password).Workspaces[0].Id
	var other api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)
	ctx := context.Background()
	commit := strings.Repeat("b", 40)

	release := make(chan struct{})
	var hanging atomic.Int32
	hang := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.Contains(r.URL.Path, "/branches/") {
			answerRepository(w, "gitea", 12, false)
			return
		}
		hanging.Add(1)
		defer hanging.Add(-1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	defer hang.Close()
	defer close(release)
	answer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/branches/gone"):
			w.WriteHeader(http.StatusNotFound)
		case strings.Contains(r.URL.Path, "/branches/"):
			answerBranch(w, "gitea", commit)
		default:
			answerRepository(w, "gitea", 12, false)
		}
	}))
	defer answer.Close()

	type connection struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	setupWorkspace := func(ws uuid.UUID, provider string, branches ...string) connection {
		var c connection
		b.call("POST", "/api/v1/workspaces/"+ws.String()+"/git-connections", map[string]string{"name": "Repository", "provider": "gitea", "baseUrl": provider, "repository": "team/app"}, 201, &c)
		enrolled, in, _ := enrollment(t, f, b, ws, "build")
		yes := true
		in.Capabilities.Build = &yes
		in.Capabilities.BuildNetwork = &yes
		in.Capabilities.RootlessBuilds = &yes
		agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
		for _, branch := range branches {
			autoBuildTarget(t, f, b, ws, c.Connection.ID, enrolled.Server.Id, branch)
		}
		return c
	}
	tracked := []string{"one", "two", "three", "four", "five", "six"}
	flood := setupWorkspace(flooding, hang.URL, tracked...)
	quiet := setupWorkspace(other.Id, answer.URL, "main", "gone")

	for i, branch := range tracked {
		pushGitea(t, f, flood.Connection.ID, flood.WebhookSecret, branch, fmt.Sprintf("tracked-%d", i), commit)
	}
	for i := range 20 {
		pushGitea(t, f, flood.Connection.ID, flood.WebhookSecret, fmt.Sprintf("fabricated-%d", i), fmt.Sprintf("fabricated-%d", i), commit)
	}
	var jobs, apart int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE queue<>'platform_git') FROM river_job WHERE kind='platform_git_refresh_v1' AND args->>'workspace'=$1", flooding.String()).Scan(&jobs, &apart); err != nil {
		t.Fatal(err)
	}
	if jobs != len(tracked) || apart != 0 {
		t.Fatalf("pushes made %d refresh jobs, %d outside the Git queue; want one per tracked branch in it", jobs, apart)
	}
	waitFor := func(what string, done func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !done() {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Three of the four workers hang; the fourth is kept for another workspace.
	waitFor("the flooding workspace's refreshes did not reach the hanging provider", func() bool { return hanging.Load() == 3 })

	pushGitea(t, f, quiet.Connection.ID, quiet.WebhookSecret, "main", "quiet-main", commit)
	waitFor("another workspace's push waited behind the flooding workspace's refreshes", func() bool {
		var runs int
		_ = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE workspace_id=$1", other.Id).Scan(&runs)
		return runs == 1
	})
	if n := hanging.Load(); n != 3 {
		t.Fatalf("the flooding workspace held %d Git workers, want 3", n)
	}

	// A branch the provider no longer has ends its refresh instead of retrying it.
	pushGitea(t, f, quiet.Connection.ID, quiet.WebhookSecret, "gone", "quiet-gone", commit)
	waitFor("a refresh for a missing branch was retried", func() bool {
		var state string
		_ = f.st.Pool.QueryRow(ctx, "SELECT state FROM river_job WHERE kind='platform_git_refresh_v1' AND args->>'branch'='gone'").Scan(&state)
		return state == "completed"
	})
}

// An archive request pays its account's budget before its body is read, and waits for the
// server's archive turn only once it is in and admitted. Another account's import whose body
// never finishes is charged, and refuses no owner's export.
func TestUnfinishedImportIsChargedAndRefusesNoExport(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	owner := f.browser(t)
	ws := owner.login("owner@example.test", f.password).Workspaces[0].Id
	stranger := uuid.New()
	hash, _ := f.auth.Passwords.Hash(f.password)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'archive-stranger@example.test','Stranger',$2)`, stranger, hash); err != nil {
		t.Fatal(err)
	}
	other := f.browser(t)
	other.login("archive-stranger@example.test", f.password)

	// The stranger starts an import and never finishes its body.
	body, unfinished := io.Pipe()
	defer unfinished.Close()
	request, _ := http.NewRequest("POST", f.server.URL+"/api/v1/workspaces/import", body)
	request.Header.Set("Origin", f.server.URL)
	request.Header.Set("X-CSRF-Token", other.csrf)
	request.Header.Set("Content-Type", "application/json")
	answered := make(chan int, 1)
	go func() {
		response, err := other.client.Do(request)
		if err != nil {
			answered <- 0
			return
		}
		response.Body.Close()
		answered <- response.StatusCode
	}()
	if _, err := unfinished.Write([]byte(`{"passphrase":"unique archive test passphrase","archive":"`)); err != nil {
		t.Fatal(err)
	}
	charged := func(scope string) int {
		var attempts int
		err := f.st.Pool.QueryRow(ctx, "SELECT COALESCE((SELECT attempts FROM auth_attempts WHERE scope_hash=$1),0)", auth.Hash(scope)).Scan(&attempts)
		if err != nil {
			t.Fatal(err)
		}
		return attempts
	}
	deadline := time.Now().Add(3 * time.Second)
	for charged("workspace-import:"+stranger.String()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("an unfinished import was not charged to its account")
		}
		time.Sleep(20 * time.Millisecond)
	}

	// A second archive request from the same account is refused at once, without charge.
	other.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": "unique archive test passphrase"}, 503, nil)
	if n := charged("workspace-export:" + stranger.String()); n != 0 {
		t.Fatal("a refused second request was charged", n)
	}
	// The owner's export does not wait behind the unfinished import.
	start := time.Now()
	var archive api.WorkspaceArchive
	owner.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": "unique archive test passphrase"}, 200, &archive)
	if time.Since(start) > archiveWaitBound {
		t.Fatal("the owner's export waited behind another account's unfinished import")
	}
	select {
	case status := <-answered:
		t.Fatal("the unfinished import was answered", status)
	default:
	}
}

// archiveWaitBound is well under the server's archive wait, so an export that only got its turn
// after waiting for it fails the test.
const archiveWaitBound = 5 * time.Second

// A workspace's signed pull-request events, however many and whatever numbers they name, leave a
// bounded number of preview jobs waiting, and those past its first few wait behind every other
// workspace's Git work.
func TestPullRequestEventFloodIsBounded(t *testing.T) {
	h := newHungPreview(t)
	// With the workers stopped, jobs wait as they would behind a slow provider. Past the event
	// limit, events add nothing; the one discovery job is the policy's own, queued on saving it.
	if err := h.f.execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	event := func(number int) {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"number":%d,"repository":{"id":12}}`, number))
		mac := hmac.New(sha256.New, []byte(h.secret))
		mac.Write(raw)
		request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gitea-Event", "pull_request")
		request.Header.Set("X-Gitea-Delivery", uuid.NewString())
		request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
		response, err := h.f.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 202 {
			t.Fatal("event refused", response.StatusCode)
		}
	}
	for number := 1000; number < 1120; number++ {
		event(number)
	}
	event(1000)
	var numbered, discovery, normal, total int
	if err := h.f.st.Pool.QueryRow(t.Context(), `SELECT count(*) FILTER (WHERE (args->>'number')::int<>0),count(*) FILTER (WHERE (args->>'number')::int=0 AND args->>'request'=$2),
 count(*) FILTER (WHERE priority=1),count(*) FROM river_job WHERE kind='platform_preview_v1' AND finalized_at IS NULL AND metadata @> jsonb_build_object('workspace',$1::text)`, h.ws.String(), uuid.Nil.String()).Scan(&numbered, &discovery, &normal, &total); err != nil {
		t.Fatal(err)
	}
	if numbered != 50 || discovery != 1 {
		t.Fatalf("120 numbers and a repeat left %d event jobs and %d discovery jobs waiting, want 50 and 1", numbered, discovery)
	}
	if normal != 4 || total != numbered+discovery {
		t.Fatalf("%d of the workspace's %d waiting jobs kept normal priority, want 4", normal, total)
	}
}

// A job left running by a client that stopped mid-run holds no worker of its workspace's for
// longer than the queue's longest timeout: the workspace's next refresh runs.
func TestCrashedGitJobReleasesItsWorkspacesTurn(t *testing.T) {
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	ctx := context.Background()
	commit := strings.Repeat("b", 40)
	answer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/branches/") {
			answerBranch(w, "gitea", commit)
			return
		}
		answerRepository(w, "gitea", 12, false)
	}))
	defer answer.Close()
	var c struct {
		Connection    pipeline.Connection `json:"connection"`
		WebhookSecret string              `json:"webhookSecret"`
	}
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/git-connections", map[string]string{"name": "Repository", "provider": "gitea", "baseUrl": answer.URL, "repository": "team/app"}, 201, &c)
	enrolled, in, _ := enrollment(t, f, b, ws, "build")
	yes := true
	in.Capabilities.Build = &yes
	in.Capabilities.BuildNetwork = &yes
	in.Capabilities.RootlessBuilds = &yes
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	autoBuildTarget(t, f, b, ws, c.Connection.ID, enrolled.Server.Id, "main")
	// Three of the workspace's jobs were running when their client stopped, longer ago than any
	// job in the queue may run.
	for i := range 3 {
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO river_job(state,attempt,attempted_at,args,kind,max_attempts,metadata,priority,queue,scheduled_at)
 VALUES('running',1,now()-interval '75 seconds',$1,'platform_git_refresh_v1',100,jsonb_build_object('workspace',$2::text),1,'platform_git',now()-interval '75 seconds')`,
			fmt.Sprintf(`{"workspace":%q,"connection":%q,"branch":"stale-%d"}`, ws, c.Connection.ID, i), ws.String()); err != nil {
			t.Fatal(err)
		}
	}
	pushGitea(t, f, c.Connection.ID, c.WebhookSecret, "main", "after-crash", commit)
	deadline := time.Now().Add(5 * time.Second)
	for {
		var runs int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pipeline_runs WHERE workspace_id=$1", ws).Scan(&runs); err != nil {
			t.Fatal(err)
		}
		if runs == 1 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs a stopped client left running kept the workspace's refresh waiting")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A request event adds its preview job unless the same job is about to run at the same priority
// or sooner. A twin waiting out a retry delay, or one demoted behind other workspaces, would hold
// the event's work back; and jobs that discovery queued do not count towards the event limit.
func TestPullRequestEventsJoinOnlyJobsAboutToRun(t *testing.T) {
	h := newHungPreview(t)
	if err := h.f.execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	var policy uuid.UUID
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT id FROM preview_policies WHERE workspace_id=$1", h.ws).Scan(&policy); err != nil {
		t.Fatal(err)
	}
	queue := func(number int, state string, priority int, delay string) {
		t.Helper()
		if _, err := h.f.st.Pool.Exec(t.Context(), `INSERT INTO river_job(state,attempt,args,kind,max_attempts,metadata,priority,queue,scheduled_at)
 VALUES($1,1,jsonb_build_object('policy',$2::text,'request',$3::text,'number',$4::int),'platform_preview_v1',20,jsonb_build_object('workspace',$5::text),$6,'platform_git',now()+$7::interval)`,
			state, policy.String(), uuid.Nil.String(), number, h.ws.String(), priority, delay); err != nil {
			t.Fatal(err)
		}
	}
	event := func(number int) {
		t.Helper()
		raw := []byte(fmt.Sprintf(`{"number":%d,"repository":{"id":12}}`, number))
		mac := hmac.New(sha256.New, []byte(h.secret))
		mac.Write(raw)
		request, _ := http.NewRequest("POST", h.f.server.URL+"/hooks/git/"+h.connection.ID.String(), bytes.NewReader(raw))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Gitea-Event", "pull_request")
		request.Header.Set("X-Gitea-Delivery", uuid.NewString())
		request.Header.Set("X-Gitea-Signature", hex.EncodeToString(mac.Sum(nil)))
		response, err := h.f.server.Client().Do(request)
		if err != nil {
			t.Fatal(err)
		}
		response.Body.Close()
		if response.StatusCode != 202 {
			t.Fatal("event refused", response.StatusCode)
		}
	}
	soon := func(number int) bool {
		t.Helper()
		var found bool
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_preview_v1' AND state='available' AND priority=1 AND (args->>'number')::int=$1)", number).Scan(&found); err != nil {
			t.Fatal(err)
		}
		return found
	}
	// A twin demoted behind other workspaces, while this workspace has few jobs waiting.
	queue(2001, "available", 4, "0 seconds")
	event(2001)
	if !soon(2001) {
		t.Fatal("an event joined a job demoted behind other workspaces")
	}
	// A twin waiting out a retry delay.
	queue(2002, "retryable", 1, "1 hour")
	event(2002)
	var available bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_preview_v1' AND state='available' AND (args->>'number')::int=2002)").Scan(&available); err != nil || !available {
		t.Fatal("an event joined a job waiting out a retry delay", err)
	}
	// Discovery's jobs, a full page of them, leave room for events.
	for number := 3000; number < 3000+50; number++ {
		queue(number, "available", 4, "0 seconds")
	}
	event(2003)
	var numbered bool
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_preview_v1' AND (args->>'number')::int=2003)").Scan(&numbered); err != nil || !numbered {
		t.Fatal("discovery's jobs counted towards the event limit", err)
	}
}

// Discovery adds no request jobs while its workspace already has a backlog of them, and keeps its
// page for the next cycle.
func TestPreviewDiscoveryWaitsBehindItsWorkspacesBacklog(t *testing.T) {
	h := newHungPreview(t)
	if err := h.f.execution.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	h.b.call("PUT", h.path+"/preview-policy", map[string]any{"enabled": true, "generation": 0, "configuration": h.policy}, 200, nil)
	var policy uuid.UUID
	var page int
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT id,discovery_page FROM preview_policies WHERE workspace_id=$1", h.ws).Scan(&policy, &page); err != nil {
		t.Fatal(err)
	}
	// A slow provider's request jobs, waiting out their snoozes.
	if _, err := h.f.st.Pool.Exec(t.Context(), `INSERT INTO river_job(state,attempt,args,kind,max_attempts,metadata,priority,queue,scheduled_at)
 SELECT 'scheduled',0,jsonb_build_object('policy',$1::text,'request',$2::text,'number',n),'platform_preview_v1',20,jsonb_build_object('workspace',$3::text),4,'platform_git',now()+interval '1 hour'
 FROM generate_series(5000,5099) n`, policy.String(), uuid.Nil.String(), h.ws.String()); err != nil {
		t.Fatal(err)
	}
	if err := h.f.execution.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT count(*) FROM river_job WHERE kind='platform_preview_v1' AND finalized_at IS NULL AND (args->>'number')::int=0").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the policy's discovery never ran")
		}
		time.Sleep(30 * time.Millisecond)
	}
	var added int
	var after int
	var next time.Duration
	if err := h.f.st.Pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM river_job WHERE kind='platform_preview_v1' AND (args->>'number')::int NOT BETWEEN 5000 AND 5099 AND (args->>'number')::int<>0),discovery_page,next_discovery_at-now() FROM preview_policies WHERE id=$1", policy).Scan(&added, &after, &next); err != nil {
		t.Fatal(err)
	}
	if added != 0 || after != page || next < 4*time.Minute {
		t.Fatalf("discovery behind a backlog added %d jobs, moved its page from %d to %d, and comes back in %v", added, page, after, next)
	}
}
