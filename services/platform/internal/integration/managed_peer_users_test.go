package integration

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/google/uuid"
)

// peerUsers is a keys fixture whose storage account lists one peer installation.
type peerUsers struct {
	*keysFixture
	peer    uuid.UUID
	path    string
	account string
}

func newPeerUsers(t *testing.T) *peerUsers {
	t.Helper()
	k := newKeysFixture(t)
	p := &peerUsers{keysFixture: k, peer: uuid.New(), path: awskeys.Path(k.ws.String()), account: k.base + "/backup-storage-accounts/" + k.account.Id.String()}
	k.b.call("POST", p.account+"/peers", api.BackupStorageAccountPeerRequest{InstallationId: p.peer}, 204, nil)
	return p
}

// check runs the account check to its report, then the alert sweep, and returns what the account
// shows.
func (p *peerUsers) check(t *testing.T) api.BackupStorageAccount {
	t.Helper()
	p.b.call("POST", p.account+"/check", nil, 204, nil)
	var got api.BackupStorageAccount
	waitFor(t, "the account check didn't report", 30*time.Second, func() bool {
		p.run(t, accountCheck{})
		got = p.view(t)
		return !got.Checking
	})
	if got.CheckError != nil {
		t.Fatalf("the account check failed: %s", *got.CheckError)
	}
	alertSweep(t, p.f, p.ws)
	return got
}

func (p *peerUsers) view(t *testing.T) api.BackupStorageAccount {
	t.Helper()
	var list []api.BackupStorageAccount
	p.b.call("GET", p.base+"/backup-storage-accounts", nil, 200, &list)
	if len(list) != 1 {
		t.Fatalf("accounts %+v", list)
	}
	return list[0]
}

// found is the found user named name, as the account shows it.
func found(t *testing.T, a api.BackupStorageAccount, name string) api.BackupStorageFoundUser {
	t.Helper()
	for _, u := range a.FoundUsers {
		if u.UserName == name {
			return u
		}
	}
	t.Fatalf("%s isn't among the found users %+v", name, a.FoundUsers)
	return api.BackupStorageFoundUser{}
}

// alerts are the states of the workspace's alerts of kind, newest first, by resource.
func (p *peerUsers) alerts(t *testing.T, kind api.AlertKind) map[uuid.UUID][]api.Alert {
	t.Helper()
	var inbox api.AlertInbox
	p.b.call("GET", p.base+"/alerts?view=all", nil, 200, &inbox)
	out := map[uuid.UUID][]api.Alert{}
	for _, a := range inbox.Alerts {
		if a.Kind == kind {
			out[a.ResourceId] = append(out[a.ResourceId], a)
		}
	}
	return out
}

func (p *peerUsers) firing(t *testing.T, resource uuid.UUID) bool {
	t.Helper()
	list := p.alerts(t, api.BackupPeerUser)[resource]
	return len(list) > 0 && list[0].State == api.AlertFiring
}

// audited is how many audited acknowledgements name, by their own record, the found user identity.
func (p *peerUsers) audited(t *testing.T, identity uuid.UUID) int {
	t.Helper()
	var n int
	if err := p.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events e JOIN backup_storage_acknowledgements k ON k.workspace_id=e.workspace_id AND k.id=e.resource_id WHERE e.workspace_id=$1 AND e.action='backup.key.acknowledge' AND k.identity=$2`, p.ws, identity).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func (p *peerUsers) acknowledge(t *testing.T, u api.BackupStorageFoundUser, status int) {
	t.Helper()
	p.b.call("POST", p.account+"/found-users/"+u.UserName+"/acknowledge", api.BackupStorageAcknowledgementRequest{UserId: u.UserId, AccessKeyIds: u.AccessKeyIds}, status, nil)
}

// A user tagged with a peer installation raises its own alert the first time a check finds it, and
// keeps it until the owner acknowledges that user by name, which is audited with the
// acknowledgement's own record. The acknowledgement covers the user as the owner reviewed it:
// another access key on it, or a user made again under the name, raises the alert again, and a user
// that goes away ends its alert.
// Peer users never raise backup_account, which stays for users the account check can't place.
func TestAPeerUserAlertsUntilItIsAcknowledgedByName(t *testing.T) {
	p := newPeerUsers(t)
	name := "astilba-" + uuid.NewString()
	p.aws.CreateUser(name, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String(), "astilba:server": uuid.NewString()}, "")
	got := found(t, p.check(t), name)
	if !got.Peer || !got.Bounded || got.AcknowledgedAt != nil || got.UserId != p.aws.UserID(name) || len(got.AccessKeyIds) != 1 || got.AccessKeyIds[0] != p.aws.Keys(name)[0] {
		t.Fatalf("the peer's user is shown as %+v", got)
	}
	if !p.firing(t, got.Id) {
		t.Fatalf("the peer's user raised no alert: %+v", p.alerts(t, api.BackupPeerUser))
	}
	if len(p.alerts(t, api.BackupAccount)) != 0 {
		t.Fatalf("the peer's user raised backup_account: %+v", p.alerts(t, api.BackupAccount))
	}
	// A check that finds the user again doesn't raise a second alert.
	p.check(t)
	if list := p.alerts(t, api.BackupPeerUser)[got.Id]; len(list) != 1 {
		t.Fatalf("two checks raised %+v", list)
	}

	// The owner acknowledges the user as they reviewed it: another IAM user ID or other keys than
	// the latest check found are refused, as are users that don't exist.
	stale := got
	stale.UserId = "AIDAOTHER"
	p.acknowledge(t, stale, 409)
	stale = got
	stale.AccessKeyIds = []string{}
	p.acknowledge(t, stale, 409)
	stale.UserName, stale.AccessKeyIds = "astilba-"+uuid.NewString(), got.AccessKeyIds
	p.acknowledge(t, stale, 404)
	alertSweep(t, p.f, p.ws)
	if !p.firing(t, got.Id) {
		t.Fatal("a refused acknowledgement cleared the alert")
	}
	p.acknowledge(t, got, 204)
	if audited := p.audited(t, got.Id); audited != 1 {
		t.Fatalf("the acknowledgement was audited %d times", audited)
	}
	if acknowledged := found(t, p.view(t), name); acknowledged.AcknowledgedAt == nil {
		t.Fatalf("the acknowledged user is shown as %+v", acknowledged)
	}
	// The alert clears at once, and stays clear across checks.
	alertSweep(t, p.f, p.ws)
	if p.firing(t, got.Id) {
		t.Fatal("the acknowledgement didn't clear the alert")
	}
	if found(t, p.check(t), name).AcknowledgedAt == nil || p.firing(t, got.Id) {
		t.Fatal("a check after the acknowledgement raised the alert again")
	}

	// Another access key isn't covered by the acknowledgement.
	p.aws.IssueKey(name)
	again := found(t, p.check(t), name)
	if again.AcknowledgedAt != nil || again.Id != got.Id || len(again.AccessKeyIds) != 2 || !p.firing(t, got.Id) {
		t.Fatalf("a user given another key is shown as %+v, alerting %v", again, p.firing(t, got.Id))
	}
	p.acknowledge(t, again, 204)
	// Each acknowledgement is kept and audited on its own.
	if audited := p.audited(t, got.Id); audited != 2 {
		t.Fatalf("two acknowledgements were audited %d times", audited)
	}
	// A key deleted since is still covered.
	p.aws.DeleteKey(again.AccessKeyIds[0])
	if found(t, p.check(t), name).AcknowledgedAt == nil || p.firing(t, got.Id) {
		t.Fatal("a user with fewer keys than acknowledged raised the alert")
	}

	// A user made again under the name is another user.
	p.aws.DeleteUser(name)
	p.aws.CreateUser(name, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	remade := found(t, p.check(t), name)
	if remade.Id == got.Id || remade.AcknowledgedAt != nil || !p.firing(t, remade.Id) || p.firing(t, got.Id) {
		t.Fatalf("the user made again is shown as %+v", remade)
	}
	// A user with no access keys is acknowledged as having none.
	p.aws.DeleteKey(remade.AccessKeyIds[0])
	keyless := found(t, p.check(t), name)
	if len(keyless.AccessKeyIds) != 0 || keyless.AcknowledgedAt != nil || !p.firing(t, remade.Id) {
		t.Fatalf("the user without keys is shown as %+v", keyless)
	}
	p.acknowledge(t, keyless, 204)
	if found(t, p.check(t), name).AcknowledgedAt == nil || p.firing(t, remade.Id) {
		t.Fatal("the user without keys still alerts once acknowledged")
	}
	// A user that goes away ends its alert.
	p.aws.DeleteUser(name)
	if p.check(t); p.firing(t, remade.Id) {
		t.Fatal("a user that's gone still alerts")
	}
}

// Each peer user raises its own alert, so one acknowledged in the inbox doesn't hide the next, and
// only bounded users tagged with a listed peer's canonical installation ID can be acknowledged:
// others raise backup_account, and acknowledging them is refused.
func TestEachPeerUserHasItsOwnAlert(t *testing.T) {
	p := newPeerUsers(t)
	first, second := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	p.aws.CreateUser(first, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	a := found(t, p.check(t), first)
	alert := p.alerts(t, api.BackupPeerUser)[a.Id]
	if len(alert) != 1 {
		t.Fatalf("the first user's alerts are %+v", alert)
	}
	p.b.call("POST", p.base+"/alerts/"+alert[0].Id.String()+"/acknowledge", nil, 204, nil)
	p.aws.CreateUser(second, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	b := found(t, p.check(t), second)
	if !p.firing(t, a.Id) || !p.firing(t, b.Id) {
		t.Fatalf("the peer user alerts are %+v", p.alerts(t, api.BackupPeerUser))
	}
	var inbox api.AlertInbox
	p.b.call("GET", p.base+"/alerts?view=attention", nil, 200, &inbox)
	seen := false
	for _, x := range inbox.Alerts {
		seen = seen || (x.Kind == api.BackupPeerUser && x.ResourceId == b.Id)
	}
	if !seen {
		t.Fatalf("the second user's alert needs no attention: %+v", inbox.Alerts)
	}
	// Acknowledging one user clears only its own alert.
	p.acknowledge(t, b, 204)
	alertSweep(t, p.f, p.ws)
	if !p.firing(t, a.Id) || p.firing(t, b.Id) {
		t.Fatalf("after acknowledging the second user the alerts are %+v", p.alerts(t, api.BackupPeerUser))
	}

	// A tag that isn't the peer's canonical ID, a user without the boundary, and another
	// installation's user aren't a peer's.
	upper, unbounded, stranger := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	p.aws.CreateUser(upper, p.path, p.boundary, map[string]string{"astilba:installation": strings.ToUpper(p.peer.String())}, "")
	p.aws.CreateUser(unbounded, p.path, "", map[string]string{"astilba:installation": p.peer.String()}, "")
	p.aws.CreateUser(stranger, p.path, p.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	view := p.check(t)
	for _, name := range []string{upper, unbounded, stranger} {
		u := found(t, view, name)
		if u.Peer && u.Bounded {
			t.Fatalf("%s is shown as a peer's: %+v", name, u)
		}
		if p.firing(t, u.Id) {
			t.Fatalf("%s raised backup_peer_user", name)
		}
		p.acknowledge(t, u, 409)
	}
	if list := p.alerts(t, api.BackupAccount)[p.keysFixture.account.Id]; len(list) == 0 || list[0].State != api.AlertFiring {
		t.Fatalf("users that aren't a peer's raised no backup_account alert: %+v", list)
	}
	var audited int
	if err := p.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='backup.key.acknowledge'`, p.ws).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("refused acknowledgements were audited: %d, %v", audited, err)
	}

	// A firing user that goes away ends its alert, unacknowledged.
	p.aws.DeleteUser(first)
	if p.check(t); p.firing(t, a.Id) {
		t.Fatal("a peer user that's gone still alerts")
	}
}

// The owner can revoke a peer's user they haven't acknowledged, which may have been made with a
// leaked admin key, but not one they have: that is the peer's. Each revocation is audited with
// the record management keeps for the user, and the user's alert ends once it is known. An
// acknowledgement of a user already covered is refused, so acknowledgements stay as many as the
// users reviewed.
func TestAnUnacknowledgedPeerUserCanBeRevoked(t *testing.T) {
	p := newPeerUsers(t)
	kept, suspect := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	p.aws.CreateUser(kept, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	p.aws.CreateUser(suspect, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	view := p.check(t)
	a, b := found(t, view, kept), found(t, view, suspect)
	p.acknowledge(t, a, 204)
	p.acknowledge(t, a, 409)
	if audited := p.audited(t, a.Id); audited != 1 {
		t.Fatalf("a refused acknowledgement was audited: %d", audited)
	}
	p.b.call("POST", p.account+"/found-users/"+kept+"/revoke", nil, 409, nil)
	// Revoking queues a key sweep, which could finish the revocation before the account is read,
	// so it is held at its first call, which only a revocation makes, until then.
	release := make(chan struct{})
	p.aws.Before(func(action string) {
		if action == "DeleteUserPolicy" {
			select {
			case <-release:
			case <-time.After(30 * time.Second):
			}
		}
	})
	t.Cleanup(func() { p.aws.Before(nil) })
	p.b.call("POST", p.account+"/found-users/"+suspect+"/revoke", nil, 204, nil)
	// Revoking it again, before a check drops it from the report, is refused and not audited.
	p.b.call("POST", p.account+"/found-users/"+suspect+"/revoke", nil, 409, nil)
	var audited int
	if err := p.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events e JOIN backup_storage_keys k ON k.workspace_id=e.workspace_id AND k.id=e.resource_id WHERE e.workspace_id=$1 AND e.action='backup.key.revoke' AND k.user_name=$2`, p.ws, suspect).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("the revocation was audited %d times by its record: %v", audited, err)
	}
	var revokes int
	if err := p.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='backup.key.revoke'`, p.ws).Scan(&revokes); err != nil || revokes != 1 {
		t.Fatalf("revocations were audited %d times: %v", revokes, err)
	}
	if !found(t, p.view(t), suspect).Revoking {
		t.Fatal("the revoked peer user isn't shown as revoking")
	}
	close(release)
	// A sweep asked for as one finishes waits for the next (PLAT-640), so sweeps run until the
	// user is gone.
	waitFor(t, "the sweep didn't revoke the peer user", 30*time.Second, func() bool {
		p.run(t, keySweep{})
		return p.aws.UserID(suspect) == ""
	})
	if p.aws.UserID(kept) == "" {
		t.Fatal("the sweep revoked the acknowledged peer user")
	}
	p.check(t)
	if p.firing(t, b.Id) || p.firing(t, a.Id) {
		t.Fatalf("after the revocation the alerts are %+v", p.alerts(t, api.BackupPeerUser))
	}
}

// A sweep raises a bounded number of peer user alerts, and the next raises the rest, so a leaked
// admin key making many users can't push a workspace's sweep past its time budget.
func TestManyPeerUsersAlertOverSeveralSweeps(t *testing.T) {
	p := newPeerUsers(t)
	const users = 250
	for range users {
		p.aws.CreateUser("astilba-"+uuid.NewString(), p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	}
	raised := func() int {
		var n int
		if err := p.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM operational_alerts WHERE workspace_id=$1 AND kind='backup_peer_user' AND state='firing'`, p.ws).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	p.check(t)
	if got := raised(); got == 0 || got >= users {
		t.Fatalf("one sweep raised %d of %d alerts", got, users)
	}
	alertSweep(t, p.f, p.ws)
	if got := raised(); got != users {
		t.Fatalf("two sweeps raised %d of %d alerts", got, users)
	}
}

// A found user deleted between the check's listing and its listing of the user's access keys is
// left out of the report, as one deleted before the listing is, and the check still succeeds.
func TestAFoundUserDeletedDuringTheCheckIsLeftOut(t *testing.T) {
	p := newPeerUsers(t)
	kept, deleted := "astilba-"+uuid.NewString(), "astilba-"+uuid.NewString()
	p.aws.CreateUser(kept, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	p.aws.CreateUser(deleted, p.path, p.boundary, map[string]string{"astilba:installation": p.peer.String()}, "")
	// The user is deleted once the check has listed the users.
	var listed atomic.Bool
	var once sync.Once
	p.aws.Before(func(action string) {
		switch {
		case action == "ListUsers":
			listed.Store(true)
		case action == "ListAccessKeys" && listed.Load():
			once.Do(func() { p.aws.DeleteUser(deleted) })
		}
	})
	t.Cleanup(func() { p.aws.Before(nil) })
	view := p.check(t)
	found(t, view, kept)
	for _, u := range view.FoundUsers {
		if u.UserName == deleted {
			t.Fatalf("the deleted user is shown as %+v", u)
		}
	}
	if p.aws.UserID(deleted) != "" {
		t.Fatal("the user wasn't deleted during the check")
	}
}
