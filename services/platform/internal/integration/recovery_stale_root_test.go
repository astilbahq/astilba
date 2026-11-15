package integration

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// A cross-host root the newest checkpoint carries finished stays finished. A host that still reports
// running it is stale, since a host releases a root in the same step as the report that finishes it,
// so recovery refuses it by name before writing anything, and recovers once the owner leaves it out.
// A host that reports one of a root's steps as its root is refused by name in the same way.
func TestRecoveryKeepsAFinishedRootFinished(t *testing.T) {
	finished, step := uuid.New(), uuid.New()
	r := newParticipantRecovery(t, func(f *fixture, b *browser, ws, _ uuid.UUID) {
		// An earlier deployment, in another environment, that every host confirmed.
		var project api.Project
		b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects", map[string]string{"name": "Earlier"}, 201, &project)
		var env api.Environment
		b.call("POST", "/api/v1/workspaces/"+ws.String()+"/projects/"+project.Id.String()+"/environments", map[string]string{"name": "Earlier"}, 201, &env)
		if _, err := f.st.Pool.Exec(t.Context(), `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,report_sequence)
SELECT $1::uuid,workspace_id,server_id,$2::uuid,1,gen_random_uuid(),jsonb_set(jsonb_set(jsonb_set(command,'{id}',to_jsonb($1::text)),'{environmentId}',to_jsonb($2::text)),'{generation}','1'),'crosshost','succeeded',1
FROM execution_operations WHERE workspace_id=$3 AND kind='crosshost' AND parent_id IS NULL`, finished, env.Id, ws); err != nil {
			t.Fatal(err)
		}
		// One of its steps that never reported finishing.
		if _, err := f.st.Pool.Exec(t.Context(), `INSERT INTO execution_operations(id,workspace_id,server_id,environment_id,generation,request_key,command,kind,state,parent_id)
SELECT $1::uuid,workspace_id,server_id,environment_id,2,gen_random_uuid(),jsonb_set(jsonb_set(jsonb_set(command,'{id}',to_jsonb($1::text)),'{parentId}',to_jsonb($2::text)),'{generation}','2'),'crosshost','running',$2::uuid
FROM execution_operations WHERE id=$2::uuid`, step, finished); err != nil {
			t.Fatal(err)
		}
	})
	exports := r.exports(t, claimExport{})
	in := execution.RecoveryImport{WorkspaceID: r.ws, OwnerID: r.f.owner, HostExports: exports}
	preview, err := r.f.execution.RecoverCrossHost(r.ctx, in)
	if err != nil {
		t.Fatal("preview with honest exports: ", err)
	}

	// The peer's export names one of the finished root's steps as its root, and then says it still
	// holds the finished root, as a host restored from an image taken while that deployment ran would.
	var evidence deployment.RecoveryEvidence
	raw, err := os.ReadFile(filepath.Join(exports[1], "host.json"))
	if err != nil || json.Unmarshal(raw, &evidence) != nil {
		t.Fatal("the peer's export", err)
	}
	in.ExpectedState = preview.StateHash
	for _, report := range []struct {
		parent uuid.UUID
		reason string
	}{{step, "another kind of operation"}, {finished, "records as ended"}} {
		evidence.ActiveParent = report.parent
		if err = os.WriteFile(filepath.Join(exports[1], "host.json"), claimBytes(t, evidence), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err = r.f.execution.RecoverCrossHost(r.ctx, in)
		if err == nil || !strings.Contains(err.Error(), report.parent.String()) || !strings.Contains(err.Error(), r.peer.ServerId.String()) || !strings.Contains(err.Error(), report.reason) || !strings.Contains(err.Error(), "revoke those servers") {
			t.Fatal("a host reporting an operation recovery can't take over wasn't refused by name", report.reason, err)
		}
		if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recoveries WHERE workspace_id=$1`, r.ws); count != 0 {
			t.Fatal("the refused recovery wrote its recovery")
		}
	}

	// Left out as the refusal says, the peer no longer holds recovery back.
	r.revokePeer(t)
	r.recoverAll(t, exports[:1])
	if count := claimCounter(t, r.f, `SELECT count(*) FROM execution_operations WHERE id=$1 AND state='succeeded' AND command->'recoveryCheckpoint' IS NULL`, finished); count != 1 {
		t.Fatal("recovery revived the finished root")
	}
	if count := claimCounter(t, r.f, `SELECT count(*) FROM cross_host_recoveries WHERE parent_id=$1`, r.root.Id); count != 1 {
		t.Fatal("recovery didn't take over the interrupted root")
	}
}
