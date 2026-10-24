package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func TestHostedQueuedExpiryAndBackpressure(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	server, input, _ := enrollment(t, f, b, ws, "deployment")
	if _, e := f.execution.Enroll(ctx, input); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, "UPDATE accounts SET email_verified_at=now() WHERE id=$1", f.owner); e != nil {
		t.Fatal(e)
	}
	account, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	p := store.Principal{Account: account, MFA: true, Recent: true}
	f.st.Hosted = true
	if e = f.st.GrantManagement(ctx, ws, time.Now().Add(time.Hour), 100); e != nil {
		t.Fatal(e)
	}
	// Stop consumption so expiry deterministically occurs after admission and
	// before delivery; this exercises the real persisted queue on restart.
	if e = f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	plan, e := f.execution.Plan(ctx, p, ws, api.PlanRequest{ServerId: server.Server.Id, EnvironmentId: env, Action: "inspect"})
	if e != nil {
		t.Fatal(e)
	}
	op, e := f.execution.Apply(ctx, p, ws, plan.Id, uuid.New())
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()-interval '1 minute' WHERE workspace_id=$1", ws); e != nil {
		t.Fatal(e)
	}
	if e = f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var state string
		if e = f.st.Pool.QueryRow(ctx, "SELECT state FROM execution_operations WHERE id=$1", op.Id).Scan(&state); e != nil {
			t.Fatal(e)
		}
		if state == "cancelled" {
			break
		}
		if state != "queued" || time.Now().After(deadline) {
			t.Fatalf("expired queued command was delivered or never fenced: %s", state)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if e = f.st.GrantManagement(ctx, ws, time.Now().Add(time.Hour), 100); e != nil {
		t.Fatal(e)
	}
	// Distinct servers avoid the existing per-server exclusion and reach the
	// independent workspace limit. None has a delivery job or host attached.
	for range 50 {
		id := uuid.New()
		if _, e = f.st.Pool.Exec(ctx, "INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Queued host','deployment')", id, ws); e != nil {
			t.Fatal(e)
		}
		if _, e = f.st.Pool.Exec(ctx, "INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state) VALUES($1,$2,$3,1,$4,'{}','test','queued')", uuid.New(), ws, id, uuid.New()); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = f.execution.Apply(ctx, p, ws, plan.Id, uuid.New()); e != domain.RateLimited {
		t.Fatalf("workspace backlog did not apply backpressure: %v", e)
	}
}

func TestHistoryRetentionAcrossRepresentativeFleet(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	env := envForExecution(t, f, b, ws)
	var revision uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT draft_revision_id FROM environments WHERE id=$1", env).Scan(&revision); e != nil {
		t.Fatal(e)
	}
	var pipelineID uuid.UUID
	for n := range 5 {
		workspace := ws
		if n > 0 {
			workspace = uuid.New()
			if e := f.st.Queries.InsertWorkspace(ctx, db.InsertWorkspaceParams{ID: workspace, Name: "Fleet fixture"}); e != nil {
				t.Fatal(e)
			}
		}
		for range 5 {
			server := uuid.New()
			if _, e := f.st.Pool.Exec(ctx, "INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Fleet host','deployment')", server, workspace); e != nil {
				t.Fatal(e)
			}
			// 100 historical operations per server, with the maximum 1 KiB
			// note and four 1 KiB diagnostic outputs per completed operation.
			if _, e := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,note,steps,created_at) SELECT gen_random_uuid(),$1,$2,g,gen_random_uuid(),'{}','test','succeeded',repeat('n',1024),jsonb_build_array(jsonb_build_object('output',repeat('l',1024)),jsonb_build_object('output',repeat('l',1024)),jsonb_build_object('output',repeat('l',1024)),jsonb_build_object('output',repeat('l',1024))),now()-interval '31 days' FROM generate_series(1,100) g`, workspace, server); e != nil {
				t.Fatal(e)
			}
			if n == 0 {
				if e := f.st.Pool.QueryRow(ctx, "SELECT id FROM execution_operations WHERE server_id=$1 LIMIT 1", server).Scan(&pipelineID); e != nil {
					t.Fatal(e)
				}
			}
		}
	}
	result, _ := json.Marshal(map[string]any{"logs": strings.Repeat("build log\n", 100), "digest": "retained-artifact-identity"})
	if _, e := f.st.Pool.Exec(ctx, `INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,result,created_at) VALUES($1,$2,$3,$4,$5,'{}',$6,now()-interval '31 days')`, pipelineID, ws, env, uuid.New(), revision, result); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,generation,request_key,command,kind,state,note,created_at) SELECT gen_random_uuid(),workspace_id,id,1001,gen_random_uuid(),'{}','test',CASE row_number() OVER () WHEN 1 THEN 'running' WHEN 2 THEN 'needs_attention' ELSE 'succeeded' END,'retained',CASE row_number() OVER () WHEN 3 THEN now() ELSE now()-interval '31 days' END FROM (SELECT * FROM servers WHERE workspace_id=$1 ORDER BY id LIMIT 3) s`, ws); e != nil {
		t.Fatal(e)
	}
	var before, after, bytes int64
	if e := f.st.Pool.QueryRow(ctx, "SELECT sum(octet_length(note)+octet_length(steps::text)) FROM execution_operations").Scan(&before); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	for range 25 {
		if e := f.st.PruneHistory(ctx); e != nil {
			t.Fatal(e)
		}
	}
	elapsed := time.Since(start)
	var count int
	if e := f.st.Pool.QueryRow(ctx, "SELECT count(*),sum(octet_length(note)+octet_length(steps::text)) FROM execution_operations").Scan(&count, &after); e != nil || count != 2503 || after != 5030 {
		t.Fatalf("retention removed history or kept expired diagnostics: rows=%d bytes=%d err=%v", count, after, e)
	}
	var kept string
	var hasLogs bool
	if e := f.st.Pool.QueryRow(ctx, "SELECT result->>'digest', result ? 'logs' FROM pipeline_runs WHERE id=$1", pipelineID).Scan(&kept, &hasLogs); e != nil || hasLogs || kept != "retained-artifact-identity" {
		t.Fatal("build retention lost artifact metadata or kept logs", e)
	}
	if e := f.st.Pool.QueryRow(ctx, "SELECT pg_database_size(current_database())").Scan(&bytes); e != nil {
		t.Fatal(e)
	}
	t.Logf("Synthetic fleet: 5 workspaces, 25 servers, 2500 completed operations; diagnostic bytes %d -> %d; 25 retention batches in %s; PostgreSQL database bytes %d (includes schema/indexes and unreclaimed space)", before, after, elapsed, bytes)
}
