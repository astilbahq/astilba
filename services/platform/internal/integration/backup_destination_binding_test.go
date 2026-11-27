package integration

import (
	"context"
	"encoding/json/v2"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/google/uuid"
)

// bindingFixture is two servers in one workspace, each with a deployed database, an S3 destination
// bound to it and a backup policy there.
type bindingFixture struct {
	f            *fixture
	b            *browser
	base         string
	first, other *reportingPolicy
}

func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	f := setup(t)
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	return &bindingFixture{f: f, b: b, base: "/api/v1/workspaces/" + ws.String(), first: newReportingPolicy(t, f, b, ws, "First"), other: newReportingPolicy(t, f, b, ws, "Other")}
}

// environment is p's environment's route.
func (x *bindingFixture) environment(p *reportingPolicy) string {
	return strings.TrimSuffix(p.path, "/backup-policies/"+p.policy.ID.String())
}

// policy is a request for a new policy backing up p's database to destination.
func (x *bindingFixture) policy(p *reportingPolicy, name string, destination uuid.UUID) api.BackupPolicyRequest {
	password := "repository-password-fixture"
	return api.BackupPolicyRequest{Name: name, ResourceId: p.policy.ResourceID, DestinationId: destination, Method: "postgres-logical", Schedule: api.DailySchedule{Hour: 4, Minute: 0, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}
}

// s3 is a request for an S3 destination with extra fields.
func s3Destination(name string, extra map[string]any) map[string]any {
	out := map[string]any{"name": name, "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "binding-fixture", "region": "test-1", "access": map[string]string{"accessKey": "binding-key", "secretKey": "binding-secret-canary", "password": ""}}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func (x *bindingFixture) destinations() map[uuid.UUID]api.BackupDestination {
	var list []api.BackupDestination
	x.b.call("GET", x.base+"/backup-destinations", nil, 200, &list)
	out := map[uuid.UUID]api.BackupDestination{}
	for _, d := range list {
		out[d.Id] = d
	}
	return out
}

// exec runs statements against the fixture's database.
func (x *bindingFixture) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := x.f.st.Pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(sql, err)
	}
}

// poll is the next command p's server is sent, or the status refusing it.
func (x *bindingFixture) poll(t *testing.T, p *reportingPolicy) (int, *api.ExecutionCommand) {
	t.Helper()
	req, _ := http.NewRequest("GET", x.f.server.URL+"/agent/v1/poll", nil)
	req.Header.Set("Authorization", "Bearer "+p.token)
	res, err := x.f.server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 {
		return res.StatusCode, nil
	}
	var out api.AgentPoll
	if err = json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	acknowledgePolled(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/poll", p.token, &out)
	return 200, out.Command
}

// A new S3 destination is bound to the server it names, which must be a deployment server, and a
// filesystem one to none. Only policies on its server can use it, and the list says which that is.
// A prefix needs an agent that keeps repositories under it.
func TestABackupDestinationIsBoundToOneServer(t *testing.T) {
	x := newBindingFixture(t)
	refused := x.policy(x.other, "Borrowed", x.first.policy.DestinationID)
	wantRefusedField(t, <-x.b.send("POST", x.environment(x.other)+"/backup-policies", refused), "destination.server")
	listed := x.destinations()
	first, other := listed[x.first.policy.DestinationID], listed[x.other.policy.DestinationID]
	if first.ServerId == nil || *first.ServerId != x.first.server || first.Shared || other.ServerId == nil || *other.ServerId != x.other.server {
		t.Fatalf("destinations = %+v and %+v, want each bound to its own server", first, other)
	}
	if got := first.Policies; len(got) != 1 || got[0].Id != x.first.policy.ID {
		t.Fatalf("the first destination's policies = %+v", got)
	}
	// Its own server's policies can use it.
	var again backup.Policy
	x.b.call("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Again", x.first.policy.DestinationID), 201, &again)

	wantRefusedField(t, <-x.b.send("POST", x.base+"/backup-destinations", s3Destination("Unbound", nil)), "serverId")
	local := map[string]any{"name": "Local", "kind": "filesystem", "serverId": x.first.server, "root": "/srv/backups", "endpoint": "", "bucket": "", "region": "", "access": map[string]string{"accessKey": "", "secretKey": "", "password": ""}}
	wantRefusedField(t, <-x.b.send("POST", x.base+"/backup-destinations", local), "serverId")
	if got := <-x.b.send("POST", x.base+"/backup-destinations", s3Destination("Nowhere", map[string]any{"serverId": uuid.New()})); got.status != http.StatusNotFound {
		t.Fatalf("a destination for an unknown server = %d %s", got.status, got.body)
	}
	x.exec(t, "UPDATE servers SET role='build' WHERE id=$1", x.other.server)
	if got := <-x.b.send("POST", x.base+"/backup-destinations", s3Destination("Builder", map[string]any{"serverId": x.other.server})); got.status != http.StatusForbidden {
		t.Fatalf("a destination for a build server = %d %s", got.status, got.body)
	}
	x.exec(t, "UPDATE servers SET role='deployment' WHERE id=$1", x.other.server)

	wantRefusedField(t, <-x.b.send("POST", x.base+"/backup-destinations", s3Destination("Bad prefix", map[string]any{"serverId": x.first.server, "prefix": "servers/../other"})), "destination")
	prefixed := s3Destination("Prefixed", map[string]any{"serverId": x.first.server, "prefix": "servers/first"})
	wantRefusedField(t, <-x.b.send("POST", x.base+"/backup-destinations", prefixed), "server.capabilities.backupPrefixes")
	x.exec(t, `UPDATE servers SET capabilities = capabilities || '{"backupPrefixes": true}' WHERE id=$1`, x.first.server)
	var created api.BackupDestination
	x.b.call("POST", x.base+"/backup-destinations", prefixed, 201, &created)
	if created.Prefix == nil || *created.Prefix != "servers/first" || created.ServerId == nil || *created.ServerId != x.first.server {
		t.Fatalf("created %+v", created)
	}
	var onPrefix backup.Policy
	x.b.call("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Prefixed", created.Id), 201, &onPrefix)
	// An agent that stops reading prefixes is refused the destination's commands rather than sent
	// to the bucket's root.
	x.exec(t, `UPDATE servers SET capabilities = capabilities - 'backupPrefixes' WHERE id=$1`, x.first.server)
	wantRefusedField(t, <-x.b.send("POST", x.environment(x.first)+"/backup-policies/"+onPrefix.ID.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}), "server.capabilities.backupPrefixes")
}

// A destination's storage keys are delivered only to the server it is bound to, with its prefix,
// or to the servers that shared it before destinations were bound. A command for a server the
// destination no longer admits is sent without them.
func TestOnlyADestinationsServerIsGivenItsKeys(t *testing.T) {
	x := newBindingFixture(t)
	x.exec(t, `UPDATE servers SET capabilities = capabilities || '{"backupPrefixes": true}' WHERE id=$1`, x.first.server)
	var prefixed api.BackupDestination
	x.b.call("POST", x.base+"/backup-destinations", s3Destination("Prefixed", map[string]any{"serverId": x.first.server, "prefix": "servers/first"}), 201, &prefixed)
	var policy backup.Policy
	x.b.call("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Prefixed", prefixed.Id), 201, &policy)
	check := func() api.Operation {
		t.Helper()
		var op api.Operation
		x.b.call("POST", x.environment(x.first)+"/backup-policies/"+policy.ID.String()+"/commands", api.BackupCommandRequest{Action: "check", RequestKey: uuid.New()}, 202, &op)
		return op
	}
	var polled *api.ExecutionCommand
	delivered := func(op api.Operation) (int, backup.Command) {
		t.Helper()
		status, c := x.poll(t, x.first)
		var b backup.Command
		if c == nil {
			return status, b
		}
		if c.Id != op.Id || c.Backup == nil || json.Unmarshal(*c.Backup, &b) != nil {
			t.Fatalf("polled %+v, want the check %s", c, op.Id)
		}
		polled = c
		return status, b
	}
	op := check()
	status, got := delivered(op)
	if status != 200 || got.Installation.Access.SecretKey != "binding-secret-canary" || got.Installation.Access.AccessKey != "binding-key" || got.Installation.Destination.Prefix != "servers/first" || got.Installation.Destination.ID != prefixed.Id {
		t.Fatalf("the bound server was given %d %+v", status, got.Installation)
	}
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/report", x.first.token, api.AgentReport{Id: polled.Id, Generation: polled.Generation, PlanHash: polled.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)

	// Bound to the other server, as no request can make it, the keys aren't sent.
	x.exec(t, "UPDATE backup_destinations SET server_id=$2 WHERE id=$1", prefixed.Id, x.other.server)
	op = check()
	if status, got = delivered(op); status == 200 && got.Installation.Access.SecretKey != "" {
		t.Fatalf("a server the destination isn't bound to was given its keys: %+v", got.Installation.Access)
	}
	// A shared destination's keys still reach the servers whose policies used it.
	x.exec(t, "UPDATE backup_destinations SET server_id=NULL, shared=true WHERE id=$1", prefixed.Id)
	for until := time.Now().Add(20 * time.Second); ; {
		if status, got = delivered(op); status == 200 && got.Installation.Access.SecretKey == "binding-secret-canary" {
			break
		}
		if time.Now().After(until) {
			t.Fatalf("a shared destination's server wasn't given its keys: %d %+v", status, got.Installation.Access)
		}
		time.Sleep(200 * time.Millisecond)
	}
	agentRequest(t, x.f.server.Client(), x.f.server.URL, "/agent/v1/report", x.first.token, api.AgentReport{Id: polled.Id, Generation: polled.Generation, PlanHash: polled.PlanHash, Sequence: 1, State: "succeeded", Steps: []api.ExecutionStep{}}, 204, nil)
	// A new policy can't choose it, while its own can still be changed.
	wantRefusedField(t, <-x.b.send("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Shared", prefixed.Id)), "destination.shared")
	update := x.policy(x.first, "Renamed", prefixed.Id)
	update.BaseGeneration = policy.Generation
	x.b.call("PUT", x.environment(x.first)+"/backup-policies/"+policy.ID.String(), update, 200, &policy)
}

// A destination no policy has used yet, as one from before destinations were bound can be, is bound
// by its first policy. Two policies on different servers can't both bind it: the second is decided
// under the destination's lock, once the first's binding is committed.
func TestOnlyOneServerBindsAnUnboundDestination(t *testing.T) {
	x := newBindingFixture(t)
	ctx := context.Background()
	unbound := func(name string) uuid.UUID {
		t.Helper()
		var d api.BackupDestination
		x.b.call("POST", x.base+"/backup-destinations", s3Destination(name, map[string]any{"serverId": x.first.server}), 201, &d)
		x.exec(t, "UPDATE backup_destinations SET server_id=NULL WHERE id=$1", d.Id)
		if x.destinations()[d.Id].ServerId != nil {
			t.Fatal("the destination is still bound")
		}
		return d.Id
	}

	// Sent together, one policy binds the destination and the other is refused.
	for i := range 3 {
		destination := unbound("Raced " + string(rune('a'+i)))
		first := x.b.send("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Raced", destination))
		other := x.b.send("POST", x.environment(x.other)+"/backup-policies", x.policy(x.other, "Raced", destination))
		a, b := <-first, <-other
		winner := x.first.server
		switch {
		case a.status == http.StatusCreated && b.status == http.StatusBadRequest:
			wantRefusedField(t, b, "destination.server")
		case b.status == http.StatusCreated && a.status == http.StatusBadRequest:
			wantRefusedField(t, a, "destination.server")
			winner = x.other.server
		default:
			t.Fatalf("raced policies = %d %s and %d %s, want one created", a.status, a.body, b.status, b.body)
		}
		if got := x.destinations()[destination]; got.ServerId == nil || *got.ServerId != winner || got.Shared || len(got.Policies) != 1 {
			t.Fatalf("the raced destination = %+v, want bound to %s", got, winner)
		}
	}

	// A binding committed while the policy waits for the destination's lock is the one it sees.
	destination := unbound("Held")
	tx, err := x.f.st.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT 1 FROM backup_destinations WHERE id=$1 FOR UPDATE", destination); err != nil {
		t.Fatal(err)
	}
	waiting := x.b.send("POST", x.environment(x.first)+"/backup-policies", x.policy(x.first, "Waiting", destination))
	for until := time.Now().Add(20 * time.Second); ; {
		var n int
		if err = x.f.st.Pool.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE wait_event_type='Lock' AND query LIKE '%FROM backup_destinations%FOR UPDATE%'").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 1 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("the policy never waited for the destination's lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err = tx.Exec(ctx, "UPDATE backup_destinations SET server_id=$2 WHERE id=$1", destination, x.other.server); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wantRefusedField(t, <-waiting, "destination.server")
}
