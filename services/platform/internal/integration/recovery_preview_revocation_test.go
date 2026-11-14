package integration

import (
	"testing"

	"github.com/google/uuid"
)

// previewRecord is what the database records of a workspace's previews, their secret copies and
// its servers at one moment, so a test can restore the database to it after the checkpoint.
type previewRecord struct{ requests, policies, secrets, versions, servers []byte }

func recordPreviews(t *testing.T, f *fixture, ws uuid.UUID) previewRecord {
	t.Helper()
	var out previewRecord
	for _, read := range []struct {
		sql string
		out *[]byte
	}{
		{`SELECT COALESCE(jsonb_agg(to_jsonb(r)),'[]') FROM preview_requests r WHERE workspace_id=$1`, &out.requests},
		{`SELECT COALESCE(jsonb_agg(to_jsonb(p)),'[]') FROM preview_policies p WHERE workspace_id=$1`, &out.policies},
		{`SELECT COALESCE(jsonb_agg(to_jsonb(s)),'[]') FROM secrets s JOIN environments e ON e.id=s.environment_id WHERE e.workspace_id=$1 AND e.purpose='preview'`, &out.secrets},
		{`SELECT COALESCE(jsonb_agg(to_jsonb(v)),'[]') FROM secret_versions v JOIN secrets s ON s.id=v.secret_id JOIN environments e ON e.id=s.environment_id WHERE e.workspace_id=$1 AND e.purpose='preview'`, &out.versions},
		{`SELECT COALESCE(jsonb_agg(to_jsonb(s)),'[]') FROM servers s WHERE workspace_id=$1`, &out.servers},
	} {
		if err := f.st.Pool.QueryRow(t.Context(), read.sql, ws).Scan(read.out); err != nil {
			t.Fatal(read.sql, err)
		}
	}
	return out
}

// restore puts the recorded previews, secret copies and revocations back, as a database restored
// from that moment has them.
func (p previewRecord) restore(t *testing.T, r *claimRecovery) {
	t.Helper()
	for _, statement := range []struct {
		sql  string
		args []any
	}{
		{`DELETE FROM secret_versions v USING secrets s, environments e WHERE s.id=v.secret_id AND e.id=s.environment_id AND e.workspace_id=$1 AND e.purpose='preview'`, []any{r.ws}},
		{`DELETE FROM secrets s USING environments e WHERE e.id=s.environment_id AND e.workspace_id=$1 AND e.purpose='preview'`, []any{r.ws}},
		{`INSERT INTO secrets SELECT * FROM jsonb_populate_recordset(NULL::secrets,$1)`, []any{p.secrets}},
		{`INSERT INTO secret_versions SELECT * FROM jsonb_populate_recordset(NULL::secret_versions,$1)`, []any{p.versions}},
		{`DELETE FROM preview_requests r WHERE workspace_id=$1 AND NOT EXISTS(SELECT 1 FROM jsonb_populate_recordset(NULL::preview_requests,$2) s WHERE s.id=r.id)`, []any{r.ws, p.requests}},
		{`UPDATE preview_requests r SET state=s.state,note=s.note,released_by_revocation=s.released_by_revocation,cleanup_at=s.cleanup_at,next_check_at=s.next_check_at,environment_id=s.environment_id,generation=s.generation FROM jsonb_populate_recordset(NULL::preview_requests,$1) s WHERE r.id=s.id`, []any{p.requests}},
		{`UPDATE preview_policies p SET server_id=s.server_id,generation=s.generation,note=s.note FROM jsonb_populate_recordset(NULL::preview_policies,$1) s WHERE p.id=s.id`, []any{p.policies}},
		{`UPDATE servers v SET revoked=s.revoked FROM jsonb_populate_recordset(NULL::servers,$1) s WHERE v.id=s.id`, []any{p.servers}},
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement.sql, statement.args...); err != nil {
			t.Fatal(statement.sql, err)
		}
	}
}

// enrolPreviewHost enrols the host a preview policy runs on.
func enrolPreviewHost(t *testing.T, f *fixture, b *browser, ws uuid.UUID) uuid.UUID {
	t.Helper()
	_, in, _ := enrollment(t, f, b, ws, "deployment")
	if _, err := f.execution.Enroll(t.Context(), acknowledging(in), "test"); err != nil {
		t.Fatal(err)
	}
	return in.ServerId
}

func revokeServer(b *browser, ws, server uuid.UUID) {
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/servers/"+server.String()+"/revoke", nil, 204, nil)
}

// A restored database from before a preview host's revocation still lists its previews open, with
// their secret copies. Recovery from a checkpoint that records the revocation releases them as the
// revocation did: nothing on a revoked host could release them later.
func TestRecoveryReleasesPreviewsOfAHostTheCheckpointRecordsRevoked(t *testing.T) {
	var previews seededPreviews
	var host uuid.UUID
	var before previewRecord
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		host = enrolPreviewHost(t, f, b, ws)
		previews = seedPreviews(t, f, b, ws, host)
		before = recordPreviews(t, f, ws)
		revokeServer(b, ws, host)
	})
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE policy_id=$1 AND state='deleted' AND released_by_revocation`, previews.policy); count != 2 {
		t.Fatal("revoking the preview host didn't release its previews before the checkpoint", count)
	}
	before.restore(t, r)

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked`, host); count != 1 {
		t.Fatal("recovery didn't keep the checkpoint's revocation")
	}
	// The checkpoint's copies record the release, which recovery keeps over the open ones. Whether a
	// request was released by revocation isn't archived.
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE policy_id=$1 AND state='deleted'`, previews.policy); count != 2 {
		t.Fatalf("recovery released %d of the revoked host's 2 previews, so the rest keep their slots", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=ANY($2)`, r.ws, previews.envs[:]); count != 0 {
		t.Fatalf("recovery kept %d secret copies of the released previews", count)
	}
}

// After the backup, the owner moved a preview policy off its host to another, where a new preview
// opened. Whether the checkpoint records the old host's revocation or the restored management
// revokes it before recovering, recovery ends the previews the old host ran and leaves the one on
// the new host alone. It keeps the copy of the policy naming the new host, so that preview's cleanup
// goes where it runs, and the policy can move again once it has gone.
func TestRecoveryReleasesOnlyThePreviewsARevokedHostRuns(t *testing.T) {
	for _, revokedAfterRestore := range []bool{false, true} {
		t.Run(map[bool]string{false: "revoked in the checkpoint", true: "revoked after the restore"}[revokedAfterRestore], func(t *testing.T) {
			var previews seededPreviews
			var host, moved, request, env uuid.UUID
			var before previewRecord
			r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
				host = enrolPreviewHost(t, f, b, ws)
				previews = seedPreviews(t, f, b, ws, host)
				before = recordPreviews(t, f, ws)
				if !revokedAfterRestore {
					revokeServer(b, ws, host)
				}
				// The old host's previews have ended, so the policy can move.
				created, _, _ := enrollment(t, f, b, ws, "deployment")
				moved = created.Server.Id
				for _, statement := range []struct {
					sql  string
					args []any
				}{
					{`UPDATE preview_requests SET state='deleted' WHERE policy_id=$1`, []any{previews.policy}},
					{`UPDATE preview_policies SET server_id=$2,generation=generation+1 WHERE id=$1`, []any{previews.policy, created.Server.Id}},
				} {
					if _, err := f.st.Pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
						t.Fatal(statement.sql, err)
					}
				}
				request, env, _ = openPreview(t, f, b, ws, previews, 9)
			})
			before.restore(t, r)
			if revokedAfterRestore {
				// Revoking advances the restored copy of the policy to the generation of the move.
				revokeServer(r.b, r.ws, host)
			}

			r.recoverAll(t, r.exports(t, claimExport{}))
			if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE id=$1 AND state<>'deleted' AND environment_id=$2`, request, env); count != 1 {
				t.Fatal("recovery released a preview running on a healthy host")
			}
			if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=$1 AND v.encrypted_value IS NOT NULL`, env); count != 1 {
				t.Fatal("recovery deleted the secret copy of a preview running on a healthy host")
			}
			if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_policies WHERE id=$1 AND server_id=$2`, previews.policy, moved); count != 1 {
				t.Fatal("recovery kept a copy of the policy naming a host its open preview doesn't run on")
			}
			if revokedAfterRestore {
				// Revoking in the restored management already released the old host's previews.
				return
			}
			if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE id=ANY($1) AND state='deleted'`, previews.requests[:]); count != 2 {
				t.Fatalf("recovery ended %d of the old host's 2 previews the restored database lists open", count)
			}
			if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=ANY($2)`, r.ws, previews.envs[:]); count != 0 {
				t.Fatalf("recovery kept %d secret copies of the revoked host's previews", count)
			}
		})
	}
}

// The restored database records a preview host's revocation, and after it the owner renewed the
// host, where a new preview opened. Recovery keeps the revocation whichever side knew of it, so it
// releases that preview as revoking the host would: nothing on a revoked host could release it later.
func TestRecoveryReleasesPreviewsOfARevokedHostRenewedAfterTheBackup(t *testing.T) {
	var host, request, env uuid.UUID
	var before previewRecord
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		host = enrolPreviewHost(t, f, b, ws)
		previews := seedPreviews(t, f, b, ws, host)
		revokeServer(b, ws, host)
		before = recordPreviews(t, f, ws)
		// Renewing clears the revocation, and saving the policy again advances its generation.
		for _, statement := range []string{`UPDATE servers SET revoked=false WHERE id=$1`, `UPDATE preview_policies SET generation=generation+1 WHERE server_id=$1`} {
			if _, err := f.st.Pool.Exec(t.Context(), statement, host); err != nil {
				t.Fatal(statement, err)
			}
		}
		request, env, _ = openPreview(t, f, b, ws, previews, 9)
	})
	before.restore(t, r)

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM servers WHERE id=$1 AND revoked`, host); count != 1 {
		t.Fatal("recovery didn't keep the database's revocation")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE id=$1 AND state='deleted' AND released_by_revocation`, request); count != 1 {
		t.Fatal("recovery left a preview on a revoked host holding its slot")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=$2`, r.ws, env); count != 0 {
		t.Fatal("recovery kept the secret copy of a preview on a revoked host")
	}
}

// After the backup, an owner who joined since cleaned up both previews, and another opened. The
// checkpoint records them deleted, with no revocation to settle them, so recovery ends them over the
// restored database's open copies and deletes the secret copies that database still holds. The
// restored database never had that owner's account, so recovery drops the cleanup requests that name
// it, as fencing the workspace does. The newer preview stays.
func TestRecoveryEndsPreviewsTheCheckpointRecordsDeleted(t *testing.T) {
	var previews seededPreviews
	var request, env uuid.UUID
	var before previewRecord
	joined := uuid.New()
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		previews = seedPreviews(t, f, b, ws, claimUUID(t, f, `SELECT id FROM servers WHERE workspace_id=$1 ORDER BY id LIMIT 1`, ws))
		before = recordPreviews(t, f, ws)
		// As an owner's cleanup leaves each preview.
		for _, statement := range []struct {
			sql  string
			args []any
		}{
			{`INSERT INTO accounts(id,email,name,password_hash) VALUES($1,'joined-owner@example.test','Joined','unused')`, []any{joined}},
			{`DELETE FROM secret_versions v USING secrets s WHERE s.id=v.secret_id AND s.environment_id=ANY($1)`, []any{previews.envs[:]}},
			{`DELETE FROM secrets WHERE environment_id=ANY($1)`, []any{previews.envs[:]}},
			{`UPDATE preview_requests SET state='deleted',cleanup_requested=true,cleanup_actor=$2,cleanup_auth_version=1,cleanup_controller=1 WHERE environment_id=ANY($1)`, []any{previews.envs[:], joined}},
		} {
			if _, err := f.st.Pool.Exec(t.Context(), statement.sql, statement.args...); err != nil {
				t.Fatal(statement.sql, err)
			}
		}
		request, env, _ = openPreview(t, f, b, ws, previews, 9)
	})
	before.restore(t, r)
	for _, statement := range []string{
		`UPDATE preview_requests SET cleanup_requested=false,cleanup_actor=NULL,cleanup_auth_version=NULL,cleanup_controller=NULL WHERE cleanup_actor=$1`,
		`DELETE FROM accounts WHERE id=$1`,
	} {
		if _, err := r.f.st.Pool.Exec(r.ctx, statement, joined); err != nil {
			t.Fatal(statement, err)
		}
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=ANY($2)`, r.ws, previews.envs[:]); count != 2 {
		t.Fatal("the restored database doesn't hold the previews' secret copies", count)
	}

	r.recoverAll(t, r.exports(t, claimExport{}))
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE id=ANY($1) AND state='deleted'`, previews.requests[:]); count != 2 {
		t.Fatalf("recovery ended %d of the 2 previews the checkpoint records deleted", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secrets WHERE workspace_id=$1 AND environment_id=ANY($2)`, r.ws, previews.envs[:]); count != 0 {
		t.Fatalf("recovery kept %d secret copies of previews the checkpoint records deleted", count)
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM preview_requests WHERE id=$1 AND state<>'deleted' AND environment_id=$2`, request, env); count != 1 {
		t.Fatal("recovery ended the preview opened after the backup")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM secret_versions v JOIN secrets s ON s.id=v.secret_id WHERE s.environment_id=$1 AND v.encrypted_value IS NOT NULL`, env); count != 1 {
		t.Fatal("recovery deleted the secret copy of the preview opened after the backup")
	}
}
