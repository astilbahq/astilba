package integration

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"

	"github.com/astilbahq/astilba/services/platform/internal/domain"

	"github.com/google/uuid"
)

func TestMetricHistoryIsolationRetentionAndRateBound(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	enrolled, in, token := enrollment(t, f, b, ws, "deployment")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	route := "/api/v1/workspaces/" + ws.String() + "/servers/" + enrolled.Server.Id.String() + "/metrics"
	other, inOther, otherToken := enrollment(t, f, b, ws, "build")
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/enroll", "", inOther, 200, nil)
	value := 30.0
	sample := api.MetricReport{Host: api.HostMetrics{DiskSource: "docker", CpuPercent: &value, Memory: &api.MetricUsage{UsedBytes: 512, TotalBytes: 1024}}, Services: []api.ServiceMetrics{}}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", token, sample, 204, nil)
	var history api.MetricHistory
	b.call("GET", route, nil, 200, &history)
	if len(history.Samples) != 1 {
		t.Fatal("sample absent")
	}
	firstSample := history.Samples[0].ReceivedAt
	var wg sync.WaitGroup
	identity, e := f.execution.Authenticate(ctx, enrolled.Server.Id, in.Credential)
	if e != nil {
		t.Fatal(e)
	}
	for range 12 {
		wg.Go(func() {
			if e := f.execution.RecordMetrics(ctx, identity, sample); e != nil {
				t.Error(e)
			}
		})
	}
	wg.Wait()
	b.call("GET", route+"?hours=24", nil, 200, &history)
	if len(history.Samples) == 0 {
		t.Fatal("concurrent reports lost history")
	}
	minutes := map[time.Time]bool{}
	for _, entry := range history.Samples {
		minute := entry.ReceivedAt.Truncate(time.Minute)
		if minutes[minute] {
			t.Fatal("concurrent reports exceeded one sample per minute")
		}
		minutes[minute] = true
	}
	// Neither browser credentials nor another server can write this server's resources.
	b.call("POST", "/agent/v1/metrics", sample, 401, nil)
	bad := sample
	bad.Host.Memory = &api.MetricUsage{UsedBytes: 1025, TotalBytes: 1024}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", token, bad, 400, nil)
	env := envForExecution(t, f, b, ws)
	var unit uuid.UUID
	if e = f.st.Pool.QueryRow(ctx, "SELECT id FROM deployment_units WHERE workspace_id=$1 AND environment_id=$2", ws, env).Scan(&unit); e != nil {
		t.Fatal(e)
	}
	resource := uuid.New()
	if _, e = f.st.Pool.Exec(ctx, "UPDATE deployment_units SET server_id=$1 WHERE id=$2", enrolled.Server.Id, unit); e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO managed_resources(workspace_id,environment_id,unit_id,id) VALUES($1,$2,$3,$4)", ws, env, unit, resource); e != nil {
		t.Fatal(e)
	}
	sample.Services = []api.ServiceMetrics{{ResourceId: resource, UnitId: unit, State: "running"}}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", otherToken, sample, 403, nil)
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", token, sample, 204, nil)
	sample.Services[0].UnitId = uuid.New()
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", token, sample, 403, nil)
	b.call("GET", route+"?hours=2", nil, 400, nil)
	viewerID := uuid.New()
	hash, _ := f.auth.Passwords.Hash(ctx, f.password)
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'metric-viewer@example.test','Viewer',$2)", viewerID, hash); e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'viewer')", ws, viewerID); e != nil {
		t.Fatal(e)
	}
	viewer := f.browser(t)
	viewer.login("metric-viewer@example.test", f.password)
	viewer.call("GET", route, nil, 200, nil)
	// An authenticated account without membership cannot read metrics.
	strangerID := uuid.New()
	if _, e = f.st.Pool.Exec(ctx, "INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'metric-stranger@example.test','Stranger',$2)", strangerID, hash); e != nil {
		t.Fatal(e)
	}
	stranger := f.browser(t)
	stranger.login("metric-stranger@example.test", f.password)
	stranger.call("GET", route, nil, 404, nil)
	b.call("GET", "/api/v1/workspaces/"+uuid.NewString()+"/servers/"+other.Server.Id.String()+"/metrics", nil, 404, nil)
	raw, _ := json.Marshal(api.MetricReport{Host: api.HostMetrics{DiskSource: "agent"}, Services: []api.ServiceMetrics{}})
	if _, e = f.st.Pool.Exec(ctx, `INSERT INTO server_metrics(workspace_id,server_id,received_at,report) SELECT $1,$2,$4::timestamptz-n*interval '1 minute',$3 FROM generate_series(1,1500) n`, ws, enrolled.Server.Id, raw, firstSample); e != nil {
		t.Fatal(e)
	}
	b.call("GET", route+"?hours=24", nil, 200, &history)
	if len(history.Samples) > 1440 {
		t.Fatal("query exceeded history cap")
	}
	var cutoff time.Time
	if e = f.st.Pool.QueryRow(ctx, "SELECT now()-interval '24 hours'").Scan(&cutoff); e != nil {
		t.Fatal(e)
	}
	if e = f.st.PruneHistory(ctx); e != nil {
		t.Fatal(e)
	}
	var expired int
	if e = f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM server_metrics WHERE received_at<=$1", cutoff).Scan(&expired); e != nil || expired != 0 {
		t.Fatal("expired metrics retained", expired, e)
	}
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+enrolled.Server.Id.String()+"/revoke", nil, 204, nil)
	if e = f.execution.RecordMetrics(ctx, identity, api.MetricReport{Host: api.HostMetrics{DiskSource: "agent"}, Services: []api.ServiceMetrics{}}); e != domain.Unauthenticated {
		t.Fatalf("stale authenticated identity bypassed revoke: %v", e)
	}
	agentRequest(t, f.server.Client(), f.server.URL, "/agent/v1/metrics", token, sample, 401, nil)
}
