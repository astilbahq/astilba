package integration

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// seededPreviews is a template's preview policy on a host, with two open requests, numbers 7 and 8,
// whose preview environments each hold a copy of a secret.
type seededPreviews struct {
	policy   uuid.UUID
	envs     [2]uuid.UUID
	requests [2]uuid.UUID
	secrets  [2]api.SecretVersion
}

// seedPreviews leaves the preview environments as allocation does: purpose is set when an
// environment is created, so the trigger that keeps it from changing is off while it is set here.
func seedPreviews(t *testing.T, f *fixture, b *browser, ws, host uuid.UUID) seededPreviews {
	t.Helper()
	base := "/api/v1/workspaces/" + ws.String() + "/projects"
	var project api.Project
	b.call("POST", base, map[string]string{"name": "Previews"}, 201, &project)
	environments := base + "/" + project.Id.String() + "/environments"
	var template api.Environment
	b.call("POST", environments, map[string]string{"name": "Template"}, 201, &template)
	out := seededPreviews{policy: uuid.New(), requests: [2]uuid.UUID{uuid.New(), uuid.New()}}
	for i, name := range []string{"Preview 7", "Preview 8"} {
		var env api.Environment
		b.call("POST", environments, map[string]string{"name": name}, 201, &env)
		b.call("POST", environments+"/"+env.Id.String()+"/secrets", map[string]string{"name": "Copied", "value": "dummy-preview-copy"}, 201, &out.secrets[i])
		out.envs[i] = env.Id
	}
	ctx := t.Context()
	conn, err := f.st.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	defer conn.Exec(ctx, `SET session_replication_role=DEFAULT`)
	connection := pipeline.Connection{ID: uuid.New(), Name: "Repository", Provider: "github", BaseURL: "https://github.com", Repository: "example/application", RepositoryID: 42, Private: true}
	access, _ := stablejson.Marshal(pipeline.Access{API: pipeline.Credential{Password: "dummy-git-token"}})
	cipher, err := keysOf(f.st).Seal(access, pipeline.EncryptionIdentity(ws, connection.ID, "git"))
	if err != nil {
		t.Fatal(err)
	}
	configuration, _ := stablejson.Marshal(connection)
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`SET session_replication_role=replica`, nil},
		{`UPDATE environments SET purpose='preview' WHERE id=ANY($1)`, []any{out.envs[:]}},
		{`SET session_replication_role=DEFAULT`, nil},
		{`INSERT INTO git_connections(id,workspace_id,configuration,encrypted_access) VALUES($1,$2,$3,$4)`, []any{connection.ID, ws, configuration, cipher}},
		{`INSERT INTO preview_policies(id,workspace_id,project_id,environment_id,revision_id,application_id,connection_id,server_id,actor_id,auth_version,controller_generation,configuration)
VALUES($1,$2,$3,$4,$5,gen_random_uuid(),$6,$7,$8,1,1,'{}')`, []any{out.policy, ws, project.Id, template.Id, template.DraftRevisionId, connection.ID, host, f.owner}},
		{`INSERT INTO preview_requests(id,workspace_id,policy_id,number,generation,environment_id,state) VALUES($1,$2,$3,7,1,$4,'ready'),($5,$2,$3,8,1,$6,'ready')`, []any{out.requests[0], ws, out.policy, out.envs[0], out.requests[1], out.envs[1]}},
	} {
		if _, err = conn.Exec(ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
	return out
}

// Recovery from a checkpoint taken before a preview was released and before an environment's purge
// brings back neither the released preview's secret copies, which its release deleted, nor the
// purged environment's secret value, which the purge erased. The secrets of an open preview and of a
// standard environment that the restored database lacks still come back.
func TestRecoveryKeepsReleasedPreviewAndPurgedSecretsGone(t *testing.T) {
	var previews seededPreviews
	var liveSecret, purgedSecret api.SecretVersion
	var purgedPath string
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		previews = seedPreviews(t, f, b, ws, claimUUID(t, f, `SELECT id FROM servers WHERE workspace_id=$1 ORDER BY id LIMIT 1`, ws))
		base := "/api/v1/workspaces/" + ws.String() + "/projects"
		var project api.Project
		b.call("POST", base, map[string]string{"name": "Standard"}, 201, &project)
		environments := base + "/" + project.Id.String() + "/environments"
		for _, env := range []struct {
			name   string
			secret *api.SecretVersion
		}{{"Live", &liveSecret}, {"Retired", &purgedSecret}} {
			var created api.Environment
			b.call("POST", environments, map[string]string{"name": env.name}, 201, &created)
			path := environments + "/" + created.Id.String()
			b.call("POST", path+"/secrets", map[string]string{"name": env.name, "value": "dummy-" + env.name + "-value"}, 201, env.secret)
			if env.name == "Retired" {
				purgedPath = path
			}
		}
	})
	released, open := previews.envs[0], previews.secrets[1]

	// After the checkpoint, the first preview is released as revoking its preview host releases it,
	// and the retired environment is purged. The restored database is from after both, but from
	// before the open preview's and the standard environment's secrets were created.
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM secret_versions v USING secrets s WHERE s.id=v.secret_id AND s.environment_id=$1`, []any{released}},
		{`DELETE FROM secrets WHERE environment_id=$1`, []any{released}},
		{`UPDATE preview_requests SET state='deleted',released_by_revocation=true WHERE id=$1`, []any{previews.requests[0]}},
		{`DELETE FROM secret_versions WHERE id=ANY($1)`, []any{[]uuid.UUID{liveSecret.Id, open.Id}}},
		{`DELETE FROM secrets WHERE id=ANY($1)`, []any{[]uuid.UUID{liveSecret.SecretId, open.SecretId}}},
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
	r.b.call("POST", purgedPath+"/retire", api.EnvironmentLifecycleRequest{ConfirmName: "Retired"}, 201, nil)
	r.b.call("POST", purgedPath+"/purge", api.EnvironmentPurgeRequest{ConfirmName: "Retired", VolumeIds: []uuid.UUID{}}, 201, nil)

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2`, r.ws, released); count != 0 {
		t.Fatalf("recovery brought back %d of the released preview's secrets", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2`, r.ws, previews.secrets[0].Id); count != 0 {
		t.Fatal("recovery brought back the released preview's secret value")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NULL AND erased_at IS NOT NULL`, r.ws, purgedSecret.Id); count != 1 {
		t.Fatal("recovery brought back the purged environment's secret value")
	}
	for _, live := range []api.SecretVersion{open, liveSecret} {
		if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions WHERE workspace_id=$1 AND id=$2 AND encrypted_value IS NOT NULL`, r.ws, live.Id); count != 1 {
			t.Fatal("recovery didn't bring back a live environment's secret", live.Id)
		}
	}
}

// A restored database from before a preview host's revocation still lists its previews open, with
// their secret copies and its policy's fork approvals. Recovery from a checkpoint that records the
// revocation releases them as the revocation did: nothing on a revoked host could release them later.
func TestRecoveryReleasesPreviewsOfAHostTheCheckpointRecordsRevoked(t *testing.T) {
	var previews seededPreviews
	var host uuid.UUID
	type saved struct{ requests, policies, secrets, versions []byte }
	var before saved
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		_, in, _ := enrollment(t, f, b, ws, "deployment")
		if _, err := f.execution.Enroll(t.Context(), acknowledging(in), "test"); err != nil {
			t.Fatal(err)
		}
		host = in.ServerId
		previews = seedPreviews(t, f, b, ws, host)
		for _, read := range []struct {
			sql string
			arg any
			out *[]byte
		}{
			{`SELECT jsonb_agg(to_jsonb(r)) FROM preview_requests r WHERE policy_id=$1`, previews.policy, &before.requests},
			{`SELECT jsonb_agg(to_jsonb(p)) FROM preview_policies p WHERE id=$1`, previews.policy, &before.policies},
			{`SELECT jsonb_agg(to_jsonb(s)) FROM secrets s WHERE environment_id=ANY($1)`, previews.envs[:], &before.secrets},
			{`SELECT jsonb_agg(to_jsonb(v)) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=ANY($1)`, previews.envs[:], &before.versions},
		} {
			if err := f.st.Pool.QueryRow(t.Context(), read.sql, read.arg).Scan(read.out); err != nil {
				t.Fatal(read.sql, err)
			}
		}
		b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+host.String()+"/revoke", nil, 204, nil)
	})
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE policy_id=$1 AND state='deleted' AND released_by_revocation`, previews.policy); count != 2 {
		t.Fatal("revoking the preview host didn't release its previews before the checkpoint", count)
	}

	// The restored database is from before the revocation.
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`UPDATE servers SET revoked=false WHERE id=$1`, []any{host}},
		{`UPDATE preview_requests r SET state=s.state,note=s.note,released_by_revocation=s.released_by_revocation,cleanup_at=s.cleanup_at,next_check_at=s.next_check_at FROM jsonb_populate_recordset(NULL::preview_requests,$1) s WHERE r.id=s.id`, []any{before.requests}},
		{`UPDATE preview_policies p SET generation=s.generation,note=s.note FROM jsonb_populate_recordset(NULL::preview_policies,$1) s WHERE p.id=s.id`, []any{before.policies}},
		{`INSERT INTO secrets SELECT * FROM jsonb_populate_recordset(NULL::secrets,$1)`, []any{before.secrets}},
		{`INSERT INTO secret_versions SELECT * FROM jsonb_populate_recordset(NULL::secret_versions,$1)`, []any{before.versions}},
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
	generation := claimCounter(t, r.f, `SELECT generation FROM preview_policies WHERE id=$1`, previews.policy)

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked`, host); count != 1 {
		t.Fatal("recovery didn't keep the checkpoint's revocation")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE policy_id=$1 AND state='deleted' AND released_by_revocation`, previews.policy); count != 2 {
		t.Fatalf("recovery released %d of the revoked host's 2 previews, so the rest keep their slots", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=ANY($2)`, r.ws, previews.envs[:]); count != 0 {
		t.Fatalf("recovery kept %d secret copies of the released previews", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_policies WHERE id=$1 AND generation>$2`, previews.policy, generation); count != 1 {
		t.Fatal("recovery left the revoked preview host's fork approvals in force")
	}
}
