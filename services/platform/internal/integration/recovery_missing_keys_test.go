package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
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
