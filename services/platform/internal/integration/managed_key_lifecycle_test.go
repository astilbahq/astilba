package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/google/uuid"
)

// A mint that stopped after AWS made its user, before management activated or latched the key,
// leaves the key minting. Once its deadline has passed the sweep revokes what AWS made, and the
// destination, which never had an active key, goes with it. Until then no policy can use it.
func TestAMintCutShortIsRevokedOnceItsDeadlinePasses(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.exec(t, `UPDATE backup_storage_keys SET state='minting',activated_at=NULL,access_key_id=NULL,encrypted_secret=NULL WHERE id=$1`, d.Keys[0].Id)

	reply := <-k.b.send("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id))
	if reply.status != 400 || !strings.Contains(string(reply.body), "destination.key") {
		t.Fatalf("a policy on a destination with no active key answered %d %s", reply.status, reply.body)
	}

	k.run(t, keySweep{})
	if got := k.key(t, d.Id); got.State != "minting" {
		t.Fatalf("a key inside its mint deadline is %s", got.State)
	}
	// The mint's deadline has passed, and the five minutes the sweep waits after it.
	k.exec(t, `UPDATE backup_storage_keys SET mint_deadline=now() - interval '10 minutes' WHERE id=$1`, d.Keys[0].Id)
	k.run(t, keySweep{})
	k.run(t, keySweep{})
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 0 {
		t.Fatalf("AWS still has %v", users)
	}
	if _, ok := k.destinations()[d.Id]; ok {
		t.Fatal("the destination that never had an active key is still listed")
	}
}

// Revoking a live server's key, or any key whose holder isn't revoked, exposes nothing: no
// revoked server ever held it.
func TestRevokingALiveServersKeyExposesNothing(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/"+d.Keys[0].Id.String()+"/revoke", nil, 204, nil)
	revoked := func() (api.BackupStorageKey, bool) {
		view := k.destinations()[d.Id]
		for _, key := range view.Keys {
			if key.Id == d.Keys[0].Id {
				return key, view.Exposed
			}
		}
		t.Fatalf("the revoked key is gone from %+v", view)
		return api.BackupStorageKey{}, false
	}
	if got, exposed := revoked(); got.State == "active" || exposed {
		t.Fatalf("the owner's revocation left the key %s, exposed %v", got.State, exposed)
	}
	k.run(t, keySweep{})
	if got, exposed := revoked(); got.State != "revoked" || exposed {
		t.Fatalf("after AWS confirmed, the key is %s, exposed %v", got.State, exposed)
	}
}

// A found user the owner revokes shows as revoking from its own row, with why its revocation
// last failed, and the check that follows no longer raises the alert. A peer's user can't be
// revoked here, and a full peer list refuses another.
func TestFoundUsersShowTheirRevocationAndPeersAreLeftAlone(t *testing.T) {
	k := newKeysFixture(t)
	path := awskeys.Path(k.ws.String())
	stranger, peer := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	peerInstallation := uuid.New()
	k.aws.CreateUser(stranger, path, k.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	k.aws.CreateUser(peer, path, k.boundary, map[string]string{"astilba:installation": peerInstallation.String()}, "")
	account := k.base + "/backup-storage-accounts/" + k.account.Id.String()
	k.b.call("POST", account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: peerInstallation}, 204, nil)
	k.run(t, accountCheck{})
	accounts := func() api.BackupStorageAccount {
		t.Helper()
		var list []api.BackupStorageAccount
		k.b.call("GET", k.base+"/backup-storage-accounts", nil, 200, &list)
		if len(list) != 1 {
			t.Fatalf("accounts %+v", list)
		}
		return list[0]
	}
	reported := func() {
		t.Helper()
		waitFor(t, "the account check didn't report", 30*time.Second, func() bool { return !accounts().Checking })
	}
	reported()
	if got := accounts(); got.CheckedAt == nil || len(got.FoundUsers) != 2 {
		t.Fatalf("after the check the account is %+v", got)
	}
	k.b.call("POST", account+"/found-users/"+peer+"/revoke", nil, 409, nil)

	k.aws.Fail("PutUserPolicy", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	k.b.call("POST", account+"/found-users/"+stranger+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	k.run(t, accountCheck{})
	reported()
	got := accounts()
	var shown *api.BackupStorageFoundUser
	for i, u := range got.FoundUsers {
		if u.UserName == stranger {
			shown = &got.FoundUsers[i]
		}
	}
	if shown == nil || !shown.Revoking || shown.LastError == nil || *shown.LastError != "unavailable" {
		t.Fatalf("the found user being revoked is shown as %+v", shown)
	}
	var alert bool
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT (report->>'alert')::bool FROM backup_storage_accounts WHERE id=$1`, k.account.Id).Scan(&alert); err != nil || alert {
		t.Fatal("the check after the revocation still raised the alert", err)
	}

	k.exec(t, `UPDATE backup_storage_keys SET next_attempt_at=now()`)
	k.run(t, keySweep{})
	for _, u := range accounts().FoundUsers {
		if u.UserName == stranger {
			t.Fatalf("the revoked user is still shown: %+v", u)
		}
	}

	// The account lists one peer; fifteen more fill it, and re-adding one changes nothing.
	for range 15 {
		k.b.call("POST", account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: uuid.New()}, 204, nil)
	}
	k.b.call("POST", account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: peerInstallation}, 204, nil)
	k.b.call("POST", account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: uuid.New()}, 409, nil)
}
