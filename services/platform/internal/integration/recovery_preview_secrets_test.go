package integration

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// Recovery from a checkpoint taken before a preview's cleanup and before an environment's purge
// brings back neither the preview's secret copies, which the cleanup deleted, nor the purged
// environment's secret value, which the purge erased. A live environment's secret the restored
// database lacks still comes back.
func TestRecoveryKeepsReleasedPreviewAndPurgedSecretsGone(t *testing.T) {
	var preview, request uuid.UUID
	var previewSecret, liveSecret, purgedSecret api.SecretVersion
	var purgedPath string
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		base := "/api/v1/workspaces/" + ws.String() + "/projects"
		var project api.Project
		b.call("POST", base, map[string]string{"name": "Previews"}, 201, &project)
		environments := base + "/" + project.Id.String() + "/environments"
		create := func(name string) (api.Environment, string) {
			var env api.Environment
			b.call("POST", environments, map[string]string{"name": name}, 201, &env)
			return env, environments + "/" + env.Id.String()
		}
		template, _ := create("Template")
		previewEnv, previewPath := create("Preview 7")
		_, livePath := create("Live")
		_, purgedPath = create("Retired")
		b.call("POST", previewPath+"/secrets", map[string]string{"name": "Copied", "value": "dummy-preview-copy"}, 201, &previewSecret)
		b.call("POST", livePath+"/secrets", map[string]string{"name": "Live", "value": "dummy-live-value"}, 201, &liveSecret)
		b.call("POST", purgedPath+"/secrets", map[string]string{"name": "Purged", "value": "dummy-purged-value"}, 201, &purgedSecret)
		preview, request = previewEnv.Id, uuid.New()

		// The preview environment its template's policy allocated for an open request, as
		// allocation leaves it: purpose is set when the environment is created, so the trigger that
		// keeps it from changing is off while it is set here.
		ctx := t.Context()
		conn, err := f.st.Pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Release()
		connection := pipeline.Connection{ID: uuid.New(), Name: "Repository", Provider: "github", BaseURL: "https://github.com", Repository: "example/application", RepositoryID: 42, Private: true}
		access, _ := stablejson.Marshal(pipeline.Access{API: pipeline.Credential{Password: "dummy-git-token"}})
		cipher, err := keysOf(f.st).Seal(access, pipeline.EncryptionIdentity(ws, connection.ID, "git"))
		if err != nil {
			t.Fatal(err)
		}
		configuration, _ := stablejson.Marshal(connection)
		policy := uuid.New()
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`SET session_replication_role=replica`, nil},
			{`UPDATE environments SET purpose='preview' WHERE id=$1`, []any{preview}},
			{`SET session_replication_role=DEFAULT`, nil},
			{`INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)`, []any{connection.ID, ws, configuration, cipher}},
			{`INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,configuration)
SELECT $1,$2,$3,$4,$5,gen_random_uuid(),$6,(SELECT id FROM servers WHERE workspace_id=$2 ORDER BY id LIMIT 1),$7,1,1,'{}'`, []any{policy, ws, project.Id, template.Id, template.DraftRevisionId, connection.ID, f.owner}},
			{`INSERT INTO preview_requests(id,workspace_id,policy_id,number,generation,environment_id,state) VALUES($1,$2,$3,7,1,$4,'ready')`, []any{request, ws, policy, preview}},
		} {
			if _, err = conn.Exec(ctx, statement.sql, statement.args...); err != nil {
				t.Fatal(statement.sql, err)
			}
		}
	})

	// After the checkpoint, the preview is cleaned up and the retired environment purged. The
	// restored database is from after both, but from before the live secret was created.
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM secret_versions v USING secrets s WHERE s.id=v.secret_id AND s.environment_id=$1`, []any{preview}},
		{`DELETE FROM secrets WHERE environment_id=$1`, []any{preview}},
		{`UPDATE preview_requests SET state='deleted' WHERE id=$1`, []any{request}},
		{`DELETE FROM secret_versions WHERE id=$1`, []any{liveSecret.Id}},
		{`DELETE FROM secrets WHERE id=$1`, []any{liveSecret.SecretId}},
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
	r.b.call("POST", purgedPath+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Retired"}, 201, nil)
	r.b.call("POST", purgedPath+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Retired", VolumeIds: []uuid.UUID{}}, 201, nil)

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2`, r.ws, preview); count != 0 {
		t.Fatalf("recovery brought back %d of the cleaned-up preview's secrets", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2`, r.ws, previewSecret.Id); count != 0 {
		t.Fatal("recovery brought back the cleaned-up preview's secret value")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL`, r.ws, purgedSecret.Id); count != 1 {
		t.Fatal("recovery brought back the purged environment's secret value")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NOT NULL`, r.ws, liveSecret.Id); count != 1 {
		t.Fatal("recovery didn't bring back a live environment's secret")
	}
}
