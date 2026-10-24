package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
)

func TestLogExportBoundsAndScope(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	prefix := "/api/v1/workspaces/" + ws.String()
	var project api.Project
	b.call("POST", prefix+"/projects", map[string]string{"name": "Logs"}, 201, &project)
	var env, other api.Environment
	envs := prefix + "/projects/" + project.Id.String() + "/environments"
	b.call("POST", envs, map[string]string{"name": "Production"}, 201, &env)
	b.call("POST", envs, map[string]string{"name": "Other"}, 201, &other)
	path := envs + "/" + env.Id.String() + "/logs/export"
	server, _, _ := enrollment(t, f, b, ws, "deployment")
	resource := uuid.New()
	var unit uuid.UUID
	if err := f.st.Pool.QueryRow(ctx, "SELECT id FROM deployment_units WHERE environment_id=$1", env.Id).Scan(&unit); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-2 * time.Hour)
	observation, _ := json.Marshal(deployment.Observation{ObservedAt: old, Resources: []deployment.Resource{{ID: resource, Logs: "runtime [redacted]", Note: "not log output"}}})
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,observation,observed_at) VALUES($1,$2,$3,$4,$5,$6)`, ws, env.Id, unit, server.Server.Id, observation, old); err != nil {
		t.Fatal(err)
	}
	insert := func(target api.Environment, generation int, at time.Time, text string) {
		t.Helper()
		id := uuid.New()
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,created_at) VALUES($1,$2,$3,$4,$5,$1,'{}','pipeline','succeeded',$6)`, id, ws, server.Server.Id, target.Id, generation, at); err != nil {
			t.Fatal(err)
		}
		result, _ := json.Marshal(map[string]string{"logs": text, "unrelated": "must-not-be-exported"})
		if _, err := f.st.Pool.Exec(ctx, `INSERT INTO pipeline_runs(id,workspace_id,environment_id,application_id,revision_id,configuration,result,created_at) VALUES($1,$2,$3,$4,$5,'{}',$6,$7)`, id, ws, target.Id, resource, target.DraftRevisionId, result, at); err != nil {
			t.Fatal(err)
		}
	}
	insert(env, 1, time.Now().Add(-30*time.Minute), "recent [redacted]")
	insert(env, 2, old, "older [redacted]")
	insert(other, 1, time.Now().Add(-time.Minute), "other-environment-output")
	var out api.LogExport
	b.call("GET", path, nil, 200, &out)
	if len(out.Entries) != 1 || out.Entries[0].Content != "recent [redacted]" || out.Entries[0].OperationId == nil {
		t.Fatalf("wrong default window: %+v", out)
	}
	b.call("GET", path+"?hours=6", nil, 200, &out)
	raw, _ := json.Marshal(out)
	if len(out.Entries) != 3 || strings.Contains(string(raw), "must-not-be-exported") || strings.Contains(string(raw), "other-environment-output") {
		t.Fatalf("export included non-log or foreign data: %s", raw)
	}
	if out.WindowEnd.Sub(out.WindowStart) != 6*time.Hour || out.Truncated {
		t.Fatal("requested window or truncation metadata incorrect")
	}
	b.call("GET", path+"?hours=2", nil, 400, nil)
	b.call("GET", strings.Replace(path, project.Id.String(), uuid.NewString(), 1), nil, 404, nil)
	b.call("GET", strings.Replace(path, ws.String(), uuid.NewString(), 1), nil, 404, nil)
	stranger := uuid.New()
	hash, _ := f.auth.Passwords.Hash(ctx, f.password)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'log-reader@example.test','Reader',$2)`, stranger, hash); err != nil {
		t.Fatal(err)
	}
	reader := f.browser(t)
	reader.login("log-reader@example.test", f.password)
	reader.call("GET", path, nil, 404, nil)
	if _, err := f.st.Pool.Exec(ctx, `INSERT INTO memberships(workspace_id,account_id,role) VALUES($1,$2,'viewer')`, ws, stranger); err != nil {
		t.Fatal(err)
	}
	reader.call("GET", path, nil, 200, nil)
	for i := range 105 {
		content := strings.Repeat("é", 8000) + " [redacted]"
		if i == 0 {
			// Leave an odd byte budget for a later two-byte character.
			content += "!"
		}
		insert(env, i+3, time.Now().Add(-time.Duration(i+1)*time.Second), content)
	}
	b.call("GET", path, nil, 200, &out)
	bytes := 0
	shortened := false
	for _, entry := range out.Entries {
		bytes += len(entry.Content)
		shortened = shortened || entry.Truncated
		if strings.ContainsRune(entry.Content, '\uFFFD') {
			t.Fatal("export split a UTF-8 character")
		}
	}
	if bytes > 65536 || !out.Truncated || !shortened {
		t.Fatalf("byte bound or truncation missing: %d bytes", bytes)
	}
	if _, err := f.st.Pool.Exec(ctx, `UPDATE pipeline_runs SET result='{"logs":"small"}' WHERE workspace_id=$1 AND environment_id=$2`, ws, env.Id); err != nil {
		t.Fatal(err)
	}
	b.call("GET", path, nil, 200, &out)
	if len(out.Entries) != 100 || !out.Truncated {
		t.Fatal("entry bound or truncation missing")
	}
}
