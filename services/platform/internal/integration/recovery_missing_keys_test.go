package integration

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// A management database restored from a backup taken before an environment's first secret has no
// key for the values the hosts' checkpoints hold under it. Recovery stops and names the
// environment, with the two ways on. An acceptance that does not name exactly the environments
// whose keys are missing is refused, so one made for another recovery cannot erase more. Accepted,
// recovery restores those values erased, lists each for re-entry without its value, and records
// the acceptance in the audit log.
func TestRecoveryStopsForEnvironmentKeysTheBackupLacks(t *testing.T) {
	const canary = "missing-key-canary"
	var billing api.Environment
	var version api.SecretVersion
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		base := "/api/v1/workspaces/" + ws.String()
		var project api.Project
		b.call("POST", base+"/projects", map[string]string{"name": "Accounts"}, 201, &project)
		path := base + "/projects/" + project.Id.String() + "/environments"
		b.call("POST", path, map[string]string{"name": "Billing"}, 201, &billing)
		b.call("POST", path+"/"+billing.Id.String()+"/secrets", map[string]string{"name": "PAYMENTS_TOKEN", "value": canary}, 201, &version)
	})
	// The database is as a backup taken after Billing was created, before its first secret.
	for _, statement := range []string{
		`SET session_replication_role=replica`,
		`DELETE FROM secret_versions WHERE workspace_id='` + r.ws.String() + `' AND id='` + version.Id.String() + `'`,
		`DELETE FROM secrets WHERE workspace_id='` + r.ws.String() + `' AND environment_id='` + billing.Id.String() + `'`,
		`DELETE FROM environment_keys WHERE workspace_id='` + r.ws.String() + `' AND environment_id='` + billing.Id.String() + `'`,
		`SET session_replication_role=DEFAULT`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: r.exports(t, claimExport{})}
	_, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	var missing *execution.RecoveryKeysMissing
	if !errors.As(err, &missing) || len(missing.Environments) != 1 || missing.Environments[0] != (execution.RecoveryMissingEnvironment{ID: billing.Id, Project: "Accounts", Name: "Billing", Values: 1}) ||
		!strings.Contains(err.Error(), "Accounts / Billing") || !strings.Contains(err.Error(), "Restore a management backup") || !strings.Contains(err.Error(), "--accept-erased-environments="+billing.Id.String()) {
		t.Fatal("recovery did not stop for the missing key by name", err)
	}
	for _, accepted := range [][]uuid.UUID{{r.env}, {billing.Id, r.env}} {
		in.AcceptErasedEnvironments = accepted
		if _, err = r.f.execution.RecoverCrossHost(r.ctx, in); !errors.As(err, &missing) || len(missing.Accepted) == 0 || !strings.Contains(err.Error(), "must name exactly") {
			t.Fatal("an acceptance naming other environments was taken", accepted, err)
		}
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recoveries WHERE workspace_id=$1`, r.ws); count != 0 {
		t.Fatal("a refused recovery wrote its recovery")
	}

	in.AcceptErasedEnvironments = []uuid.UUID{billing.Id}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal("preview accepting the erasure:", err)
	}
	want := []execution.RecoveryErasedValue{{EnvironmentID: billing.Id, Project: "Accounts", Environment: "Billing", Secret: "PAYMENTS_TOKEN", VersionID: version.Id}}
	if len(preview.ErasedValues) != 1 || preview.ErasedValues[0] != want[0] {
		t.Fatalf("the preview lists %+v", preview.ErasedValues)
	}
	in.ExpectedState = preview.StateHash
	applied, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil || !applied.Applied || len(applied.ErasedValues) != 1 || applied.ErasedValues[0] != want[0] {
		t.Fatalf("recovery accepting the erasure %+v %v", applied, err)
	}
	if report, _ := stablejson.Marshal(applied); strings.Contains(string(report), canary) {
		t.Fatal("the report holds the value")
	}
	// Repeating the apply, as after a lost reply, reports the same values.
	if repeated, err := r.f.execution.RecoverCrossHost(r.ctx, in); err != nil || !repeated.AlreadyApplied || !slices.Equal(repeated.ErasedValues, want) {
		t.Fatalf("the repeated recovery reports %+v %v", repeated, err)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL`, r.ws, version.Id); count != 1 {
		t.Fatal("the value under the missing key was not restored erased")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='workspace.recover_erased_environment_values' AND resource_id=$2 AND actor_id=$3`, r.ws, billing.Id, r.f.owner); count != 1 {
		t.Fatal("the acceptance is not in the audit log")
	}
	// The values are stored erased now, which no recovery undoes, so inspecting again does not stop.
	again, err := r.f.execution.RecoverCrossHost(r.ctx, execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: in.HostExports})
	if err != nil || len(again.ErasedValues) != 0 {
		t.Fatal("a later recovery stopped for values already erased", again.ErasedValues, err)
	}
}

// A database restored from a backup taken before conversion moved an environment's values onto
// its key holds them under the workspace key, which it opens, and has no environment key. The
// hosts' checkpoints hold the same versions under the environment key. Recovery keeps the stored
// versions as they are, so it neither stops for them nor reports them erased.
func TestRecoveryKeepsVersionsTheBackupHoldsUnderTheWorkspaceKey(t *testing.T) {
	const canary = "converted-later-canary"
	var billing api.Environment
	var version api.SecretVersion
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		base := "/api/v1/workspaces/" + ws.String()
		var project api.Project
		b.call("POST", base+"/projects", map[string]string{"name": "Accounts"}, 201, &project)
		path := base + "/projects/" + project.Id.String() + "/environments"
		b.call("POST", path, map[string]string{"name": "Billing"}, 201, &billing)
		b.call("POST", path+"/"+billing.Id.String()+"/secrets", map[string]string{"name": "PAYMENTS_TOKEN", "value": canary}, 201, &version)
	})
	sealed, err := cryptobox.SealUnder(workspaceKey(t, r.f, r.ws, 1), cryptobox.ScopeWorkspace, 1, []byte(canary), secretIdentity(r.ws, version.Id))
	if err != nil {
		t.Fatal(err)
	}
	tx, err := r.f.st.Pool.Begin(r.ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(r.ctx)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`SET LOCAL session_replication_role=replica`, nil},
		{`UPDATE secret_versions SET encrypted_value=$3 WHERE workspace_id=$1 AND id=$2`, []any{r.ws, version.Id, sealed}},
		{`DELETE FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2`, []any{r.ws, billing.Id}},
	} {
		if _, err = tx.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
	if err = tx.Commit(r.ctx); err != nil {
		t.Fatal(err)
	}
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: r.exports(t, claimExport{})}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil || len(preview.ErasedValues) != 0 {
		t.Fatal("recovery stopped for versions the database holds", preview.ErasedValues, err)
	}
	in.ExpectedState = preview.StateHash
	if applied, err := r.f.execution.RecoverCrossHost(r.ctx, in); err != nil || !applied.Applied || len(applied.ErasedValues) != 0 {
		t.Fatalf("recovery %+v %v", applied, err)
	}
	var stored []byte
	if err = r.f.st.Pool.QueryRow(r.ctx, `SELECT encrypted_value FROM secret_versions WHERE workspace_id=$1 AND id=$2`, r.ws, version.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if value, err := inspectValue(t, r.f.st, r.f.owner, r.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: version.Id}, stored); err != nil || string(value) != canary {
		t.Fatal("the stored version did not survive recovery", string(value), err)
	}
}

// An environment whose key the restored database lacks can also have values the hosts'
// checkpoints hold under the workspace key, sealed before every instance opened environment
// values. Accepted, recovery replaces the lost key at the generation after it, and the value under
// the workspace key moves onto that one, while the one under the lost key comes back erased. A
// key at the lost generation would fail to open a copy under the lost key as if it were corrupt,
// so a later recovery that finds such a copy the database lacks stops for it again instead.
func TestAcceptedRecoveryReplacesTheLostKeyAboveIt(t *testing.T) {
	const canary = "before-the-key-canary"
	var billing api.Environment
	var before, after api.SecretVersion
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		base := "/api/v1/workspaces/" + ws.String()
		var project api.Project
		b.call("POST", base+"/projects", map[string]string{"name": "Accounts"}, 201, &project)
		path := base + "/projects/" + project.Id.String() + "/environments"
		b.call("POST", path, map[string]string{"name": "Billing"}, 201, &billing)
		// An instance below stage 5 keeps the first value under the workspace key.
		older := uuid.New()
		if _, err := f.st.Pool.Exec(context.Background(), `INSERT INTO custody_instances(instance_id,stage) VALUES($1,4)`, older); err != nil {
			t.Fatal(err)
		}
		b.call("POST", path+"/"+billing.Id.String()+"/secrets", map[string]string{"name": "LEDGER_TOKEN", "value": canary}, 201, &before)
		if _, err := f.st.Pool.Exec(context.Background(), `UPDATE custody_instances SET seen_at=now()-interval '6 minutes' WHERE instance_id=$1`, older); err != nil {
			t.Fatal(err)
		}
		b.call("POST", path+"/"+billing.Id.String()+"/secrets", map[string]string{"name": "PAYMENTS_TOKEN", "value": "lost-canary"}, 201, &after)
	})
	underEnvironment := `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND substring(encrypted_value FROM 1 FOR 35)='{"version":2,"scope":"environment",'::bytea`
	if claimCounter(t, r.f, underEnvironment, r.ws, before.Id) != 0 || claimCounter(t, r.f, underEnvironment, r.ws, after.Id) != 1 {
		t.Fatal("the values were not sealed one under each key")
	}
	forget := func(versions ...uuid.UUID) {
		t.Helper()
		ids := make([]string, len(versions))
		for i, id := range versions {
			ids[i] = "'" + id.String() + "'"
		}
		for _, statement := range []string{
			`SET session_replication_role=replica`,
			`DELETE FROM secret_versions WHERE workspace_id='` + r.ws.String() + `' AND id IN (` + strings.Join(ids, ",") + `)`,
			`SET session_replication_role=DEFAULT`,
		} {
			if _, err := r.f.st.Pool.Exec(r.ctx, statement); err != nil {
				t.Fatal(statement, err)
			}
		}
	}
	forget(before.Id, after.Id)
	for _, statement := range []string{
		`SET session_replication_role=replica`,
		`DELETE FROM secrets WHERE workspace_id='` + r.ws.String() + `' AND environment_id='` + billing.Id.String() + `'`,
		`DELETE FROM environment_keys WHERE workspace_id='` + r.ws.String() + `' AND environment_id='` + billing.Id.String() + `'`,
		`SET session_replication_role=DEFAULT`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement); err != nil {
			t.Fatal(statement, err)
		}
	}
	exports := r.exports(t, claimExport{})
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports, AcceptErasedEnvironments: []uuid.UUID{billing.Id}}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil || len(preview.ErasedValues) != 1 || preview.ErasedValues[0].VersionID != after.Id {
		t.Fatal("preview accepting the erasure:", preview.ErasedValues, err)
	}
	in.ExpectedState = preview.StateHash
	if applied, err := r.f.execution.RecoverCrossHost(r.ctx, in); err != nil || !applied.Applied {
		t.Fatalf("recovery accepting the erasure %+v %v", applied, err)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM environment_keys WHERE workspace_id=$1 AND environment_id=$2 AND state='active' AND generation=2`, r.ws, billing.Id); count != 1 {
		t.Fatal("recovery did not replace the lost key above it")
	}
	var stored []byte
	if err = r.f.st.Pool.QueryRow(r.ctx, `SELECT encrypted_value FROM secret_versions WHERE workspace_id=$1 AND id=$2`, r.ws, before.Id).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if value, err := inspectValue(t, r.f.st, r.f.owner, r.ws, secretbroker.Item{Kind: secretbroker.SecretVersion, ID: before.Id, Environment: billing.Id}, stored); err != nil || string(value) != canary {
		t.Fatal("the value under the workspace key did not survive recovery", string(value), err)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND erased_at IS NOT NULL`, r.ws, after.Id); count != 1 {
		t.Fatal("the value under the lost key was not restored erased")
	}
	// A database without the erased version, as one restored from a backup taken before this
	// recovery, finds its copy under the lost key again, and recovery stops for it by name.
	forget(after.Id)
	_, err = r.f.execution.RecoverCrossHost(r.ctx, execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports})
	var missing *execution.RecoveryKeysMissing
	if !errors.As(err, &missing) || len(missing.Environments) != 1 || missing.Environments[0].ID != billing.Id || missing.Environments[0].Values != 1 {
		t.Fatal("a later recovery did not stop for the copy under the lost key", err)
	}
}
