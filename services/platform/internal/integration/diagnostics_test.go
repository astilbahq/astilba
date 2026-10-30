package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func seedDiagnosticHistory(t *testing.T, f *fixture) (workspace, environment, parent uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	b := f.browser(t)
	workspace = b.login("owner@example.test", f.password).Workspaces[0].Id
	environment = envForExecution(t, f, b, workspace)
	server, plan, parent := uuid.New(), uuid.New(), uuid.New()
	var unit, revision uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, `SELECT d.id,e.draft_revision_id FROM deployment_units d JOIN environments e ON e.id=d.environment_id WHERE e.id=$1`, environment).Scan(&unit, &revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO servers(id,workspace_id,name,role) VALUES($1,$2,'Diagnostic fixture','deployment')`, server, workspace); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,note) VALUES($1,$2,$3,$4,1,$5,'{}','crosshost','succeeded','Command failed: application-note-log-canary')`, parent, workspace, server, environment, uuid.New()); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_requests(id,workspace_id,server_id,environment_id,unit_id,revision_id,operation_id,candidate) VALUES($1,$2,$3,$4,$5,$6,$7,'{}')`, plan, workspace, server, environment, unit, revision, parent); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,'{"configuration":{"logs":"retained-configuration-value"}}')`, plan, workspace, environment); err != nil {
		t.Fatal(err)
	}
	observation := `{"stateHash":"retained-state-hash","resources":[{"id":"` + uuid.NewString() + `","state":"healthy","image":"retained-image","logs":"application-log-canary"}]}`
	receipts := `{"complete/fixture":{"planHash":"retained-plan-hash","checkpointHash":"retained-checkpoint-hash","observation":` + observation + `},"checkpoint/fixture":{"checkpointHash":"retained-checkpoint-only"}}`
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO cross_host_operations(id,workspace_id,environment_id,plan_id,phase,receipts) VALUES($1,$2,$3,$4,'complete',$5)`, parent, workspace, environment, plan, receipts); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,$6)`, workspace, environment, unit, server, plan, observation); err != nil {
		t.Fatal(err)
	}
	inventory := `{"plan":{"configuration":{"logs":"retained-configuration-value"}},"observation":` + observation + `}`
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO cross_host_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,inventory,cpu_millis,memory_mib) VALUES($1,$2,$3,$4,$5,$6,100,64)`, workspace, environment, unit, server, plan, inventory); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,result) VALUES($1,$2,$3,$4,$5,'{"logs":"retained-configuration-value"}','{"logs":"build-log-canary","digest":"retained-artifact-digest"}')`, parent, workspace, environment, uuid.New(), revision); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO supported_upgrades(id,author_id,workspace_id,environment_id,server_id,plan,status) VALUES($1,$2,$3,$4,$5,'{"id":"`+plan.String()+`"}','{"phase":"attention","step":"restore","serving":"none","snapshot":"retained-upgrade-snapshot","note":"Upgrade paused at restore: upgrade-note-log-canary"}')`, uuid.New(), f.owner, workspace, environment, server); err != nil {
		t.Fatal(err)
	}
	return workspace, environment, parent
}

func TestWorkspaceExportOmitsLogsAndPreservesRecoveryDiagnostics(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, _, _ := seedDiagnosticHistory(t, f)
	account, err := f.st.Queries.AccountByID(ctx, f.owner)
	if err != nil {
		t.Fatal(err)
	}
	passphrase := "diagnostic export fixture passphrase"
	encrypted, err := (&portability.Service{Store: f.st}).Export(ctx, store.Principal{Account: account, Recent: true}, ws, passphrase)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := portability.Open(ctx, passphrase, encrypted)
	if err != nil {
		t.Fatal(err)
	}
	var exported portability.Archive
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	for table, preserved := range map[string][]string{
		"execution_operations":  {`"kind":"crosshost"`, `"state":"succeeded"`},
		"visual_runtime":        {"retained-state-hash", "retained-image"},
		"pipeline_runs":         {"retained-artifact-digest", "retained-configuration-value"},
		"cross_host_operations": {"retained-plan-hash", "retained-checkpoint-hash", "retained-checkpoint-only", "retained-state-hash", "retained-image"},
		"cross_host_runtime":    {"retained-state-hash", "retained-image", "retained-configuration-value"},
		"visual_plans":          {"retained-configuration-value"},
		"supported_upgrades":    {`"phase":"attention"`, `"step":"restore"`, `"serving":"none"`, "retained-upgrade-snapshot"},
	} {
		encoded, _ := json.Marshal(exported.Tables[table])
		for _, log := range []string{"application-note-log-canary", "upgrade-note-log-canary", "application-log-canary", "build-log-canary"} {
			if bytes.Contains(encoded, []byte(log)) {
				t.Errorf("%s exported diagnostic %s", table, log)
			}
		}
		for _, value := range preserved {
			if !bytes.Contains(encoded, []byte(value)) {
				t.Errorf("%s omitted metadata %s", table, value)
			}
		}
	}
	tx, err := f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	recovery, err := portability.CaptureRecovery(ctx, tx, ws)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := portability.DecodeRecovery(recovery); err != nil {
		t.Fatal("diagnostic export changed recovery format", err)
	}
	for _, value := range []string{"application-note-log-canary", "upgrade-note-log-canary", "application-log-canary", "build-log-canary"} {
		if !bytes.Contains(recovery, []byte(value)) {
			t.Errorf("internal recovery omitted diagnostic %s", value)
		}
	}
}

func TestMalformedImportedReceiptsDoNotBlockRetention(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	_, _, parent := seedDiagnosticHistory(t, f)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET created_at=now()-interval '31 days' WHERE id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, before, after string }{
		{"scalar resource", `{"complete":{"observation":{"resources":[{"logs":"expired","state":"healthy"},1,null,"text",[],true]}}}`, `{"complete":{"observation":{"resources":[{"state":"healthy"},1,null,"text",[],true]}}}`},
		{"array root", `[{"complete":{"observation":{"resources":[{"logs":"untouched"}]}}}]`, `[{"complete":{"observation":{"resources":[{"logs":"untouched"}]}}}]`},
		{"scalar root", `1`, `1`},
		{"non-array resources", `{"complete":{"observation":{"resources":{"logs":"untouched"}}}}`, `{"complete":{"observation":{"resources":{"logs":"untouched"}}}}`},
		{"scalar receipt", `{"complete":1}`, `{"complete":1}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := f.st.Pool.Exec(ctx, `UPDATE cross_host_operations SET receipts=$2 WHERE id=$1`, parent, test.before); err != nil {
				t.Fatal(err)
			}
			if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET note='expired diagnostic' WHERE id=$1`, parent); err != nil {
				t.Fatal(err)
			}
			if err := f.st.PruneHistory(ctx); err != nil {
				t.Fatal("malformed imported receipts blocked the retention transaction", err)
			}
			var receipts, diagnostics bool
			if err := f.st.Pool.QueryRow(ctx, `SELECT h.receipts=$2::jsonb,o.note='' FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id WHERE h.id=$1`, parent, test.after).Scan(&receipts, &diagnostics); err != nil || !receipts || !diagnostics {
				t.Fatal("retention changed unsupported receipt data or failed to prune diagnostics", receipts, diagnostics, err)
			}
		})
	}
}

func TestCrossHostReceiptLogRetentionIsBounded(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	ws, env, parent := seedDiagnosticHistory(t, f)
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET created_at=now()-interval '31 days' WHERE id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	// Include more than one batch of terminal parents plus an old active parent
	// and a recent terminal parent. Only historical terminal receipts may change.
	if _, err := f.st.Pool.Exec(ctx, `WITH inserted AS (INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,created_at) SELECT gen_random_uuid(),workspace_id,server_id,environment_id,g,gen_random_uuid(),'{}','crosshost',CASE WHEN g=103 THEN 'running' WHEN g%3=0 THEN 'failed' WHEN g%3=1 THEN 'cancelled' ELSE 'succeeded' END,CASE g WHEN 102 THEN now() ELSE now()-interval '31 days' END FROM execution_operations CROSS JOIN generate_series(2,103) g WHERE id=$1 RETURNING id,workspace_id,environment_id) INSERT INTO cross_host_operations(id,workspace_id,environment_id,plan_id,phase,receipts) SELECT i.id,i.workspace_id,i.environment_id,h.plan_id,h.phase,h.receipts FROM inserted i CROSS JOIN cross_host_operations h WHERE h.id=$1`, parent); err != nil {
		t.Fatal(err)
	}
	for pass, remaining := range []int{3, 2} {
		if err := f.st.PruneHistory(ctx); err != nil {
			t.Fatal(err)
		}
		var count, intact int
		if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE jsonb_path_exists(h.receipts,'$.*.observation.resources[*].logs')),count(*) FILTER (WHERE h.receipts->'complete/fixture'->>'planHash'='retained-plan-hash' AND h.receipts->'checkpoint/fixture'->>'checkpointHash'='retained-checkpoint-only' AND h.receipts->'complete/fixture'->'observation'->>'stateHash'='retained-state-hash') FROM cross_host_operations h WHERE workspace_id=$1`, ws).Scan(&count, &intact); err != nil || count != remaining || intact != 103 {
			t.Fatalf("retention pass %d: logs=%d metadata=%d err=%v", pass+1, count, intact, err)
		}
	}
	var protected int
	if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id WHERE h.workspace_id=$1 AND o.generation IN (102,103) AND jsonb_path_exists(h.receipts,'$.*.observation.resources[*].logs')`, ws).Scan(&protected); err != nil || protected != 2 {
		t.Fatal("retention removed active or recent receipt logs", protected, err)
	}
	var live int
	if err := f.st.Pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM visual_runtime WHERE environment_id=$1 AND jsonb_path_exists(observation,'$.resources[*].logs'))+(SELECT count(*) FROM cross_host_runtime WHERE environment_id=$1 AND jsonb_path_exists(inventory,'$.observation.resources[*].logs'))`, env).Scan(&live); err != nil || live != 2 {
		t.Fatal("retention changed current runtime diagnostics", live, err)
	}
}
