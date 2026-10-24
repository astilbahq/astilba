package integration

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/alerts"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/google/uuid"
)

func alertOperation(t *testing.T, f *fixture, ws, server, env uuid.UUID, state string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	var generation int
	if err := f.st.Pool.QueryRow(context.Background(), "SELECT COALESCE(max(generation),0)+1 FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&generation); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.Pool.Exec(context.Background(), `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,note) VALUES($1,$2,$3,$4,$5,$6,'{"kind":"visual","visual":{"action":"deploy"}}','visual',$7,'PRIVATE-OPERATION-CANARY')`, id, ws, server, env, generation, uuid.New(), state)
	if err != nil {
		t.Fatal(err)
	}
	return id
}
func alertSweep(t *testing.T, f *fixture, ws uuid.UUID) {
	t.Helper()
	if _, err := f.st.Pool.Exec(context.Background(), "UPDATE workspaces SET alert_scanned_at='1970-01-01' WHERE id=$1", ws); err != nil {
		t.Fatal(err)
	}
	if err := f.apiServer.Alerts.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestAlertInboxIsolationDeduplicationAndRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
	server := enrolled.Server.Id
	env := envForExecution(t, f, b, ws)
	operation := alertOperation(t, f, ws, server, env, "needs_attention")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			if err := f.apiServer.Alerts.Sweep(ctx); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	route := "/api/v1/workspaces/" + ws.String() + "/alerts"
	var inbox api.AlertInbox
	b.call("GET", route, nil, 200, &inbox)
	if len(inbox.Alerts) != 1 || inbox.Alerts[0].Kind != api.DeploymentProblem || inbox.Alerts[0].ResourceId != operation {
		t.Fatalf("missing needs-attention event: %+v", inbox)
	}
	id := inbox.Alerts[0].Id
	if _, err := f.st.Pool.Exec(ctx, "UPDATE execution_operations SET state='failed' WHERE id=$1", operation); err != nil {
		t.Fatal(err)
	}
	alertSweep(t, f, ws)
	b.call("GET", route, nil, 200, &inbox)
	if len(inbox.Alerts) != 1 {
		t.Fatal("same operation alerted again after terminal failure")
	}
	var other api.Workspace
	b.call("POST", "/api/v1/workspaces", map[string]string{"name": "Other"}, 201, &other)
	b.call("POST", "/api/v1/workspaces/"+other.Id.String()+"/alerts/"+id.String()+"/acknowledge", nil, 404, nil)
	viewerID := uuid.New()
	hash, _ := f.auth.Passwords.Hash(ctx, f.password)
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'alert-viewer@example.test','Viewer',$2)", viewerID, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'viewer')", ws, viewerID); err != nil {
		t.Fatal(err)
	}
	viewer := f.browser(t)
	viewer.login("alert-viewer@example.test", f.password)
	viewer.call("GET", route, nil, 200, nil)
	viewer.call("POST", route+"/"+id.String()+"/acknowledge", nil, 403, nil)
	viewer.call("GET", "/api/v1/workspaces/"+ws.String()+"/alert-settings", nil, 403, nil)
	b.call("POST", route+"/"+id.String()+"/acknowledge", nil, 204, nil)
	b.call("GET", route+"?view=attention", nil, 200, &inbox)
	if len(inbox.Alerts) != 0 {
		t.Fatal("acknowledged event remained in attention inbox")
	}
	// Deleting retained event history must not recreate the operation notification.
	if _, err := f.st.Pool.Exec(ctx, "DELETE FROM operational_alerts WHERE id=$1", id); err != nil {
		t.Fatal(err)
	}
	alertSweep(t, f, ws)
	b.call("GET", route, nil, 200, &inbox)
	if len(inbox.Alerts) != 0 {
		t.Fatal("pruned operation event was replayed")
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO operational_alerts(id,workspace_id,kind,resource_id,state,opened_at) SELECT gen_random_uuid(),$1,'build_problem',gen_random_uuid(),'event',now()-n*interval '1 minute' FROM generate_series(1,1005) n`, ws); err != nil {
		t.Fatal(err)
	}
	alertSweep(t, f, ws)
	var count int
	if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE workspace_id=$1", ws).Scan(&count); err != nil || count != 1000 {
		t.Fatal("history cap", count, err)
	}
	b.call("GET", route, nil, 200, &inbox)
	if len(inbox.Alerts) != 100 || inbox.Next == nil {
		t.Fatal("bounded pagination missing")
	}
	next := inbox.Next.String()
	first := inbox.Alerts[99].Id
	b.call("GET", route+"?before="+next, nil, 200, &inbox)
	for _, a := range inbox.Alerts {
		if a.Id == first {
			t.Fatal("cursor repeated previous page")
		}
	}
}
func TestAlertConditionsRequireRealRecovery(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
	server := enrolled.Server.Id
	env := envForExecution(t, f, b, ws)
	now := time.Now().UTC().Truncate(time.Minute).Add(30 * time.Second)
	f.apiServer.Alerts.Now = func() time.Time { return now }
	if _, err := f.st.Pool.Exec(ctx, "UPDATE servers SET last_seen=$2 WHERE id=$1", server, now.Add(-3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	resource, revision := uuid.New(), uuid.New()
	var unit uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE deployment_units SET server_id=$1 WHERE id=$2", server, unit); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"version":1,"applications":[{"id":"` + resource.String() + `"}]}`)
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO specification_revisions(workspace_id,environment_id,id,author_id,specification,content_hash) VALUES($1,$2,$3,$4,$5,$6)", ws, env, revision, f.owner, raw, make([]byte, 32)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "UPDATE environments SET applied_revision_id=$1 WHERE id=$2", revision, env); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO managed_resources(workspace_id,environment_id,unit_id,id) VALUES($1,$2,$3,$4)", ws, env, unit, resource); err != nil {
		t.Fatal(err)
	}
	metrics := api.MetricReport{Host: api.HostMetrics{DiskSource: "docker", Disk: &api.MetricUsage{UsedBytes: 95, TotalBytes: 100}}, Services: []api.ServiceMetrics{}}
	setMetrics := func() {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, "DELETE FROM server_metrics WHERE server_id=$1", server); err != nil {
			t.Fatal(err)
		}
		data, _ := json.Marshal(metrics)
		for i := 0; i < 3; i++ {
			if _, err := f.st.Pool.Exec(ctx, "INSERT INTO server_metrics(workspace_id,server_id,received_at,report) VALUES($1,$2,$3,$4)", ws, server, now.Truncate(time.Minute).Add(-time.Duration(i)*time.Minute), data); err != nil {
				t.Fatal(err)
			}
		}
	}
	active := func(kind string, want int) {
		t.Helper()
		var got int
		if err := f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM operational_alerts WHERE workspace_id=$1 AND kind=$2 AND state='firing'", ws, kind).Scan(&got); err != nil || got != want {
			t.Fatalf("%s active=%d want=%d: %v", kind, got, want, err)
		}
	}
	setMetrics()
	alertSweep(t, f, ws)
	active("server_offline", 1)
	active("disk_docker", 1)
	active("service_runtime", 1)
	metrics.Host.DiskSource = "agent"
	metrics.Host.Disk.UsedBytes = 20
	metrics.Partial = true
	unhealthy := api.MetricUnhealthy
	metrics.Services = []api.ServiceMetrics{{ResourceId: resource, UnitId: unit, State: "running", Health: &unhealthy}}
	setMetrics()
	alertSweep(t, f, ws)
	active("disk_docker", 1)
	active("service_runtime", 0)
	active("service_health", 1)
	metrics.Services[0].Health = nil
	setMetrics()
	alertSweep(t, f, ws)
	active("service_health", 1)
	healthy := api.MetricHealthy
	metrics.Services[0].Health = &healthy
	metrics.Host.DiskSource = "docker"
	metrics.Host.Disk.UsedBytes = 80
	if _, err := f.st.Pool.Exec(ctx, "UPDATE servers SET last_seen=$2 WHERE id=$1", server, now); err != nil {
		t.Fatal(err)
	}
	setMetrics()
	alertSweep(t, f, ws)
	active("service_health", 0)
	active("disk_docker", 0)
	active("server_offline", 0)
	policy := uuid.New()
	failedAt, restoredAt := now.Add(-time.Hour), now.Add(-time.Minute)
	config, _ := json.Marshal(backup.Policy{ID: policy, Enabled: true})
	status := backup.Status{Runs: []backup.Run{{ID: uuid.New(), State: "failed", FinishedAt: &failedAt}, {ID: uuid.New(), State: "succeeded", FinishedAt: &restoredAt, TargetID: uuid.New()}}}
	data, _ := json.Marshal(status)
	if _, err := f.st.Pool.Exec(ctx, "INSERT INTO backup_policies(id,workspace_id,environment_id,server_id,configuration,encrypted_access,status) VALUES($1,$2,$3,$4,$5,'x',$6)", policy, ws, env, server, config, data); err != nil {
		t.Fatal(err)
	}
	alertSweep(t, f, ws)
	active("backup_schedule", 1)
	status.Runs = append(status.Runs, backup.Run{ID: uuid.New(), State: "succeeded", FinishedAt: &now})
	data, _ = json.Marshal(status)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE backup_policies SET status=$2 WHERE id=$1", policy, data); err != nil {
		t.Fatal(err)
	}
	alertSweep(t, f, ws)
	active("backup_schedule", 0)
	// A restarted scheduler cannot give an interrupted run a finish time.
	status.Runs = append(status.Runs, backup.Run{ID: uuid.New(), State: "needs_attention", StartedAt: now.Add(time.Minute)})
	for _, state := range []string{"needs_attention", "failed"} {
		status.Runs[len(status.Runs)-1].State = state
		data, _ = json.Marshal(status)
		if _, err := f.st.Pool.Exec(ctx, "UPDATE backup_policies SET status=$2 WHERE id=$1", policy, data); err != nil {
			t.Fatal(err)
		}
		alertSweep(t, f, ws)
		active("backup_schedule", 1)
	}
}

func TestAlertSweepAllowsConcurrentReportLockOrder(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
	env := envForExecution(t, f, b, ws)
	op := alertOperation(t, f, ws, enrolled.Server.Id, env, "needs_attention")
	report, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer report.Rollback(context.Background())
	if _, err = report.Exec(ctx, "SELECT id FROM servers WHERE id=$1 FOR UPDATE", enrolled.Server.Id); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- f.apiServer.Alerts.Sweep(ctx) }()
	for {
		var waiting bool
		if err = f.st.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE 'SELECT % FROM execution_operations%')").Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Report takes the operation lock after its server lock. The detector must
	// not hold that operation while it waits for this server.
	if _, err = report.Exec(ctx, "SELECT id FROM execution_operations WHERE id=$1 FOR UPDATE NOWAIT", op); err != nil {
		t.Fatal("alert detection inverted the report lock order", err)
	}
	if err = report.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestAlertDeliveryAuthorizationAndRetry(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, _, _ := enrollment(t, f, b, ws, "deployment")
	server := enrolled.Server.Id
	env := envForExecution(t, f, b, ws)
	now := time.Now()
	f.apiServer.Alerts.Now = func() time.Time { return now }
	if _, err := f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=now() WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	settingsRoute := "/api/v1/workspaces/" + ws.String() + "/alert-settings"
	b.call("PUT", settingsRoute, api.SaveAlertSettings{EmailEnabled: true}, 200, nil)
	alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case mail := <-f.mail:
		if !strings.Contains(mail, "Deployment needs review") || strings.Contains(mail, "PRIVATE-OPERATION-CANARY") {
			t.Fatal("unsafe mail payload")
		}
	case <-time.After(time.Second):
		t.Fatal("mail absent")
	}
	alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	if _, err := f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=NULL WHERE id=$1", f.owner); err != nil {
		t.Fatal(err)
	}
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-f.mail:
		t.Fatal("unverified recipient received mail")
	default:
	}
	var hits atomic.Int32
	var fail atomic.Bool
	secret := strings.Repeat("s", 40)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		raw, _ := io.ReadAll(r.Body)
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write([]byte(r.Header.Get("X-Astilba-Timestamp") + "\n"))
		mac.Write(raw)
		if r.Header.Get("X-Astilba-Signature") != "sha256="+hex.EncodeToString(mac.Sum(nil)) || r.Header.Get("Idempotency-Key") == "" || strings.Contains(string(raw), "PRIVATE-OPERATION-CANARY") {
			t.Error("unsafe or unsigned webhook")
		}
		if fail.Load() {
			w.WriteHeader(503)
		} else {
			w.WriteHeader(204)
		}
	}))
	defer hook.Close()
	url := hook.URL + "/events"
	setting := api.SaveAlertSettings{WebhookEnabled: true, WebhookUrl: &url, WebhookSecret: &secret}
	response := b.call("PUT", settingsRoute, setting, 200, nil)
	if strings.Contains(string(response), secret) || strings.Contains(string(response), url) {
		t.Fatal("credential returned")
	}
	alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	// A new settings revision invalidates already queued work.
	b.call("PUT", settingsRoute, setting, 200, nil)
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 0 {
		t.Fatal("obsolete destination received delivery")
	}
	rotated, err := cryptobox.New("rotated")
	if err != nil {
		t.Fatal(err)
	}
	previous := f.st.Keys.Active
	f.st.Keys.Keys[rotated.Active] = rotated.Keys[rotated.Active]
	f.st.Keys.Active = rotated.Active
	if err = f.st.Rewrap(ctx); err != nil {
		t.Fatal(err)
	}
	delete(f.st.Keys.Keys, previous)
	operation := alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatal("webhook absent")
	}
	fail.Store(true)
	retryOp := alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	for range 5 {
		if err := f.apiServer.Alerts.Deliver(ctx); err == nil {
			t.Fatal("failed endpoint counted as success")
		}
		now = now.Add(20 * time.Minute)
	}
	var state string
	var attempts int
	if err := f.st.Pool.QueryRow(ctx, `SELECT d.state,d.attempts FROM alert_deliveries d JOIN operational_alerts a ON a.id=d.alert_id WHERE a.resource_id=$1`, retryOp).Scan(&state, &attempts); err != nil || state != "failed" || attempts != 5 {
		t.Fatal("unbounded retry", state, attempts, err)
	}
	before := hits.Load()
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Fatal("terminal failure retried")
	}
	// Acknowledgement also suppresses an unsent event.
	ackOp := alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	var alertID uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT id FROM operational_alerts WHERE resource_id=$1", ackOp).Scan(&alertID); err != nil {
		t.Fatal(err)
	}
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/alerts/"+alertID.String()+"/acknowledge", nil, 204, nil)
	if err := f.apiServer.Alerts.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Fatal("acknowledged event sent")
	}
	leaseOp := alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE alert_deliveries SET state='leased',attempts=4,next_attempt_at=$2 WHERE alert_id=(SELECT id FROM operational_alerts WHERE resource_id=$1)`, leaseOp, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	restarted := alerts.New(f.st, f.auth.Mail, f.server.URL, true, nil)
	restarted.Now = func() time.Time { return now }
	if err := restarted.Deliver(ctx); err == nil {
		t.Fatal("reclaimed failed lease counted as success")
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT d.state,d.attempts FROM alert_deliveries d JOIN operational_alerts a ON a.id=d.alert_id WHERE a.resource_id=$1`, leaseOp).Scan(&state, &attempts); err != nil || state != "failed" || attempts != 5 {
		t.Fatal("restart lost attempt bound", state, attempts, err)
	}
	expiredOp := alertOperation(t, f, ws, server, env, "failed")
	alertSweep(t, f, ws)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE alert_deliveries SET created_at=$2 WHERE alert_id=(SELECT id FROM operational_alerts WHERE resource_id=$1)`, expiredOp, now.Add(-25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	before = hits.Load()
	if err := restarted.Deliver(ctx); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != before {
		t.Fatal("expired notification sent")
	}
	if err := f.st.Pool.QueryRow(ctx, `SELECT d.state,d.attempts FROM alert_deliveries d JOIN operational_alerts a ON a.id=d.alert_id WHERE a.resource_id=$1`, expiredOp).Scan(&state, &attempts); err != nil || state != "failed" || attempts != 0 {
		t.Fatal("expiry was not terminal", state, attempts, err)
	}
	_ = operation
}
