package integration

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys/awsfake"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

// A mint that stopped after AWS made its user, before management activated or latched the key,
// leaves the key minting. Once its deadline has passed the sweep revokes what AWS made, and the
// destination, which never had an active key, goes with it. Until then no policy can use it.
func TestAMintCutShortIsRevokedOnceItsDeadlinePasses(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	if d.Placeholder || k.destinations()[d.Id].Placeholder {
		t.Fatal("a destination with an active key is shown as a placeholder")
	}
	k.exec(t, `UPDATE backup_storage_keys SET state='minting',activated_at=NULL,access_key_id=NULL,encrypted_secret=NULL WHERE id=$1`, d.Keys[0].Id)
	if !k.destinations()[d.Id].Placeholder {
		t.Fatal("a destination whose key was never active isn't shown as a placeholder")
	}

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
// last failed, and the check that follows no longer raises the alert. A peer's user the owner
// acknowledged can't be revoked here, and a full peer list refuses another.
func TestFoundUsersShowTheirRevocationAndPeersAreLeftAlone(t *testing.T) {
	k := newKeysFixture(t)
	path := awskeys.Path(k.ws.String())
	stranger, peer := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	peerInstallation := uuid.New()
	k.aws.CreateUser(stranger, path, k.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	k.aws.CreateUser(peer, path, k.boundary, map[string]string{"astilba:installation": peerInstallation.String()}, "")
	account := k.base + "/backup-storage-accounts/" + k.account.Id.String()
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
	// The check that adding the peer asks for fails, so the latest report still has the peer's
	// user as a stranger's. The account's peers are what count.
	k.aws.Fail("ListUsers", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	k.b.call("POST", account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: peerInstallation}, 204, nil)
	k.run(t, accountCheck{})
	reported()
	got := accounts()
	if got.CheckError == nil {
		t.Fatalf("the check after adding the peer didn't fail: %+v", got)
	}
	var peerUser api.BackupStorageFoundUser
	for _, u := range got.FoundUsers {
		if u.Peer != (u.UserName == peer) {
			t.Fatalf("with the peer added, the found user is shown as %+v", u)
		}
		if u.UserName == peer {
			peerUser = u
		}
	}
	k.b.call("POST", account+"/found-users/"+peer+"/acknowledge", api.BackupStorageAcknowledgementRequest{UserId: peerUser.UserId, AccessKeyIds: peerUser.AccessKeyIds}, 204, nil)
	k.b.call("POST", account+"/found-users/"+peer+"/revoke", nil, 409, nil)

	// The check lists found users' access keys too, so the failures go to a call only the
	// revocation makes.
	k.aws.Fail("DeleteUserPolicy", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	k.b.call("POST", account+"/found-users/"+stranger+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	k.run(t, accountCheck{})
	reported()
	got = accounts()
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

// A storage account's credential can be replaced only with one of the same AWS account, whose
// users are the ones minted so far; another account's key is refused for what it is.
func TestAReplacementCredentialMustBelongToTheSameAccount(t *testing.T) {
	k := newKeysFixture(t)
	credential := k.base + "/backup-storage-accounts/" + k.account.Id.String() + "/credential"
	other := k.aws.AddAdmin(awsfake.Admin{Name: "other-admin", Path: "/", Account: "210987654321", Within: awskeys.Path(k.ws.String()), Boundary: k.boundary})
	reply := <-k.b.send("PUT", credential, api.BackupStorageCredentialRequest{AccessKeyId: &other.AccessKeyID, SecretAccessKey: &other.SecretAccessKey, Generation: k.account.Generation})
	if reply.status != 400 || !strings.Contains(string(reply.body), "another AWS account") || !strings.Contains(string(reply.body), `"credential"`) {
		t.Fatalf("another account's key answered %d %s", reply.status, reply.body)
	}
	same := k.aws.AddAdmin(awsfake.Admin{Name: "second-admin", Path: "/", Within: awskeys.Path(k.ws.String()), Boundary: k.boundary})
	var replaced api.BackupStorageAccount
	k.b.call("PUT", credential, api.BackupStorageCredentialRequest{AccessKeyId: &same.AccessKeyID, SecretAccessKey: &same.SecretAccessKey, Generation: k.account.Generation}, 200, &replaced)
	if replaced.Generation == k.account.Generation {
		t.Fatalf("the replacement left the account at generation %d", replaced.Generation)
	}
}

// An install that can't start because its environment is already running something waits for the
// next pass, and the sweep goes on to revoke what is due.
func TestABusyEnvironmentDoesntHoldUpRevocation(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	var policy backup.Policy
	k.b.call("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id), 201, &policy)
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	minted := k.renew(t, d)

	// The environment runs a check, so the install the next pass queues has to wait, while a
	// found user the owner revoked is due. The key's installs are held back until then; one a
	// sweep queued before that fails, so the next try is the one the busy environment refuses.
	k.exec(t, `UPDATE backup_storage_keys SET install_next_at=now() + interval '1 hour' WHERE id=$1`, minted.Id)
	if status, c := k.poll(t, k.first); status == 200 && c != nil {
		agentRequest(t, k.f.server.Client(), k.f.server.URL, "/agent/v1/report", k.first.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "failed", Steps: []api.ExecutionStep{}}, 204, nil)
	}
	k.b.call("POST", k.environment(k.first)+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}, 202, nil)
	k.exec(t, `UPDATE backup_storage_keys SET install_next_at=NULL WHERE id=$1`, minted.Id)
	path := awskeys.Path(k.ws.String())
	stranger := "astilba-" + uuid.NewString()
	k.aws.CreateUser(stranger, path, k.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	account := k.base + "/backup-storage-accounts/" + k.account.Id.String()
	k.b.call("POST", account+"/check", nil, 204, nil)
	k.run(t, accountCheck{})
	waitFor(t, "the account check didn't report the stranger", 30*time.Second, func() bool {
		var list []api.BackupStorageAccount
		k.b.call("GET", k.base+"/backup-storage-accounts", nil, 200, &list)
		return len(list) == 1 && !list[0].Checking && len(list[0].FoundUsers) == 1
	})
	k.b.call("POST", account+"/found-users/"+stranger+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	if slices.Contains(k.aws.Users(path), stranger) {
		t.Fatal("with an install waiting, the found user wasn't revoked")
	}
	var pending int
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM execution_operations WHERE workspace_id=$1 AND kind='backup' AND state NOT IN ('succeeded','failed','cancelled')`, k.ws).Scan(&pending); err != nil || pending != 1 {
		t.Fatal("the busy environment holds", pending, "unfinished backup operations", err)
	}
}

// keyState is a key's state and last error as its row has them.
func (k *keysFixture) keyState(t *testing.T, id uuid.UUID) (string, string) {
	t.Helper()
	var state string
	var last *string
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT state,last_error FROM backup_storage_keys WHERE id=$1`, id).Scan(&state, &last); err != nil {
		t.Fatal(err)
	}
	if last == nil {
		return state, ""
	}
	return state, *last
}

// A key whose IAM user is gone at AWS is revoked on the next pass, whether IAM answers a call on
// the missing user by name with NoSuchEntity or, under the path-scoped admin policy, with
// AccessDenied: the key itself no longer works.
func TestAKeyWhoseUserIsGoneIsRevokedWhateverIAMAnswers(t *testing.T) {
	for _, report := range []bool{false, true} {
		k := newKeysFixture(t)
		k.aws.ReportMissingUsers(report)
		var d api.BackupDestination
		k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
		k.aws.DeleteUser(d.Keys[0].UserName)
		// The account check asks IAM only about a key active well before it began.
		k.exec(t, `UPDATE backup_storage_keys SET activated_at=now() - interval '2 hours' WHERE id=$1`, d.Keys[0].Id)
		k.b.call("POST", k.base+"/backup-storage-accounts/"+k.account.Id.String()+"/check", nil, 204, nil)
		k.run(t, accountCheck{})
		waitFor(t, "the account check didn't find the user gone", 30*time.Second, func() bool { s, _ := k.keyState(t, d.Keys[0].Id); return s != "active" })
		k.exec(t, `UPDATE backup_storage_keys SET next_attempt_at=now() WHERE id=$1`, d.Keys[0].Id)
		k.run(t, keySweep{})
		if s, e := k.keyState(t, d.Keys[0].Id); s != "revoked" {
			t.Fatalf("reporting missing users %v, the key whose user is gone is %s (%s)", report, s, e)
		}
	}
}

// A user someone with wider rights moved off the workspace's path is refused by name and unlisted,
// as a missing one is, but its key still works: it stays revoking, and the account alerts.
func TestAKeyWhoseUserMovedOffThePathStaysRevoking(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.aws.MoveUser(d.Keys[0].UserName, "/elsewhere/")
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/"+d.Keys[0].Id.String()+"/revoke", nil, 204, nil)
	alerts := func() bool {
		t.Helper()
		alerting, err := db.New(k.f.st.Pool).AlertingBackupStorageAccounts(context.Background(), k.ws)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(alerting, k.account.Id)
	}
	for range 3 {
		k.exec(t, `UPDATE backup_storage_keys SET next_attempt_at=now() WHERE id=$1`, d.Keys[0].Id)
		k.run(t, keySweep{})
		if s, e := k.keyState(t, d.Keys[0].Id); s != "revoking" || e != "moved_off_path" {
			t.Fatalf("the key whose user was moved is %s (%s)", s, e)
		}
	}
	if !alerts() {
		t.Fatal("the account doesn't alert for the moved user's key")
	}
	// An attempt that fails for another reason leaves the alert standing.
	k.aws.Fail("GetCallerIdentity", "ServiceFailure", "ServiceFailure", "ServiceFailure")
	k.exec(t, `UPDATE backup_storage_keys SET next_attempt_at=now() WHERE id=$1`, d.Keys[0].Id)
	k.run(t, keySweep{})
	if s, e := k.keyState(t, d.Keys[0].Id); s != "revoking" || e != "unavailable" || !alerts() {
		t.Fatalf("after a failed attempt the moved user's key is %s (%s), alerting %v", s, e, alerts())
	}
}

// A found user the owner deleted in the AWS console before revoking it here is revoked at once:
// the check listed it under the path, so being refused by name and unlisted means it is gone.
// A mint that never finished, whose user was never made, waits until the user would be listed.
func TestUnlistedUsersWithoutAKeyAreRevokedOnceTheyWouldBeListed(t *testing.T) {
	k := newKeysFixture(t)
	path := awskeys.Path(k.ws.String())
	stranger := "astilba-" + uuid.NewString()
	k.aws.CreateUser(stranger, path, k.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	account := k.base + "/backup-storage-accounts/" + k.account.Id.String()
	k.b.call("POST", account+"/check", nil, 204, nil)
	k.run(t, accountCheck{})
	waitFor(t, "the account check didn't report the stranger", 30*time.Second, func() bool {
		var list []api.BackupStorageAccount
		k.b.call("GET", k.base+"/backup-storage-accounts", nil, 200, &list)
		return len(list) == 1 && !list[0].Checking && len(list[0].FoundUsers) == 1
	})
	k.aws.DeleteUser(stranger)
	k.b.call("POST", account+"/found-users/"+stranger+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	var found string
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT state FROM backup_storage_keys WHERE user_name=$1`, stranger).Scan(&found); err != nil || found != "revoked" {
		t.Fatal("the found user deleted in the console is", found, err)
	}

	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.aws.DeleteUser(d.Keys[0].UserName)
	k.exec(t, `UPDATE backup_storage_keys SET state='revoking',activated_at=NULL,access_key_id=NULL,encrypted_secret=NULL,mint_deadline=now() - interval '10 minutes',next_attempt_at=now() WHERE id=$1`, d.Keys[0].Id)
	k.run(t, keySweep{})
	if s, e := k.keyState(t, d.Keys[0].Id); s != "revoking" || e != "not_listed" {
		t.Fatalf("an unfinished mint's missing user, just after its mint, is %s (%s)", s, e)
	}
	k.exec(t, `UPDATE backup_storage_keys SET created_at=now() - interval '20 minutes',next_attempt_at=now() WHERE id=$1`, d.Keys[0].Id)
	k.run(t, keySweep{})
	// Revoked, the key goes with its destination, which never had an active key.
	if _, ok := k.destinations()[d.Id]; ok {
		t.Fatalf("an unfinished mint's missing user, well after its mint, is %v", k.destinations()[d.Id].Keys)
	}
}
