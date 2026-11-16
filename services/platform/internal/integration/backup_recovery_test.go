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
	return g.lostFilesAcross(t)
}

// lostFilesAcross is lostFiles with the web application placed on each of across.
func (g *revokedGroup) lostFilesAcross(t *testing.T, across ...uuid.UUID) lostFiles {
	t.Helper()
	l := lostFiles{gone: g.hosts[0], b: g.hosts[1], snapshot: snapshotID("e")}
	l.e, l.volume = g.filesStack(t, l.gone)
	if len(across) > 0 {
		l.e.spec.Applications[0].Replicas = len(across)
		g.applied(t, &l.e, g.place(t, &l.e, l.gone.id, across...))
	}
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

// What a restore is issued on must still hold when it is released. One that can't go ahead never
// fails or skips the poll, which would hold or reorder the server's other commands: if no agent has
// received it, it ends in management, and an agent that may hold it is told to stop, without the
// repository access.
func TestRestoreIsReleasedOnlyWhileItsConditionsHold(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	capable := func(yes *bool) {
		g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) { c.RecoveryRestore = yes })
	}
	yes, no := true, false
	// Renewing the lost server clears its revocation.
	revoked := func(yes bool) {
		if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE servers SET revoked=$2 WHERE id=$1`, l.gone.id, yes); err != nil {
			t.Fatal(err)
		}
	}
	capable(&yes)
	g.pause(l.b, true)
	// Once the test's own poll holds the lease, the host's last poll has ended.
	if c, err := g.pollFor(l.b); err != nil || c != nil {
		t.Fatalf("the paused host had work: %v %v", c, err)
	}
	start := func() uuid.UUID {
		var op api.Operation
		g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
		return op.Id
	}
	ended := func(id uuid.UUID, want, why string) {
		t.Helper()
		var state, note string
		g.waitFor(t, "the restore to end", func() bool {
			if c, err := g.pollFor(l.b); err != nil || c != nil {
				t.Fatalf("a restore that can't go ahead was sent or failed the poll: %v %v", c, err)
			}
			state, note = g.operationState(t, id)
			return state == "succeeded" || state == "failed" || state == "cancelled"
		})
		if state != want || !strings.Contains(note, why) {
			t.Fatalf("restore ended %s: %s", state, note)
		}
	}

	op := start()
	revoked(false)
	ended(op, "failed", "renewed")
	revoked(true)

	op = start()
	g.b.call("POST", g.base+"/operations/"+op.String()+"/cancel", nil, 204, nil)
	ended(op, "cancelled", "before any agent received it")

	expire := func(op uuid.UUID) {
		t.Helper()
		if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{deadline}',to_jsonb('2001-01-01T00:00:00Z'::text)) WHERE id=$1`, op); err != nil {
			t.Fatal(err)
		}
	}
	op = start()
	expire(op)
	ended(op, "cancelled", "before any agent received it")

	// An agent that no longer runs the restore kind, such as a downgraded one, would hold it
	// unsettled.
	op = start()
	capable(nil)
	ended(op, "failed", "no longer runs restores")

	// One that runs it gets it even while its restore prerequisites fail, and settles it itself.
	capable(&yes)
	op = start()
	capable(&no)
	c, err := g.pollUntil(t, l.b)
	var r backup.Recovery
	if err != nil || c == nil || c.Id != op || c.CancelRequested || json.Unmarshal(*c.Backup, &r) != nil || r.Installation.Access.SecretKey != "storage-secret" {
		t.Fatalf("the restore wasn't released with its repository access: %v %v", c, err)
	}
	// Once an agent may hold it, a lost capability or a passed deadline is the agent's to settle:
	// it goes on, without the access sent again.
	capable(nil)
	expire(op)
	c, err = g.pollUntil(t, l.b)
	if err != nil || c == nil || c.Id != op || c.CancelRequested || json.Unmarshal(*c.Backup, &r) != nil || r.Installation.Access != (backup.Access{}) {
		t.Fatalf("a held restore past its deadline wasn't left to its agent: %v %v", c, err)
	}
	// A renewed source server stops it, without the access.
	revoked(false)
	c, err = g.pollUntil(t, l.b)
	if err != nil || c == nil || c.Id != op || !c.CancelRequested || json.Unmarshal(*c.Backup, &r) != nil || r.Installation.Access != (backup.Access{}) {
		t.Fatalf("a restore an agent may hold wasn't stopped without its access: %v %v", c, err)
	}
	if state, note := g.operationState(t, op); state != "dispatched" || !strings.Contains(note, "renewed") {
		t.Fatalf("the stopped restore is %s: %s", state, note)
	}
}

// An agent that says it lost a restore it was sent gets its access again before the deadline, and is
// told to stop after it, since it could no longer start it.
func TestRestoreAnAgentLostIsStoppedAfterItsDeadline(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	g.pause(l.b, true)
	if c, err := g.pollFor(l.b); err != nil || c != nil {
		t.Fatalf("the paused host had work: %v %v", c, err)
	}
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	released := func() (api.ExecutionCommand, backup.Recovery) {
		t.Helper()
		c, err := g.pollUntil(t, l.b)
		var r backup.Recovery
		if err != nil || c == nil || c.Id != op.Id || c.Backup == nil || json.Unmarshal(*c.Backup, &r) != nil {
			t.Fatalf("the restore wasn't sent: %v %v", c, err)
		}
		return *c, r
	}
	lost := func(c api.ExecutionCommand) {
		t.Helper()
		notice := api.SecretReleaseNotice{Acknowledged: []api.SecretReleaseRef{}, Redeliver: []api.SecretRedelivery{{OperationId: c.Id, Epoch: c.SecretRelease.Epoch, Reason: api.JournalMissing}}}
		if err := g.f.execution.NoticeSecretReleases(g.ctx, l.b.identity, notice); err != nil {
			t.Fatal(err)
		}
	}
	c, r := released()
	lost(c)
	if c, r = released(); c.CancelRequested || r.Installation.Access.SecretKey != "storage-secret" {
		t.Fatal("a restore its agent lost before the deadline wasn't sent with its access again")
	}
	if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{deadline}',to_jsonb('2001-01-01T00:00:00Z'::text)) WHERE id=$1`, op.Id); err != nil {
		t.Fatal(err)
	}
	lost(c)
	if c, r = released(); !c.CancelRequested || r.Installation.Access != (backup.Access{}) {
		t.Fatal("a restore its agent lost after the deadline wasn't stopped without its access")
	}
	if state, note := g.operationState(t, op.Id); state != "dispatched" || !strings.Contains(note, "lost this restore") {
		t.Fatalf("the lost restore is %s: %s", state, note)
	}
}

// A restore onto an environment that spans servers runs on its primary unit's server and records
// the plan the environment applied.
func TestRestoringOntoAnEnvironmentAcrossServers(t *testing.T) {
	g := newRevokedGroup(t, 3)
	other := g.hosts[2]
	l := g.lostFilesAcross(t, g.hosts[1].id, other.id)
	g.revoke(t, l.gone)
	g.networkReady(t)
	// Moving the ingress to the new primary is saved in the draft, though only a replacement plans it.
	wantRefusedField(t, g.place(t, &l.e, l.b.id, l.b.id, other.id), "replacement")
	status := g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	if status.Plan.Distributed == nil || status.Plan.PrimaryPlan().ServerID != l.b.id {
		t.Fatal("the environment doesn't span servers with its primary on the new server")
	}
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}
	records := g.recoveries(l)
	if len(records) != 1 || records[0].ServerId != l.b.id || records[0].PlanId != status.Plan.VisualPlanID() || records[0].Restored == nil {
		t.Fatalf("the restore across servers wasn't recorded against the applied plan: %+v", records)
	}
	if g.received(other, "restore") {
		t.Fatal("a restore reached the environment's other server")
	}
	// Its volume can't be switched in until a distributed cutover records its own boundary.
	draft := g.draft(t, l)
	wantRefusedField(t, g.cutover(l, records[0].Id, draft.Id), "deployment.distributed")
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

// restoreCapable revokes l's lost server, moves the environment onto its new primary and has that
// server's agent advertise the restore kind.
func (g *revokedGroup) restoreCapable(t *testing.T, l *lostFiles) {
	t.Helper()
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	g.reports(t, l.b, func(c *api.AgentCapabilities, _ *api.HostPreflight) {
		yes := true
		c.RecoveryRestore = &yes
	})
}

// restored starts a restore of l's snapshot and returns its record once it has ended as want.
func (g *revokedGroup) restored(t *testing.T, l lostFiles, want api.ExecutionState) api.BackupRecoveryRecord {
	t.Helper()
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	if state := g.finished(t, op.Id); state != string(want) {
		t.Fatalf("restore ended %s", state)
	}
	for _, r := range g.recoveries(l) {
		if r.OperationId == op.Id {
			return r
		}
	}
	t.Fatal("the restore wasn't recorded")
	return api.BackupRecoveryRecord{}
}

func recoveryPath(l lostFiles, id uuid.UUID) string {
	return l.e.path + "/backup-policies/" + l.policy.ID.String() + "/recoveries/" + id.String()
}

func (g *revokedGroup) cutover(l lostFiles, id, base uuid.UUID) reply {
	return <-g.b.send("POST", recoveryPath(l, id)+"/cutover", api.BackupRecoveryCutoverRequest{BaseRevisionId: base})
}

func (g *revokedGroup) discard(l lostFiles, id uuid.UUID) reply {
	return <-g.b.send("DELETE", recoveryPath(l, id)+"/target", nil)
}

type draftRevision struct {
	Id            uuid.UUID            `json:"id"`
	Specification domain.Specification `json:"specification"`
}

func (g *revokedGroup) draft(t *testing.T, l lostFiles) draftRevision {
	t.Helper()
	var out draftRevision
	g.b.call("GET", l.e.path+"/draft", nil, 200, &out)
	return out
}

// mounts reports whether spec declares volume and its files service mounts it.
func mounts(spec domain.Specification, volume uuid.UUID) bool {
	return slices.ContainsFunc(spec.Volumes, func(v domain.Volume) bool { return v.ID == volume }) && slices.ContainsFunc(spec.Applications, func(a domain.Application) bool {
		return slices.ContainsFunc(a.Mounts, func(m domain.VolumeMount) bool { return m.VolumeID == volume })
	})
}

// A succeeded restore's volume is switched in through a draft, deployed like any other. The draft
// is written only from the restore's recorded result, checked again, for the plan it ran against.
func TestCuttingOverToARestoredVolume(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.restoreCapable(t, &l)
	record := g.restored(t, l, "succeeded")
	draft := g.draft(t, l)
	if got := g.cutover(l, record.Id, uuid.New()); got.status != http.StatusConflict {
		t.Fatalf("a cutover from another draft = %d %s", got.status, got.body)
	}
	if got := g.cutover(l, uuid.New(), draft.Id); got.status != http.StatusNotFound {
		t.Fatalf("a cutover from an unknown restore = %d %s", got.status, got.body)
	}
	// A result altered since it was recorded no longer matches its restore.
	var result []byte
	var target uuid.UUID
	if err := g.f.st.Pool.QueryRow(g.ctx, `SELECT result, target_volume_id FROM backup_recoveries WHERE id = $1`, record.Id).Scan(&result, &target); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]string{
		"another snapshot":          `UPDATE backup_recoveries SET result = jsonb_set(result, '{snapshot}', to_jsonb(repeat('f', 64))) WHERE id = $1`,
		"another policy's manifest": `UPDATE backup_recoveries SET result = jsonb_set(result, '{manifest,policyId}', to_jsonb(gen_random_uuid()::text)) WHERE id = $1`,
		"an unbounded manifest":     `UPDATE backup_recoveries SET result = jsonb_set(result, '{manifest,extensions}', '["adminpack"]') WHERE id = $1`,
		"another target":            `UPDATE backup_recoveries SET target_volume_id = gen_random_uuid() WHERE id = $1`,
	} {
		if _, err := g.f.st.Pool.Exec(g.ctx, change, record.Id); err != nil {
			t.Fatal(err)
		}
		got := g.cutover(l, record.Id, draft.Id)
		if _, err := g.f.st.Pool.Exec(g.ctx, `UPDATE backup_recoveries SET result = $2, target_volume_id = $3 WHERE id = $1`, record.Id, result, target); err != nil {
			t.Fatal(err)
		}
		if got.status != http.StatusConflict {
			t.Fatalf("a cutover from a result with %s = %d %s", name, got.status, got.body)
		}
	}

	got := g.cutover(l, record.Id, draft.Id)
	var revision draftRevision
	if got.err != nil || got.status != http.StatusOK || json.Unmarshal(got.body, &revision) != nil {
		t.Fatalf("cutover = %d %s %v", got.status, got.body, got.err)
	}
	if !mounts(revision.Specification, record.TargetId) || mounts(revision.Specification, l.volume) {
		t.Fatalf("the draft doesn't switch the restored volume in: %+v", revision.Specification)
	}
	g.b.call("GET", l.e.path+"/draft", nil, 200, &l.e.draft)
	status := g.applied(t, &l.e, <-g.b.send("POST", l.e.path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: revision.Id, ServerId: l.b.id}))
	if !mounts(status.Plan.Specification, record.TargetId) {
		t.Fatal("the deployed plan doesn't mount the restored volume")
	}
	// The deployment no longer runs the plan the restore ran against.
	if got := g.cutover(l, record.Id, revision.Id); got.status != http.StatusConflict {
		t.Fatalf("a second cutover = %d %s", got.status, got.body)
	}
	// The volume the server runs is kept, even once the draft no longer mounts it.
	g.b.call("GET", l.e.path+"/draft", nil, 200, &revision)
	g.b.call("PUT", l.e.path+"/draft", map[string]any{"baseRevisionId": revision.Id, "specification": draft.Specification}, 200, &revision)
	if got := g.discard(l, record.Id); got.status != http.StatusConflict {
		t.Fatalf("a discard of the volume the server runs = %d %s", got.status, got.body)
	}
}

// An ended restore's volume can be deleted, unless the draft mounts it, and once deleted it can't
// be switched in.
func TestDiscardingARestoredVolume(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.restoreCapable(t, &l)
	discarded := func(id uuid.UUID) {
		t.Helper()
		got := g.discard(l, id)
		var op api.Operation
		if got.err != nil || got.status != http.StatusCreated || json.Unmarshal(got.body, &op) != nil {
			t.Fatalf("discard = %d %s %v", got.status, got.body, got.err)
		}
		if state := g.finished(t, op.Id); state != "succeeded" {
			t.Fatalf("discard ended %s", state)
		}
		for _, r := range g.recoveries(l) {
			if r.Id == id && !r.TargetDeleted {
				t.Fatal("the discarded volume isn't recorded as deleted")
			}
		}
	}

	g.mu.Lock()
	g.failing["restore/"+l.b.id.String()] = true
	g.mu.Unlock()
	failed := g.restored(t, l, "failed")
	if failed.TargetDeleted {
		t.Fatal("a restore's volume was recorded as deleted before it was discarded")
	}
	discarded(failed.Id)
	if got := g.discard(l, failed.Id); got.status != http.StatusConflict {
		t.Fatalf("a second discard = %d %s", got.status, got.body)
	}
	g.mu.Lock()
	delete(g.failing, "restore/"+l.b.id.String())
	g.mu.Unlock()

	// A restore still running keeps its volume.
	g.hold(l.b, "restore")
	var op api.Operation
	g.b.call("POST", l.e.path+"/backup-policies/"+l.policy.ID.String()+"/recoveries", recoveryRequest(l.snapshot), 202, &op)
	g.waitFor(t, "the restore to reach the new primary", func() bool { return g.received(l.b, "restore") })
	var running api.BackupRecoveryRecord
	for _, r := range g.recoveries(l) {
		if r.OperationId == op.Id {
			running = r
		}
	}
	if got := g.discard(l, running.Id); got.status != http.StatusConflict {
		t.Fatalf("a running restore's discard = %d %s", got.status, got.body)
	}
	g.release(l.b, "restore")
	if state := g.finished(t, op.Id); state != "succeeded" {
		t.Fatalf("restore ended %s", state)
	}

	// A draft that mounts the volume keeps it.
	before := g.draft(t, l)
	var revision draftRevision
	got := g.cutover(l, running.Id, before.Id)
	if got.status != http.StatusOK || json.Unmarshal(got.body, &revision) != nil {
		t.Fatalf("cutover = %d %s", got.status, got.body)
	}
	if got := g.discard(l, running.Id); got.status != http.StatusConflict {
		t.Fatalf("a discard of a volume the draft mounts = %d %s", got.status, got.body)
	}
	g.b.call("PUT", l.e.path+"/draft", map[string]any{"baseRevisionId": revision.Id, "specification": before.Specification}, 200, &revision)
	discarded(running.Id)
	if got := g.cutover(l, running.Id, revision.Id); got.status != http.StatusConflict {
		t.Fatalf("a cutover to a discarded volume = %d %s", got.status, got.body)
	}
}

// destinations lists g's backup destinations by ID.
func (g *revokedGroup) destinations() map[uuid.UUID]api.BackupDestination {
	var list []api.BackupDestination
	g.b.call("GET", g.base+"/backup-destinations", nil, 200, &list)
	out := map[uuid.UUID]api.BackupDestination{}
	for _, d := range list {
		out[d.Id] = d
	}
	return out
}

// exposureAlerts sweeps g's alerts and returns the state of each backup_exposure alert by
// destination, the newest alert's for each.
func (g *revokedGroup) exposureAlerts(t *testing.T) map[uuid.UUID]api.AlertState {
	t.Helper()
	alertSweep(t, g.f, g.ws)
	var inbox api.AlertInbox
	g.b.call("GET", g.base+"/alerts?view=all", nil, 200, &inbox)
	out := map[uuid.UUID]api.AlertState{}
	for _, a := range inbox.Alerts {
		if _, seen := out[a.ResourceId]; !seen && a.Kind == api.BackupExposure {
			out[a.ResourceId] = a.State
		}
	}
	return out
}

// An S3 destination is exposed while a policy using it is bound to a revoked server, which was
// given its storage keys. A new policy on it needs an acknowledgement, which is recorded, and one
// alert names the destination. A filesystem destination has no keys, and its repository was on the
// revoked server's own disk.
func TestAnExposedDestinationNeedsAnAcknowledgement(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	var unused api.BackupDestination
	g.b.call("POST", g.base+"/backup-destinations", map[string]any{"name": "Unused", "kind": "s3", "root": "", "endpoint": "https://storage.example.test", "bucket": "unused-fixture", "region": "test-1", "access": map[string]string{"accessKey": "unused-key", "secretKey": "unused-secret", "password": ""}}, 201, &unused)
	before := g.destinations()
	if unused.Exposed || len(unused.Policies) != 0 || before[l.policy.DestinationID].Exposed || before[l.local.DestinationID].Exposed {
		t.Fatal("a destination was exposed before its server was revoked")
	}
	if got := before[l.policy.DestinationID].Policies; len(got) != 1 || got[0].Id != l.policy.ID || got[0].Name != "Files" || got[0].EnvironmentId != l.policy.EnvironmentID {
		t.Fatalf("the destination's policies = %+v", got)
	}
	if alerts := g.exposureAlerts(t); len(alerts) != 0 {
		t.Fatalf("exposure alerts before a revocation = %v", alerts)
	}
	g.revoke(t, l.gone)
	g.applied(t, &l.e, g.replace(&l.e, l.b.id, l.volume))
	now := g.destinations()
	if !now[l.policy.DestinationID].Exposed || now[l.local.DestinationID].Exposed || now[unused.Id].Exposed {
		t.Fatalf("exposed destinations = %+v", now)
	}
	if alerts := g.exposureAlerts(t); len(alerts) != 1 || alerts[l.policy.DestinationID] != api.AlertFiring {
		t.Fatalf("exposure alerts = %v", alerts)
	}

	password := "replacement-repository-password"
	request := api.BackupPolicyRequest{Name: "Files again", ResourceId: l.volume, DestinationId: l.policy.DestinationID, Method: "quiesced-files", Schedule: api.DailySchedule{Hour: 3, Minute: 0, Timezone: "UTC"}, Retain: 7, Enabled: true, Command: []string{}, Password: &password}
	wantRefusedField(t, <-g.b.send("POST", l.e.path+"/backup-policies", request), "destination.exposed")
	no := false
	request.ExposureAcknowledged = &no
	wantRefusedField(t, <-g.b.send("POST", l.e.path+"/backup-policies", request), "destination.exposed")
	yes := true
	request.ExposureAcknowledged = &yes
	var created backup.Policy
	g.b.call("POST", l.e.path+"/backup-policies", request, 201, &created)
	var audited int
	if err := g.f.st.Pool.QueryRow(g.ctx, "SELECT count(*) FROM audit_events WHERE workspace_id=$1 AND action='backup.destination.exposure.acknowledge' AND resource_id=$2", g.ws, l.policy.DestinationID).Scan(&audited); err != nil || audited != 1 {
		t.Fatalf("the acknowledgement was audited %d times: %v", audited, err)
	}
	// Only choosing the destination is acknowledged: an update keeps it.
	update := request
	update.ExposureAcknowledged, update.BaseGeneration, update.Retain = nil, created.Generation, 8
	g.b.call("PUT", l.e.path+"/backup-policies/"+created.ID.String(), update, 200, &created)
	if got := g.destinations()[l.policy.DestinationID].Policies; len(got) != 2 {
		t.Fatalf("the exposed destination's policies = %+v", got)
	}
	// A destination nothing revoked used needs none, a filesystem one neither, and a new policy
	// on a server in use exposes neither.
	request.Name, request.DestinationId, request.ExposureAcknowledged = "Files elsewhere", unused.Id, nil
	g.b.call("POST", l.e.path+"/backup-policies", request, 201, &created)
	request.Name, request.DestinationId = "Local files again", l.local.DestinationID
	g.b.call("POST", l.e.path+"/backup-policies", request, 201, &created)
	if now := g.destinations(); now[unused.Id].Exposed || now[l.local.DestinationID].Exposed {
		t.Fatal("a policy on a server in use exposed its destination")
	}
}

// A restore onto a new primary gives that server the destination's keys too, so the destination
// is exposed while either server is revoked, and its alert resolves only once neither is.
func TestARestoresServerKeepsItsDestinationExposed(t *testing.T) {
	g := newRevokedGroup(t, 2)
	l := g.lostFiles(t)
	g.restoreCapable(t, &l)
	if got := g.recover(l, l.policy, recoveryRequest(l.snapshot)); got.err != nil || got.status != http.StatusAccepted {
		t.Fatalf("restore = %d %s %v", got.status, got.body, got.err)
	}
	if alerts := g.exposureAlerts(t); alerts[l.policy.DestinationID] != api.AlertFiring {
		t.Fatalf("exposure alerts = %v", alerts)
	}
	// Renewing the lost server trusts it again, and nothing revoked holds the keys.
	g.b.call("POST", g.base+"/servers/"+l.gone.id.String()+"/enrollment", nil, 201, nil)
	if g.destinations()[l.policy.DestinationID].Exposed {
		t.Fatal("a destination no revoked server holds the keys of is exposed")
	}
	if alerts := g.exposureAlerts(t); alerts[l.policy.DestinationID] != api.AlertResolved {
		t.Fatalf("exposure alerts once nothing revoked holds the keys = %v", alerts)
	}
	g.revoke(t, l.b)
	if !g.destinations()[l.policy.DestinationID].Exposed {
		t.Fatal("the restore's revoked server holds the keys, but the destination isn't exposed")
	}
	if alerts := g.exposureAlerts(t); alerts[l.policy.DestinationID] != api.AlertFiring {
		t.Fatalf("exposure alerts once the restore's server is revoked = %v", alerts)
	}
}
