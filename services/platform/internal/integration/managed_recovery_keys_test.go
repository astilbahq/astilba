package integration

import (
	"encoding/json/v2"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
)

// A restore of a lost server's snapshot from a managed destination is given a key of its own,
// minted for the new primary and reaching only the policy's repository, and the key is revoked
// once the restore ends. A repeated request reuses the restore and its key.
func TestARestoreFromAManagedDestinationGetsItsOwnKey(t *testing.T) {
	g := newRevokedGroup(t, 3)
	a := newManagedAccount(t, g.f, g.b, g.ws)
	l := g.lostFilesOn(t, map[string]any{"name": "Managed", "kind": "s3", "serverId": g.hosts[0].id, "root": "", "endpoint": "", "bucket": "managed-backups", "region": "eu-west-2", "accountId": a.account.Id})
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})

	request := recoveryRequest(l.snapshot)
	got := g.recover(l, l.policy, request)
	var op api.Operation
	if got.err != nil || got.status != http.StatusAccepted || json.Unmarshal(got.body, &op) != nil {
		t.Fatalf("restore = %d %s %v", got.status, got.body, got.err)
	}
	if again := g.recover(l, l.policy, request); again.status != http.StatusAccepted || !strings.Contains(string(again.body), op.Id.String()) {
		t.Fatalf("a repeated request started another restore: %d %s", again.status, again.body)
	}
	recoveryKeys := func() []api.BackupStorageKey {
		var list []api.BackupDestination
		g.b.call("GET", g.base+"/backup-destinations", nil, 200, &list)
		var out []api.BackupStorageKey
		for _, d := range list {
			if d.Id == l.policy.DestinationID {
				for _, k := range d.Keys {
					if k.Purpose == "recovery" {
						out = append(out, k)
					}
				}
			}
		}
		return out
	}
	if keys := recoveryKeys(); len(keys) != 1 || keys[0].ServerId != l.b.id {
		t.Fatalf("the restore's keys are %+v, want one for the new primary", keys)
	}
	key := recoveryKeys()[0]
	var accessKey, scope string
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT access_key_id,scope FROM backup_storage_keys WHERE id=$1`, key.Id).Scan(&accessKey, &scope); err != nil {
		t.Fatal(err)
	}
	if want := "astilba/" + g.ws.String() + "/" + l.policy.ID.String() + "/"; scope != want {
		t.Fatalf("the restore's key reaches %q, want only %q", scope, want)
	}
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}
	_, r := g.restoreCommand(t, l.b)
	if r.Installation.Access.AccessKey != accessKey || r.Installation.Access.Password != "fixture-repository-password" {
		t.Fatalf("the restore was released %q, not its own key", r.Installation.Access.AccessKey)
	}
	cred := awskeys.Credential{AccessKeyID: accessKey, SecretAccessKey: r.Installation.Access.SecretKey}

	// The restore has ended, so the sweep revokes its key.
	if _, err := g.f.execution.Queue.Insert(g.ctx, keySweep{}, nil); err != nil {
		t.Fatal(err)
	}
	g.waitFor(t, "the restore's key to be revoked", func() bool {
		keys := recoveryKeys()
		return len(keys) == 1 && keys[0].State == "revoked"
	})
	for _, u := range a.aws.Users(awskeys.Path(g.ws.String())) {
		if u == key.UserName {
			t.Fatal("the restore's user is still at AWS")
		}
	}
	if err := a.reach(cred, "astilba/"+g.ws.String()+"/"+l.policy.ID.String()+"/"); !errors.Is(err, awskeys.ErrCredential) {
		t.Fatal("the restore's revoked key still works:", err)
	}
}
