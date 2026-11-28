package integration

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
)

// audits counts the workspace's audit events of action on resource.
func (k *keysFixture) audits(t *testing.T, action string, resource uuid.UUID) int {
	t.Helper()
	var n int
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action=$2 AND resource_id=$3`, k.ws, action, resource).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// serverKeys are a destination's server keys by state, as its view shows them.
func (k *keysFixture) serverKeys(destination uuid.UUID) map[string][]api.BackupStorageKey {
	out := map[string][]api.BackupStorageKey{}
	for _, key := range k.destinations()[destination].Keys {
		if key.Purpose == "server" {
			out[string(key.State)] = append(out[string(key.State)], key)
		}
	}
	return out
}

// rotated waits for a key other than old to become active on destination, and returns it.
func (k *keysFixture) rotated(t *testing.T, destination uuid.UUID, old uuid.UUID) api.BackupStorageKey {
	t.Helper()
	var minted api.BackupStorageKey
	waitFor(t, "the destination got no new key", 30*time.Second, func() bool {
		for _, key := range k.serverKeys(destination)["active"] {
			if key.Id != old {
				minted = key
				return true
			}
		}
		return false
	})
	return minted
}

// installed has the first server take the install the sweep queued and report the policy at its
// generation, returning the install.
func (k *keysFixture) installed(t *testing.T) backup.Command {
	t.Helper()
	k.run(t, keySweep{})
	status, c := k.poll(t, k.first)
	var b backup.Command
	if status != 200 || c == nil || c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Action != "install" {
		t.Fatalf("polled %d %+v, want an install", status, c)
	}
	agentRequest(t, k.f.server.Client(), k.f.server.URL, "/agent/v1/report", k.first.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	k.first.sequence++
	raw, _ := stablejson.Marshal(backup.Status{Enabled: true, PolicyID: b.Installation.Policy.ID, Generation: b.Installation.Policy.Generation, Next: time.Now().Add(time.Hour), Runs: []backup.Run{}})
	agentRequest(t, k.f.server.Client(), k.f.server.URL, "/agent/v1/schedule-reports", k.first.token, map[string]any{"sequence": k.first.sequence, "status": jsontext.Value(raw)}, 204, nil)
	return b
}

// An owner's rotation mints the destination a new key and installs it on its policies while the
// old key keeps working. Once the server reports the new generation, the old key is superseded
// and revoked at AWS, and the server is never revoked. The request is audited, and refused while
// it waits or while the rotation is under way.
func TestAnOwnerRotatesAKeyWithoutRevokingItsServer(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	var policy backup.Policy
	k.b.call("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id), 201, &policy)
	old := d.Keys[0]
	rotate := k.base + "/backup-destinations/" + d.Id.String() + "/keys/rotate"

	k.b.call("POST", rotate, nil, 204, nil)
	if k.audits(t, "backup.key.rotate", d.Id) != 1 {
		t.Fatal("the rotation wasn't audited")
	}
	if view := k.destinations()[d.Id]; !view.RotationRequested || view.RotationDueAt == nil {
		t.Fatalf("after the request, the view is %+v", view)
	}
	if reply := <-k.b.send("POST", rotate, nil); reply.status != 409 {
		t.Fatalf("a second request answered %d %s", reply.status, reply.body)
	}

	k.run(t, keySweep{})
	minted := k.rotated(t, d.Id, old.Id)
	view := k.destinations()[d.Id]
	if view.RotationRequested || view.RotationDueAt != nil || len(k.serverKeys(d.Id)["active"]) != 2 {
		t.Fatalf("while rotating, the view is %+v", view)
	}
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 2 || !slices.Contains(users, old.UserName) || !slices.Contains(users, minted.UserName) {
		t.Fatalf("AWS has %v, want both keys' users", users)
	}
	if reply := <-k.b.send("POST", rotate, nil); reply.status != 409 {
		t.Fatalf("a request during the rotation answered %d %s", reply.status, reply.body)
	}

	// The old key stays active until the server has installed the new one.
	b := k.installed(t)
	if b.Installation.Policy.ID != policy.ID || b.Installation.Policy.Generation <= policy.Generation {
		t.Fatalf("installed %s at generation %d, want %s past %d", b.Installation.Policy.ID, b.Installation.Policy.Generation, policy.ID, policy.Generation)
	}
	if keys := k.aws.Keys(minted.UserName); len(keys) != 1 || b.Installation.Access.AccessKey != keys[0] {
		t.Fatalf("the install carries %q, not the new key", b.Installation.Access.AccessKey)
	}
	if state, _ := k.keyState(t, old.Id); state != "active" {
		t.Fatalf("before the next pass the old key is %s", state)
	}
	k.run(t, keySweep{})
	if state, _ := k.keyState(t, old.Id); state != "revoked" {
		t.Fatalf("after the server installed the new key, the old one is %s", state)
	}
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 1 || users[0] != minted.UserName {
		t.Fatalf("AWS has %v, want only the new key's user", users)
	}
	var revoked bool
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT revoked FROM servers WHERE id=$1`, k.first.server).Scan(&revoked); err != nil || revoked {
		t.Fatal("the rotation revoked the server", err)
	}
	if view := k.destinations()[d.Id]; view.Exposed || view.RotationDueAt == nil {
		t.Fatalf("after the rotation, the view is %+v", view)
	}
}

// A storage account is added with its key rotation stated, here 90 days. Its owner can change it
// or turn it off, each audited, and the sweep replaces a key older than the period, and none while
// it is off.
func TestKeysRotateOnTheirAccountsSchedule(t *testing.T) {
	k := newKeysFixture(t)
	if k.account.KeyRotationDays == nil || *k.account.KeyRotationDays != 90 {
		t.Fatalf("the account rotates every %v days", k.account.KeyRotationDays)
	}
	unstated := map[string]any{"name": "Unstated", "accessKeyId": "AKIAEXAMPLE", "secretAccessKey": "secret", "boundary": k.boundary}
	if reply := <-k.b.send("POST", k.base+"/backup-storage-accounts", unstated); reply.status != 400 {
		t.Fatalf("an account without its key rotation answered %d %s", reply.status, reply.body)
	}
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	old := d.Keys[0]
	if view := k.destinations()[d.Id]; view.RotationDueAt == nil || view.RotationDueAt.Sub(*old.ActivatedAt) != 90*24*time.Hour {
		t.Fatalf("the key is due at %v, activated %v", view.RotationDueAt, old.ActivatedAt)
	}
	setting := k.base + "/backup-storage-accounts/" + k.account.Id.String() + "/key-rotation"
	for _, days := range []int{29, 366} {
		if reply := <-k.b.send("PUT", setting, map[string]int{"keyRotationDays": days}); reply.status != 400 {
			t.Fatalf("%d days answered %d %s", days, reply.status, reply.body)
		}
	}
	if reply := <-k.b.send("PUT", setting, map[string]any{}); reply.status != 400 {
		t.Fatalf("a request without the setting answered %d %s", reply.status, reply.body)
	}

	// Turned off, a key past any period stays.
	var account api.BackupStorageAccount
	k.b.call("PUT", setting, map[string]any{"keyRotationDays": nil}, 200, &account)
	if account.KeyRotationDays != nil || k.audits(t, "backup.account.rotation.off", k.account.Id) != 1 {
		t.Fatalf("turning rotation off gave %+v", account)
	}
	k.exec(t, `UPDATE backup_storage_keys SET activated_at=now() - interval '400 days' WHERE id=$1`, old.Id)
	if view := k.destinations()[d.Id]; view.RotationDueAt != nil {
		t.Fatalf("with rotation off, the key is due at %v", view.RotationDueAt)
	}
	k.run(t, keySweep{})
	// A claim would have moved the destination's next attempt on.
	var claimed bool
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT remint_next_at IS NOT NULL FROM backup_destinations WHERE id=$1`, d.Id).Scan(&claimed); err != nil || claimed {
		t.Fatal("with rotation off, the sweep claimed the destination", err)
	}

	k.b.call("PUT", setting, map[string]int{"keyRotationDays": 30}, 200, &account)
	if account.KeyRotationDays == nil || *account.KeyRotationDays != 30 || k.audits(t, "backup.account.rotation.set", k.account.Id) != 1 {
		t.Fatalf("setting 30 days gave %+v", account)
	}
	k.exec(t, `UPDATE backup_storage_keys SET activated_at=now() - interval '31 days' WHERE id=$1`, old.Id)
	k.run(t, keySweep{})
	minted := k.rotated(t, d.Id, old.Id)
	if view := k.destinations()[d.Id]; view.RotationRequested || view.RotationDueAt != nil {
		t.Fatalf("a scheduled rotation shows as %+v", view)
	}
	// With no policy to install, nothing uses the old key, so the next pass revokes it.
	k.run(t, keySweep{})
	if state, _ := k.keyState(t, old.Id); state != "revoked" {
		t.Fatalf("the old key is %s", state)
	}
	if view := k.destinations()[d.Id]; view.RotationDueAt == nil || view.RotationDueAt.Sub(*minted.ActivatedAt) != 30*24*time.Hour {
		t.Fatalf("the new key is due at %v, activated %v", view.RotationDueAt, minted.ActivatedAt)
	}
}

// A rotation whose new key AWS fails to make leaves the old key in use, shows why, and is tried
// again, as the owner's retry asks at once. Rotation is refused while the server is revoked.
func TestAFailedRotationKeepsTheOldKeyAndRetries(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	old := d.Keys[0]
	rotate := k.base + "/backup-destinations/" + d.Id.String() + "/keys/rotate"
	k.aws.Fail("CreateAccessKey", "AccessDenied")
	k.b.call("POST", rotate, nil, 204, nil)
	k.run(t, keySweep{})
	waitFor(t, "the failed rotation recorded no error", 30*time.Second, func() bool {
		return k.destinations()[d.Id].RemintError != nil
	})
	view := k.destinations()[d.Id]
	if *view.RemintError != "access_denied" || !view.RotationRequested || len(k.serverKeys(d.Id)["active"]) != 1 {
		t.Fatalf("after the failure, the view is %+v", view)
	}
	if state, _ := k.keyState(t, old.Id); state != "active" {
		t.Fatalf("the failed rotation left the old key %s", state)
	}

	// A job still finishing would keep the retry's job from being queued until the next claim.
	waitFor(t, "the failed rotation's job didn't finish", 30*time.Second, func() bool {
		var running bool
		if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_backup_remint_v1' AND state <> 'completed')`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		return !running
	})
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/retry", nil, 204, nil)
	k.run(t, keySweep{})
	k.rotated(t, d.Id, old.Id)
	if view := k.destinations()[d.Id]; view.RemintError != nil || view.RotationRequested {
		t.Fatalf("after the retry, the view is %+v", view)
	}

	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	if reply := <-k.b.send("POST", rotate, nil); reply.status != 409 {
		t.Fatalf("rotating a revoked server's key answered %d %s", reply.status, reply.body)
	}
	if reply := <-k.b.send("POST", k.base+"/backup-destinations/"+uuid.NewString()+"/keys/rotate", nil); reply.status != 404 {
		t.Fatalf("rotating an unknown destination answered %d %s", reply.status, reply.body)
	}
}

// A policy whose environment has retired uses no key, since retirement removed its schedule from
// the host, so it doesn't keep a rotated destination's old key active.
func TestARotationDoesntWaitForARetiredEnvironment(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	var policy backup.Policy
	k.b.call("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id), 201, &policy)
	old := d.Keys[0]
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/rotate", nil, 204, nil)
	k.run(t, keySweep{})
	k.rotated(t, d.Id, old.Id)
	k.run(t, keySweep{})
	if state, _ := k.keyState(t, old.Id); state != "active" {
		t.Fatalf("before the server installed the new key, the old one is %s", state)
	}

	// The environment retires before its server installs the new key.
	k.exec(t, `UPDATE environments SET state='retired',retired_at=now(),retired_by=(SELECT id FROM accounts ORDER BY created_at LIMIT 1)
		WHERE id=(SELECT environment_id FROM backup_policies WHERE id=$1)`, policy.ID)
	k.run(t, keySweep{})
	if state, _ := k.keyState(t, old.Id); state != "revoked" {
		t.Fatalf("after the environment retired, the old key is %s", state)
	}
	if reply := <-k.b.send("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/rotate", nil); reply.status != 204 {
		t.Fatalf("a later rotation answered %d %s", reply.status, reply.body)
	}
}

// A failed rotation's error stands only while the key still needs replacing: once the owner turns
// rotation off, the next claim clears it without minting. A created destination shows when its key
// is due, as the list does.
func TestARotationFailureClearsOnceNoKeyIsNeeded(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	if d.RotationDueAt == nil || !d.RotationDueAt.Equal(*k.destinations()[d.Id].RotationDueAt) {
		t.Fatalf("the created destination is due at %v", d.RotationDueAt)
	}
	old := d.Keys[0]
	k.aws.Fail("CreateAccessKey", "AccessDenied")
	k.exec(t, `UPDATE backup_storage_keys SET activated_at=now() - interval '100 days' WHERE id=$1`, old.Id)
	k.run(t, keySweep{})
	waitFor(t, "the due rotation recorded no error", 30*time.Second, func() bool {
		return k.destinations()[d.Id].RemintError != nil
	})
	waitFor(t, "the failed rotation's job didn't finish", 30*time.Second, func() bool {
		var running bool
		if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_backup_remint_v1' AND state <> 'completed')`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		return !running
	})

	k.b.call("PUT", k.base+"/backup-storage-accounts/"+k.account.Id.String()+"/key-rotation", map[string]any{"keyRotationDays": nil}, 200, nil)
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/retry", nil, 204, nil)
	k.run(t, keySweep{})
	waitFor(t, "the error outlived the rotation", 30*time.Second, func() bool {
		return k.destinations()[d.Id].RemintError == nil
	})
	if keys := k.serverKeys(d.Id); len(keys["active"]) != 1 || keys["active"][0].Id != old.Id || len(keys["minting"]) != 0 {
		t.Fatalf("with no key needed, the keys are %+v", keys)
	}
}

// Revoking a server clears its destinations' failures to make a new key, since none is made for it
// until it is renewed.
func TestRevokingAServerClearsItsRotationFailure(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.aws.Fail("CreateAccessKey", "AccessDenied")
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/rotate", nil, 204, nil)
	k.run(t, keySweep{})
	waitFor(t, "the failed rotation recorded no error", 30*time.Second, func() bool {
		return k.destinations()[d.Id].RemintError != nil
	})
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	if view := k.destinations()[d.Id]; view.RemintError != nil {
		t.Fatalf("after the server's revocation, the destination shows %q", *view.RemintError)
	}
}

func TestARevocationDuringARotationLeavesNoFailure(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	// The rotation's mint waits at AWS until the server has been revoked.
	minting, revoked := make(chan struct{}), make(chan struct{})
	var once sync.Once
	k.aws.Before(func(action string) {
		if action == "CreateAccessKey" {
			once.Do(func() {
				close(minting)
				select {
				case <-revoked:
				case <-time.After(30 * time.Second):
				}
			})
		}
	})
	t.Cleanup(func() { k.aws.Before(nil) })
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/rotate", nil, 204, nil)
	k.run(t, keySweep{})
	select {
	case <-minting:
	case <-time.After(30 * time.Second):
		t.Fatal("the rotation never minted a key")
	}
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	close(revoked)
	waitFor(t, "the rotation's job didn't finish", 30*time.Second, func() bool {
		var running bool
		if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM river_job WHERE kind='platform_backup_remint_v1' AND state <> 'completed')`).Scan(&running); err != nil {
			t.Fatal(err)
		}
		return !running
	})
	if view := k.destinations()[d.Id]; view.RemintError != nil {
		t.Fatalf("after a revocation during its rotation, the destination shows %q", *view.RemintError)
	}
}
