package integration

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func TestEncryptedWorkspaceExportPreservesIdentityWithoutInstallationKeys(t *testing.T) {
	f := setup(t)
	browser := f.browser(t)
	session := browser.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	var project api.Project
	browser.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Portable"}, 201, &project)
	base := "/api/v1/workspaces/" + ws.String() + "/projects/" + project.Id.String() + "/environments"
	var env api.Environment
	browser.call("POST", base, map[string]string{"name": "Production"}, 201, &env)
	var secret api.SecretVersion
	browser.call("POST", base+"/"+env.Id.String()+"/secrets", map[string]string{"name": "PORTABLE_TOKEN", "value": "portable-secret-canary"}, 201, &secret)
	server, enroll, _ := enrollment(t, f, browser, ws, "combined")
	if _, e := f.execution.Enroll(context.Background(), enroll); e != nil {
		t.Fatal(e)
	}
	connection := pipeline.Connection{ID: uuid.New(), Name: "Repository", Provider: "github", BaseURL: "https://github.com", Repository: "example/application", RepositoryID: 42, Private: true}
	access := pipeline.Access{API: pipeline.Credential{Password: "git-token-canary"}, Webhook: "webhook-canary"}
	rawAccess, _ := json.Marshal(access)
	cipher, e := f.st.Keys.Seal(rawAccess, pipeline.EncryptionIdentity(ws, connection.ID, "git"))
	if e != nil {
		t.Fatal(e)
	}
	rawConnection, _ := json.Marshal(connection)
	if _, e = f.st.Pool.Exec(context.Background(), "INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)", connection.ID, ws, rawConnection, cipher); e != nil {
		t.Fatal(e)
	}
	source := pipeline.Source{ConnectionID: connection.ID, Branch: "main", RunnerID: server.Server.Id, Entrypoints: []string{"compose.yaml"}, Paths: []string{}, AutoDeploy: true}
	rawSource, _ := json.Marshal(source)
	if _, e = f.st.Pool.Exec(context.Background(), "INSERT INTO git_sources(workspace_id,environment_id,connection_id,configuration,policy_actor) VALUES($1,$2,$3,$4,$5)", ws, env.Id, connection.ID, rawSource, f.owner); e != nil {
		t.Fatal(e)
	}
	passphrase := "unique archive passphrase for fixture"
	var encrypted api.WorkspaceArchive
	browser.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)
	if bytes.Contains(encrypted.Archive, []byte("portable-secret-canary")) || bytes.Contains(encrypted.Archive, []byte(f.st.Keys.Active)) {
		t.Fatal("archive exposes contents")
	}
	if _, e := portability.Open(context.Background(), "incorrect archive passphrase", encrypted.Archive); e == nil {
		t.Fatal("wrong passphrase accepted")
	}
	altered := append([]byte(nil), encrypted.Archive...)
	altered[len(altered)-1] ^= 1
	if _, e := portability.Open(context.Background(), passphrase, altered); e == nil {
		t.Fatal("altered archive accepted")
	}
	plain, e := portability.Open(context.Background(), passphrase, encrypted.Archive)
	if e != nil {
		t.Fatal(e)
	}
	defer clear(plain)
	var archive portability.Archive
	if e = json.Unmarshal(plain, &archive); e != nil {
		t.Fatal(e)
	}
	if archive.Workspace != ws || archive.Name != "Test workspace" || len(archive.Tables["environments"]) != 1 {
		t.Fatal("workspace identity not preserved")
	}
	var encoded string
	if e = json.Unmarshal(archive.Tables["secret_versions"][0]["encrypted_value"], &encoded); e != nil {
		t.Fatal(e)
	}
	value, e := hex.DecodeString(strings.TrimPrefix(encoded, `\x`))
	if e != nil || string(value) != "portable-secret-canary" {
		t.Fatal("secret was not unwrapped into encrypted archive")
	}
	for _, forbidden := range []string{"accounts", "memberships", "sessions", "hosted_subscriptions", "river_job"} {
		if _, ok := archive.Tables[forbidden]; ok {
			t.Fatal("installation-private table exported", forbidden)
		}
	}
	destination := setup(t)
	owner := destination.browser(t)
	owner.login("owner@example.test", destination.password)
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": "incorrect archive passphrase"}, 400, nil)
	var imported api.Workspace
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, &imported)
	if imported.Id != ws || imported.Role != "owner" {
		t.Fatal("import changed workspace identity")
	}
	var importedCipher []byte
	var author uuid.UUID
	if e = destination.st.Pool.QueryRow(context.Background(), "SELECT encrypted_value,author_id FROM secret_versions WHERE id=$1", secret.Id).Scan(&importedCipher, &author); e != nil {
		t.Fatal(e)
	}
	recovered, e := destination.st.Keys.Open(importedCipher, store.SecretIdentity(ws, secret.Id))
	if e != nil || string(recovered) != "portable-secret-canary" || author != destination.owner {
		t.Fatalf("secret or author was not transferred: %v", e)
	}
	if _, e = f.st.Keys.Open(importedCipher, store.SecretIdentity(ws, secret.Id)); e == nil {
		t.Fatal("import retained source wrapping keys")
	}
	var status string
	var actor uuid.UUID
	var config pipeline.Source
	var configRaw []byte
	if e = destination.st.Pool.QueryRow(context.Background(), "SELECT status,policy_actor,configuration FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", ws, env.Id).Scan(&status, &actor, &configRaw); e != nil {
		t.Fatal(e)
	}
	if json.Unmarshal(configRaw, &config) != nil || config.AutoDeploy || status != "approval_required" || actor != destination.owner {
		t.Fatal("import granted unattended automation")
	}
	account, e := destination.st.Queries.AccountByID(context.Background(), destination.owner)
	if e != nil {
		t.Fatal(e)
	}
	if e = destination.execution.SourcePolicy(context.Background(), store.Principal{Account: account, Recent: true}, ws, project.Id, env.Id, true); e != nil {
		t.Fatal(e)
	}
	if e = destination.st.Pool.QueryRow(context.Background(), "SELECT status FROM git_sources WHERE workspace_id=$1 AND environment_id=$2", ws, env.Id).Scan(&status); e != nil || status != "ready" {
		t.Fatal("explicit owner approval did not release the imported Git policy", e)
	}
	var awaiting bool
	var controller int64
	var bearer []byte
	if e = destination.st.Pool.QueryRow(context.Background(), "SELECT awaiting_transfer,controller_generation,credential_hash FROM servers WHERE id=$1", server.Server.Id).Scan(&awaiting, &controller, &bearer); e != nil || !awaiting || controller != 1 || len(bearer) != 0 {
		t.Fatalf("server authority was copied: %v", e)
	}
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 409, nil)
	anonymous := f.browser(t)
	anonymous.call("POST", "/api/v1/workspaces/"+ws.String()+"/export", map[string]string{"passphrase": passphrase}, 401, nil)
}

func TestWorkspaceExportRejectsConcurrentOwnerRemoval(t *testing.T) {
	f := setup(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	browser := f.browser(t)
	ws := browser.login("owner@example.test", f.password).Workspaces[0].Id
	a, e := f.st.Queries.AccountByID(ctx, f.owner)
	if e != nil {
		t.Fatal(e)
	}
	other, e := f.st.Queries.InsertAccount(ctx, db.InsertAccountParams{ID: uuid.New(), Email: "other@example.test", Name: "Other owner", PasswordHash: "unused"})
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.Queries.InsertMembership(ctx, db.InsertMembershipParams{WorkspaceID: ws, AccountID: other.ID, Role: "owner"}); e != nil {
		t.Fatal(e)
	}
	removing, e := f.st.Pool.Begin(ctx)
	if e != nil {
		t.Fatal(e)
	}
	defer removing.Rollback(context.Background())
	q := db.New(removing)
	if _, e = q.LockWorkspaceByID(ctx, ws); e != nil {
		t.Fatal(e)
	}
	if e = q.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: ws, AccountID: f.owner}); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		_, e := (&portability.Service{Store: f.st}).Export(ctx, store.Principal{Account: a, Recent: true}, ws, "unique archive passphrase for fixture")
		done <- e
	}()
	// Observe the actual lock wait so the test proves the revocation/export
	// interleaving rather than relying on goroutine scheduling or a fixed sleep.
	for {
		var waiting bool
		e = f.st.Pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND wait_event_type='Lock' AND query LIKE '%FROM workspaces WHERE id=$1 FOR UPDATE%')`).Scan(&waiting)
		if e != nil {
			t.Fatal(e)
		}
		if waiting {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if e = removing.Commit(ctx); e != nil {
		t.Fatal(e)
	}
	if e = <-done; e == nil {
		t.Fatal("removed owner exported a stale permission snapshot")
	}
}

func TestManagementRecoveryFencesQueuedCommandsBeforeAgentReconnect(t *testing.T) {
	f := setup(t)
	browser := f.browser(t)
	session := browser.login("owner@example.test", f.password)
	ws := session.Workspaces[0].Id
	enrolled, input, oldBearer := enrollment(t, f, browser, ws, "deployment")
	if _, e := f.execution.Enroll(context.Background(), input); e != nil {
		t.Fatal(e)
	}
	env := envForExecution(t, f, browser, ws)
	account, e := f.st.Queries.AccountByID(context.Background(), f.owner)
	if e != nil {
		t.Fatal(e)
	}
	principal := store.Principal{Account: account, Recent: true}
	plan, e := f.execution.Plan(context.Background(), principal, ws, api.PlanRequest{ServerId: enrolled.Server.Id, EnvironmentId: env, Action: "inspect"})
	if e != nil {
		t.Fatal(e)
	}
	old, e := f.execution.Apply(context.Background(), principal, ws, plan.Id, uuid.New())
	if e != nil {
		t.Fatal(e)
	}
	if e = f.st.PrepareRecovery(context.Background(), enrolled.Server.Id, 1); e != nil {
		t.Fatal(e)
	}
	if e = f.execution.Control(context.Background(), principal, ws, enrolled.Server.Id, true); e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.Authenticate(context.Background(), enrolled.Server.Id, strings.SplitN(oldBearer, ".", 2)[1]); e == nil {
		t.Fatal("old bearer survived recovery fence")
	}
	var state string
	if e = f.st.Pool.QueryRow(context.Background(), "SELECT state FROM execution_operations WHERE id=$1", old.Id).Scan(&state); e != nil || state != "cancelled" {
		t.Fatalf("restored command not fenced: %s %v", state, e)
	}
	ticket, e := f.execution.PrepareTransfer(context.Background(), principal, ws, enrolled.Server.Id)
	if e != nil {
		t.Fatal(e)
	}
	input.Token = ticket.Token
	input.Credential, _ = cryptobox.Token()
	if _, e = f.execution.Enroll(context.Background(), input); e == nil {
		t.Fatal("old-generation enrollment accepted")
	}
	input.ControllerGeneration = &ticket.Generation
	input.Inventory = &api.ControllerInventory{Units: map[string]string{}, Schedules: map[string]int64{}}
	if _, e = f.execution.Enroll(context.Background(), input); e != nil {
		t.Fatal(e)
	}
	if _, e = f.execution.Enroll(context.Background(), input); e != nil {
		t.Fatal("lost enrollment response was not retryable", e)
	}
	plan, e = f.execution.Plan(context.Background(), principal, ws, api.PlanRequest{ServerId: enrolled.Server.Id, EnvironmentId: env, Action: "inspect"})
	if e != nil {
		t.Fatal(e)
	}
	next, e := f.execution.Apply(context.Background(), principal, ws, plan.Id, uuid.New())
	if e != nil {
		t.Fatal(e)
	}
	identity, e := f.execution.Authenticate(context.Background(), enrolled.Server.Id, input.Credential)
	if e != nil {
		t.Fatal(e)
	}
	command, e := f.execution.Poll(context.Background(), identity)
	if e != nil || command == nil || command.Id != next.Id || command.ControllerGeneration == nil || *command.ControllerGeneration != 1 {
		t.Fatalf("new controller command not fenced: %+v %v", command, e)
	}
}
