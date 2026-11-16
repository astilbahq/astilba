package integration

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// lostFiles is a files stack whose primary uploaded a snapshot to an S3 bucket before it was lost,
// with a second policy for the same volume that kept its repository on the server itself.
type lostFiles struct {
	e             primaryEnvironment
	gone, b       *aheadHost
	volume        uuid.UUID
	policy, local backup.Policy
	snapshot      string
}

func (g *revokedGroup) lostFiles(t *testing.T) lostFiles {
	t.Helper()
	l := lostFiles{gone: g.hosts[0], b: g.hosts[1], snapshot: snapshotID("e")}
	l.e, l.volume = g.filesStack(t, l.gone)
	var bucket backup.Destination
	g.b.call("POST", g.base+"/backup-destinations", map[string]any{"name": "Bucket", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "recovery-fixture", "region": "test-1", "access": map[string]string{"accessKey": "storage-key", "secretKey": "storage-secret", "password": ""}}, 201, &bucket)
	password := "fixture-repository-password"
	request := api.BackupPolicyRequest{Name: "Files", ResourceId: l.volume, DestinationId: bucket.ID, Method: "quiesced-files", Schedule: api.DailySchedule{Hour: 2, Minute: 30, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}
	g.b.call("POST", l.e.path+"/backup-policies", request, 201, &l.policy)
	request.Name, request.DestinationId = "Local files", g.destination(t)
	g.b.call("POST", l.e.path+"/backup-policies", request, 201, &l.local)
	at := time.Now().UTC()
	for _, p := range []backup.Policy{l.policy, l.local} {
		run := backup.Run{ID: uuid.New(), PolicyID: p.ID, Generation: p.Generation, ScheduledAt: at, StartedAt: at, FinishedAt: &at, State: "succeeded", Snapshot: l.snapshot}
		if err := g.f.execution.ScheduleReport(g.ctx, l.gone.identity, 1, backup.Status{Enabled: true, PolicyID: p.ID, Generation: p.Generation, Next: at.Add(time.Hour), Runs: []backup.Run{run}}); err != nil {
			t.Fatal(err)
		}
	}
	return l
}

func (g *revokedGroup) recover(l lostFiles, policy backup.Policy, in api.BackupRecoveryRequest) reply {
	return <-g.b.send("POST", l.e.path+"/backup-policies/"+policy.ID.String()+"/recoveries", in)
}

func (g *revokedGroup) recoveries(l lostFiles) []api.BackupRecoveryRecord {
	var out []api.BackupRecoveryRecord
	g.b.call("GET", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", nil, 200, &out)
	return out
}

// restoreCommand is the restore the host was last released, with what it carried.
func (g *revokedGroup) restoreCommand(t *testing.T, h *aheadHost) (api.ExecutionCommand, backup.Recovery) {
	t.Helper()
	g.mu.Lock()
	c, ok := g.restores[h.id]
	g.mu.Unlock()
	var r backup.Recovery
	if !ok || json.Unmarshal(*c.Backup, &r) != nil {
		t.Fatal("no restore command released to", h.id)
	}
	return c, r
}

// pollFor polls in the place of a paused host, once its last poll's lease has ended.
func (g *revokedGroup) pollFor(h *aheadHost) (*api.ExecutionCommand, error) {
	for {
		poll, done := context.WithTimeout(g.ctx, 200*time.Millisecond)
		c, err := pollReleased(g.f.execution, poll, h.identity)
		expired := poll.Err()
		done()
		if waitForFixturePollLease(g.ctx, err) {
			continue
		}
		if err != nil && expired != nil {
			// A long poll that ends with nothing to send.
			return nil, nil
		}
		return c, err
	}
}

// pollUntil polls in the place of a paused host until it is sent a command or refused one.
func (g *revokedGroup) pollUntil(t *testing.T, h *aheadHost) (c *api.ExecutionCommand, err error) {
	t.Helper()
	g.waitFor(t, "a poll to answer", func() bool {
		c, err = g.pollFor(h)
		return c != nil || err != nil
	})
	return c, err
}

func recoveryRequest(snapshot string) api.BackupRecoveryRequest {
	return api.BackupRecoveryRequest{Snapshot: snapshot, RequestKey: uuid.New(), Acknowledged: true}
}

// A lost server's S3 snapshot can be restored onto the environment's new primary, once the old
// server is revoked and the new one's agent can run the restore. The restore runs there alone,
// with the policy's repository access, and its report is recorded against the restore.
func TestRestoringALostServersSnapshotOnTheNewPrimary(t *testing.T) {
	g := newRevokedGroup(t, 3)
	l := g.lostFiles(t)
	other := g.hosts[2]

	// A server that still answers restores its own snapshots.
	wantRefusedField(t, g.recover(l, l.policy, recoveryRequest(l.snapshot)), "policy.server")
	g.revoke(t, l.gone)
	// The environment still runs on the revoked server.
	wantRefusedField(t, g.recover(l, l.policy, recoveryRequest(l.snapshot)), "deployment")
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	// An agent without the restore kind would hold the command unsettled.
	wantRefusedField(t, g.recover(l, l.policy, recoveryRequest(l.snapshot)), "server.capabilities.recoveryRestore")
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	unacknowledged := recoveryRequest(l.snapshot)
	unacknowledged.Acknowledged = false
	// The contract admits only true, so the request is refused before it is read.
	wantRefusedField(t, g.recover(l, l.policy, unacknowledged), "request")
	// Only a snapshot management received from the policy's server can be chosen.
	wantRefusedField(t, g.recover(l, l.policy, recoveryRequest(snapshotID("f"))), "snapshot")
	// A filesystem repository was on the lost server.
	wantRefusedField(t, g.recover(l, l.local, recoveryRequest(l.snapshot)), "policy.destination")
	if got := g.recoveries(l); len(got) != 0 {
		t.Fatal("a refused restore was recorded", got)
	}

	request := recoveryRequest(l.snapshot)
	got := g.recover(l, l.policy, request)
	var op api.Operation
	if got.err != nil || got.status != http.StatusAccepted || json.Unmarshal(got.body, &op) != nil {
		t.Fatalf("restore = %d %s %v", got.status, got.body, got.err)
	}
	if again := g.recover(l, l.policy, request); again.status != http.StatusAccepted || !strings.Contains(string(again.body), op.Id.String()) {
		t.Fatalf("a repeated request started another restore: %d %s", again.status, again.body)
	}
	changed := request
	changed.Snapshot = snapshotID("f")
	if conflict := g.recover(l, l.policy, changed); conflict.status != http.StatusConflict {
		t.Fatalf("a request key reused for another snapshot = %d %s", conflict.status, conflict.body)
	}
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}
	c, r := g.restoreCommand(t, l.b)
	if c.ServerId != l.b.id || r.Installation.Policy.ID != l.policy.ID || r.Installation.Policy.ServerID != l.b.id || r.Snapshot != l.snapshot {
		t.Fatalf("restore released as %+v", r)
	}
	if a := r.Installation.Access; a.AccessKey != "storage-key" || a.SecretKey != "storage-secret" || a.Password != "fixture-repository-password" {
		t.Fatal("the restore wasn't released with the policy's repository access")
	}
	if g.received(other, "restore") || g.received(l.gone, "restore") {
		t.Fatal("a restore reached a server other than the new primary")
	}
	records := g.recoveries(l)
	if len(records) != 1 {
		t.Fatalf("%d restores recorded", len(records))
	}
	record := records[0]
	if record.OperationId != op.Id || record.SourceServerId != l.gone.id || record.ServerId != l.b.id || record.Snapshot != l.snapshot || record.TargetId != r.TargetID || record.State != "succeeded" {
		t.Fatalf("restore recorded as %+v", record)
	}
	if record.Restored == nil || record.Restored.Manifest.PolicyId != l.policy.ID || record.Restored.Manifest.ResourceId != l.volume {
		t.Fatalf("the restore's result wasn't recorded: %+v", record.Restored)
	}
}

// A restore's report must name the snapshot and target it was issued for and carry a manifest the
// policy admits; only a succeeded restore carries one.
func TestRestoreReportsAreCheckedAgainstTheirCommand(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	g.hold(l.b, "restore")
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	g.waitFor(t, "the restore to reach the new primary", func() bool { return g.received(l.b, "restore") })
	c, r := g.restoreCommand(t, l.b)
	for name, change := range map[string]func(*api.AgentReport, *backup.RecoveryResult){
		"another snapshot":  func(_ *api.AgentReport, x *backup.RecoveryResult) { x.Snapshot = snapshotID("f") },
		"another target":    func(_ *api.AgentReport, x *backup.RecoveryResult) { x.TargetID = uuid.New() },
		"another policy":    func(_ *api.AgentReport, x *backup.RecoveryResult) { x.Manifest.PolicyID = uuid.New() },
		"an unbounded role": func(_ *api.AgentReport, x *backup.RecoveryResult) { x.Manifest.Roles = nil },
		"a failed restore":  func(report *api.AgentReport, _ *backup.RecoveryResult) { report.State = "failed" },
	} {
		result := fixtureRecovery(r)
		report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
		change(&report, &result)
		report.Restore = claimMessage(t, result)
		var invalid *domain.Error
		if err := g.f.execution.Report(g.ctx, l.b.identity, report); !errors.As(err, &invalid) || invalid.Code != "invalid_input" {
			t.Fatalf("a report with %s = %v", name, err)
		}
	}
	report := api.AgentReport{Id: c.Id, Generation: c.Generation, PlanHash: c.PlanHash, Sequence: 2, State: "succeeded", Steps: []api.ExecutionStep{}}
	if err := g.f.execution.Report(g.ctx, l.b.identity, report); err == nil {
		t.Fatal("a succeeded restore was accepted without its result")
	}
	if records := g.recoveries(l); len(records) != 1 || records[0].Restored != nil {
		t.Fatalf("a refused report was recorded: %+v", records)
	}
	g.release(l.b, "restore")
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}
	if records := g.recoveries(l); len(records) != 1 || records[0].Restored == nil {
		t.Fatalf("the restore's result wasn't recorded: %+v", records)
	}
}

// What a restore is issued on must still hold when it is released: an agent that stopped
// advertising the restore kind, or a source server no longer revoked, gets nothing released.
func TestRestoreIsReleasedOnlyWhileItsConditionsHold(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	capable := func(yes bool) {
		g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) { c.RecoveryRestore = &yes })
	}
	capable(true)
	g.pause(l.b, true)
	// Once the test's own poll holds the lease, the host's last poll has ended.
	if c, err := g.pollFor(l.b); err != nil || c != nil {
		t.Fatalf("the paused host had work: %v %v", c, err)
	}
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	capable(false)
	var invalid *domain.Error
	if c, err := g.pollUntil(t, l.b); !errors.As(err, &invalid) || !slices.Contains(invalid.Fields, "server.capabilities.recoveryRestore") {
		t.Fatalf("a restore was released to an agent that can't run it: %v %v", c, err)
	}
	capable(true)
	// Renewing the lost server clears its revocation.
	revoked := func(yes bool) {
		if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE servers SET revoked=$2 WHERE id=$1`, l.gone.id, yes); err != nil {
			t.Fatal(err)
		}
	}
	revoked(false)
	if c, err := g.pollUntil(t, l.b); !errors.Is(err, domain.Conflict) {
		t.Fatalf("a restore was released while its source server was renewed: %v %v", c, err)
	}
	revoked(true)
	c, err := g.pollUntil(t, l.b)
	if err != nil || c == nil || c.Id != op.Id {
		t.Fatalf("the restore wasn't released once the agent could run it: %v %v", c, err)
	}
	var r backup.Recovery
	if json.Unmarshal(*c.Backup, &r) != nil || r.Installation.Access.SecretKey != "storage-secret" {
		t.Fatal("the restore was released without its repository access")
	}
	g.pause(l.b, false)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}
}

// Restoring data from a lost server is an owner's action that needs recent authentication.
func TestRestoringNeedsRecentAuthentication(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	later := time.Now().Add(16 * time.Minute)
	g.f.auth.Now = func() time.Time { return later }
	if got := g.recover(l, l.policy, recoveryRequest(l.snapshot)); got.status != http.StatusForbidden {
		t.Fatalf("a stale session's restore = %d %s", got.status, got.body)
	}
	if got := g.recoveries(l); len(got) != 0 {
		t.Fatal("a refused restore was recorded", got)
	}
}

// A restore needs somewhere to go: the new primary's deployment must still declare the volume.
func TestRestoreNeedsTheVolumeStillDeclared(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	spec := l.e.spec
	spec.Volumes = []domain.Volume{}
	spec.Applications = slices.DeleteFunc(slices.Clone(spec.Applications), func(a domain.Application) bool { return len(a.Mounts) > 0 })
	g.b.call("PUT", l.e.path+"/draft", map[string]any{"baseRevisionId": l.e.draft.Id, "specification": spec}, 200, &l.e.draft)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	wantRefusedField(t, g.recover(l, l.policy, recoveryRequest(l.snapshot)), "policy.resource")
}
