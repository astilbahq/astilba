package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys"
	"github.com/astilbahq/astilba/services/platform/internal/awskeys/awsfake"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/google/uuid"
)

// managedBoundary is the documented permissions boundary for the fixture's bucket.
const managedBoundary = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["s3:GetObject","s3:PutObject","s3:DeleteObject","s3:AbortMultipartUpload","s3:ListBucket"],"Resource":["arn:aws:s3:::managed-backups","arn:aws:s3:::managed-backups/*"]}]}`

// keySweep and accountCheck queue the managed-key jobs by their River kinds.
type keySweep struct{}

func (keySweep) Kind() string { return "platform_backup_key_sweep_v1" }

type accountCheck struct{}

func (accountCheck) Kind() string { return "platform_backup_account_check_v1" }

// keysFixture is a binding fixture whose management mints storage keys in a fake AWS, with one
// storage account.
type keysFixture struct {
	*bindingFixture
	managedAccount
	ws uuid.UUID
}

func newKeysFixture(t *testing.T) *keysFixture {
	t.Helper()
	x := newBindingFixture(t)
	a := newManagedAccount(t, x.f, x.b, x.first.ws)
	return &keysFixture{bindingFixture: x, managedAccount: a, ws: x.first.ws}
}

// managedAccount is a fake AWS account with the fixture's bucket, that f's management mints keys
// in, added to workspace ws as its storage account.
type managedAccount struct {
	aws      *awsfake.AWS
	client   *awskeys.Client
	boundary string
	account  api.BackupStorageAccount
}

func newManagedAccount(t *testing.T, f *fixture, b *browser, ws uuid.UUID) managedAccount {
	t.Helper()
	fake := awsfake.New()
	t.Cleanup(fake.Close)
	arn := fake.AddPolicy("astilba-backup-boundary", managedBoundary)
	admin := fake.AddAdmin(awsfake.Admin{Name: "astilba-admin", Path: "/", Within: awskeys.Path(ws.String()), Boundary: arn})
	fake.AddBucket("managed-backups", "eu-west-2")
	client, err := awskeys.NewFixture(fake.URL)
	if err != nil {
		t.Fatal(err)
	}
	f.execution.AWS = client
	a := managedAccount{aws: fake, client: client, boundary: arn}
	b.call("POST", "/api/v1/workspaces/"+ws.String()+"/backup-storage-accounts", map[string]any{"name": "Backups", "accessKeyId": admin.AccessKeyID, "secretAccessKey": admin.SecretAccessKey, "boundary": arn}, 201, &a.account)
	return a
}

// reach probes the bucket under scope with cred, for up to a second.
func (a managedAccount) reach(cred awskeys.Credential, scope string) error {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	return a.client.Probe(ctx, cred, awskeys.Spec{Bucket: "managed-backups", Region: "eu-west-2", Scope: scope})
}

// managed is a request for a managed destination on the fixture's bucket, bound to server.
func (k *keysFixture) managed(name string, server uuid.UUID) map[string]any {
	return map[string]any{"name": name, "kind": "s3", "serverId": server, "root": "", "endpoint": "", "bucket": "managed-backups", "region": "eu-west-2", "accountId": k.account.Id}
}

// run queues job and waits for it to complete.
func (k *keysFixture) run(t *testing.T, job interface{ Kind() string }) {
	t.Helper()
	runJob(t, k.f, job)
}

// runJob queues job at f's management and waits for it to complete.
func runJob(t *testing.T, f *fixture, job interface{ Kind() string }) {
	t.Helper()
	ctx := context.Background()
	inserted, err := f.execution.Queue.Insert(ctx, job, nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, job.Kind()+" didn't complete", 30*time.Second, func() bool {
		var state string
		if err := f.st.Pool.QueryRow(ctx, `SELECT state FROM river_job WHERE id=$1`, inserted.Job.ID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		return state == "completed"
	})
}

// key is the destination's only key, as its view shows it.
func (k *keysFixture) key(t *testing.T, destination uuid.UUID) api.BackupStorageKey {
	t.Helper()
	d, ok := k.destinations()[destination]
	if !ok || len(d.Keys) != 1 {
		t.Fatalf("destination %s = %+v, want one key", destination, d)
	}
	return d.Keys[0]
}

// delivered is the access a check of policy gives its server.
func (k *keysFixture) delivered(t *testing.T, policy uuid.UUID) backup.Access {
	t.Helper()
	var op api.Operation
	k.b.call("POST", k.environment(k.first)+"/backup-policies/"+policy.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}, 202, &op)
	status, c := k.poll(t, k.first)
	var b backup.Command
	if status != 200 || c == nil || c.Id != op.Id || c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil {
		t.Fatalf("polled %d %+v, want the check %s", status, c, op.Id)
	}
	agentRequest(t, k.f.server.Client(), k.f.server.URL, "/agent/v1/report", k.first.token, api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	return b.Installation.Access
}

// A managed destination's key is minted for its server alone, reaches only the workspace's part of
// the bucket, and is given only to that server. Revoking the server revokes it at AWS: the
// destination is exposed until AWS confirms, and for the hour after that IAM may take to stop
// honouring it.
func TestAManagedKeyIsScopedAndRevokedWithItsServer(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	if d.AccountId == nil || *d.AccountId != k.account.Id || d.Endpoint != "https://s3.eu-west-2.amazonaws.com" || len(d.Keys) != 1 || d.Keys[0].State != "active" || d.Keys[0].ServerId != k.first.server {
		t.Fatalf("created %+v", d)
	}
	path := awskeys.Path(k.ws.String())
	if users := k.aws.Users(path); len(users) != 1 || users[0] != d.Keys[0].UserName || k.aws.Tags(users[0])["astilba:server"] != k.first.server.String() {
		t.Fatalf("AWS has users %v under %s", users, path)
	}
	// An owner-keyed request can't name an account and carry access too.
	both := k.managed("Both", k.first.server)
	both["access"] = map[string]string{"accessKey": "owner-key", "secretKey": "owner-secret", "password": ""}
	wantRefusedField(t, <-k.b.send("POST", k.base+"/backup-destinations", both), "access")
	given := k.managed("Elsewhere", k.first.server)
	given["endpoint"] = "https://storage.example.test"
	wantRefusedField(t, <-k.b.send("POST", k.base+"/backup-destinations", given), "endpoint")

	var policy backup.Policy
	k.b.call("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id), 201, &policy)
	access := k.delivered(t, policy.ID)
	cred := awskeys.Credential{AccessKeyID: access.AccessKey, SecretAccessKey: access.SecretKey}
	repository := "astilba/" + k.ws.String() + "/" + policy.ID.String() + "/"
	if keys := k.aws.Keys(d.Keys[0].UserName); !cred.Valid() || len(keys) != 1 || keys[0] != cred.AccessKeyID {
		t.Fatalf("the server was given %q, not the minted key", access.AccessKey)
	}
	if err := k.reach(cred, repository); err != nil {
		t.Fatal("the key can't reach its own repository:", err)
	}
	for _, outside := range []string{"", "astilba/", "astilba/" + uuid.NewString() + "/", "other/astilba/" + k.ws.String() + "/"} {
		if err := k.reach(cred, outside); !errors.Is(err, awskeys.ErrDenied) {
			t.Errorf("the key listed %q: %v", outside, err)
		}
	}

	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	if got, view := k.key(t, d.Id), k.destinations()[d.Id]; got.State != "revoking" || !view.Exposed {
		t.Fatalf("after the revocation the key is %s, exposed %v", got.State, view.Exposed)
	}
	k.run(t, keySweep{})
	if got := k.key(t, d.Id); got.State != "revoked" || got.RevokedAt == nil {
		t.Fatalf("after the sweep the key is %+v", got)
	}
	if users := k.aws.Users(path); len(users) != 0 {
		t.Fatalf("AWS still has %v", users)
	}
	if err := k.reach(cred, repository); !errors.Is(err, awskeys.ErrCredential) {
		t.Fatal("the revoked key still works:", err)
	}
	if !k.destinations()[d.Id].Exposed {
		t.Fatal("the destination stopped being exposed as soon as AWS confirmed")
	}
	k.exec(t, `UPDATE backup_storage_keys SET revoked_at=revoked_at - interval '2 hours' WHERE id=$1`, d.Keys[0].Id)
	if k.destinations()[d.Id].Exposed {
		t.Fatal("the destination is still exposed an hour after its key was revoked")
	}
}

// A revocation that hands a server's keys over to a workspace transfer leaves them working and
// unexposed, until the owner revokes them.
func TestAHandedOverKeyLivesUntilTheOwnerRevokesIt(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", map[string]bool{"handOverStorageKeys": true}, 204, nil)
	k.run(t, keySweep{})
	if got, view := k.key(t, d.Id), k.destinations()[d.Id]; got.State != "active" || !got.HandedOver || view.Exposed {
		t.Fatalf("a handed-over key is %+v, exposed %v", got, view.Exposed)
	}
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 1 {
		t.Fatal("the handed-over key's user was deleted")
	}
	k.b.call("POST", k.base+"/backup-destinations/"+d.Id.String()+"/keys/"+d.Keys[0].Id.String()+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})
	if got := k.key(t, d.Id); got.State != "revoked" {
		t.Fatalf("the owner's revocation left the key %s", got.State)
	}
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 0 {
		t.Fatal("the owner's revocation left the user")
	}
}

// Renewing a revoked server mints its destination a new key, and installs each policy on the
// destination at a new generation carrying it.
func TestARenewedServerGetsANewKey(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	var policy backup.Policy
	k.b.call("POST", k.environment(k.first)+"/backup-policies", k.policy(k.first, "Managed", d.Id), 201, &policy)
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	k.run(t, keySweep{})

	var renewal api.Enrollment
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/enrollment", nil, 201, &renewal)
	credential, err := randtoken.New()
	if err != nil {
		t.Fatal(err)
	}
	in := k.first.enroll
	in.Token, in.Credential = renewal.Token, &credential
	agentRequest(t, k.f.server.Client(), k.f.server.URL, "/agent/v1/enroll", "", in, 200, nil)
	k.first.token = k.first.server.String() + "." + credential
	k.run(t, keySweep{})
	var minted api.BackupStorageKey
	waitFor(t, "the renewed server's destination got no new key", 30*time.Second, func() bool {
		for _, key := range k.destinations()[d.Id].Keys {
			if key.Id != d.Keys[0].Id && key.State == "active" {
				minted = key
				return true
			}
		}
		return false
	})
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 1 || users[0] != minted.UserName {
		t.Fatalf("AWS has %v, want only the new key's user", users)
	}

	// The next pass queues the policy's install, which carries the new key.
	k.run(t, keySweep{})
	status, c := k.poll(t, k.first)
	var b backup.Command
	if status != 200 || c == nil || c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil || b.Action != "install" {
		t.Fatalf("polled %d %+v, want the policy's install", status, c)
	}
	if b.Installation.Policy.ID != policy.ID || b.Installation.Policy.Generation <= policy.Generation {
		t.Fatalf("installed %s at generation %d, want %s past %d", b.Installation.Policy.ID, b.Installation.Policy.Generation, policy.ID, policy.Generation)
	}
	if keys := k.aws.Keys(minted.UserName); len(keys) != 1 || b.Installation.Access.AccessKey != keys[0] {
		t.Fatalf("the install carries %q, not the new key", b.Installation.Access.AccessKey)
	}
	cred := awskeys.Credential{AccessKeyID: b.Installation.Access.AccessKey, SecretAccessKey: b.Installation.Access.SecretKey}
	if err := k.reach(cred, "astilba/"+k.ws.String()+"/"+policy.ID.String()+"/"); err != nil {
		t.Fatal("the new key can't reach the policy's repository:", err)
	}
}

// A workspace exported after a handover carries its storage account and handed-over key. The
// importing installation keeps the key working and lists the source as a peer, and revoking the
// server there, after renewing it, revokes the key.
func TestAHandedOverKeyMovesWithItsWorkspace(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	k.b.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", map[string]bool{"handOverStorageKeys": true}, 204, nil)
	const passphrase = "unique archive passphrase for fixture"
	var encrypted api.WorkspaceArchive
	k.b.call("POST", k.base+"/export", map[string]string{"passphrase": passphrase}, 200, &encrypted)

	to := setup(t)
	to.execution.AWS = k.client
	owner := to.browser(t)
	owner.login("owner@example.test", to.password)
	owner.call("POST", "/api/v1/workspaces/import", map[string]any{"archive": encrypted.Archive, "passphrase": passphrase}, 201, nil)
	var list []api.BackupDestination
	owner.call("GET", k.base+"/backup-destinations", nil, 200, &list)
	var moved *api.BackupDestination
	for i := range list {
		if list[i].Id == d.Id {
			moved = &list[i]
		}
	}
	if moved == nil || len(moved.Keys) != 1 || moved.Keys[0].State != "active" || !moved.Keys[0].HandedOver || moved.Exposed {
		t.Fatalf("imported %+v", moved)
	}
	var accounts []api.BackupStorageAccount
	owner.call("GET", k.base+"/backup-storage-accounts", nil, 200, &accounts)
	if len(accounts) != 1 || len(accounts[0].Peers) != 1 || accounts[0].Peers[0] != k.account.InstallationId || accounts[0].InstallationId == k.account.InstallationId {
		t.Fatalf("imported accounts %+v, want the source listed as a peer", accounts)
	}
	runJob(t, to, keySweep{})
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 1 {
		t.Fatal("the import revoked the handed-over key")
	}

	owner.call("POST", k.base+"/servers/"+k.first.server.String()+"/enrollment", nil, 201, nil)
	owner.call("POST", k.base+"/servers/"+k.first.server.String()+"/revoke", nil, 204, nil)
	runJob(t, to, keySweep{})
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 0 {
		t.Fatalf("revoking the server at its new installation left %v", users)
	}
}

// A mint AWS refuses fails the create, and the sweep revokes what AWS made and removes the
// destination, which nothing could use.
func TestAFailedMintLeavesNothingBehind(t *testing.T) {
	k := newKeysFixture(t)
	k.aws.Fail("CreateAccessKey", "AccessDenied")
	reply := <-k.b.send("POST", k.base+"/backup-destinations", k.managed("Refused", k.first.server))
	if reply.status != 400 {
		t.Fatalf("a refused mint answered %d %s", reply.status, reply.body)
	}
	if len(k.aws.Users(awskeys.Path(k.ws.String()))) != 1 {
		t.Fatal("the refused mint made no user to clean up")
	}
	// The sweep waits out the mint's deadline, which a request still minting might be inside.
	k.exec(t, `UPDATE backup_storage_keys SET mint_deadline=now() - interval '10 minutes'`)
	k.run(t, keySweep{})
	if users := k.aws.Users(awskeys.Path(k.ws.String())); len(users) != 0 {
		t.Fatalf("AWS still has %v", users)
	}
	for _, d := range k.destinations() {
		if d.AccountId != nil {
			t.Fatalf("the placeholder %+v is still listed", d)
		}
	}
}

// The account check reports a user under the workspace's path that management never made, and the
// owner's revocation of it deletes it. Another access key on a user management made is deleted.
func TestTheAccountCheckReportsUsersManagementDidntMake(t *testing.T) {
	k := newKeysFixture(t)
	var d api.BackupDestination
	k.b.call("POST", k.base+"/backup-destinations", k.managed("Managed", k.first.server), 201, &d)
	path := awskeys.Path(k.ws.String())
	stranger := "astilba-" + uuid.NewString()
	k.aws.CreateUser(stranger, path, k.boundary, map[string]string{"astilba:installation": uuid.NewString()}, "")
	k.aws.CreateUser("unbounded", path, "", nil, "")
	k.exec(t, `UPDATE backup_storage_keys SET activated_at=now() - interval '2 hours' WHERE id=$1`, d.Keys[0].Id)
	extra := k.aws.IssueKey(d.Keys[0].UserName)
	k.run(t, accountCheck{})
	var accounts []api.BackupStorageAccount
	k.b.call("GET", k.base+"/backup-storage-accounts", nil, 200, &accounts)
	if len(accounts) != 1 || accounts[0].CheckError != nil || len(accounts[0].FoundUsers) != 2 || accounts[0].ExtraKeysDeleted != 1 {
		t.Fatalf("the check found %+v", accounts)
	}
	for _, id := range k.aws.Keys(d.Keys[0].UserName) {
		if id == extra.AccessKeyID {
			t.Fatal("the extra access key wasn't deleted")
		}
	}
	var alert bool
	if err := k.f.st.Pool.QueryRow(context.Background(), `SELECT (report->>'alert')::bool FROM backup_storage_accounts WHERE id=$1`, k.account.Id).Scan(&alert); err != nil || !alert {
		t.Fatal("the check raised no alert", err)
	}
	k.b.call("POST", k.base+"/backup-storage-accounts/"+k.account.Id.String()+"/found-users/"+stranger+"/revoke", nil, 204, nil)
	k.b.call("POST", k.base+"/backup-storage-accounts/"+k.account.Id.String()+"/found-users/someone-else/revoke", nil, 404, nil)
	k.run(t, keySweep{})
	for _, u := range k.aws.Users(path) {
		if u == stranger {
			t.Fatal("the owner's revocation left the found user")
		}
	}
}
