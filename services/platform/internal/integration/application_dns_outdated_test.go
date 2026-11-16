package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

// The addresses publishedServer approves, and the ones a moved server is approved for after it.
var (
	approvedFirst = []string{"198.51.100.20", "2001:db8::20"}
	approvedLater = []string{"198.51.100.40", "2001:db8::40"}
)

// newerZone is a management restored from a backup, over a zone the management it replaced kept
// changing after the backup: one server was moved to new addresses, and another revoked.
type newerZone struct {
	source         *fixture
	restored       *fixture
	z              *dnsZone
	moved, revoked dnsServer
}

func restoreOverNewerZone(t *testing.T) newerZone {
	t.Helper()
	ctx := context.Background()
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	moved := publishedServer(t, f, z)
	revoked := publishedServer(t, f, z)
	// A backup taken while a job holds a server's lease would keep the restored management's jobs
	// waiting for it to run out.
	dnsJobsDone(t, f)
	backup := backupDatabase(t, f)

	// Later than the backup by more than the margin, the owner moves one server and revokes the other.
	z.mu.Lock()
	z.ahead = 10 * time.Minute
	z.mu.Unlock()
	reconfigure(t, moved, approvedLater)
	eventually(t, "the moved server's new addresses are published", func() bool { return zoneShows(z, moved, approvedLater...) })
	revoked.b.call("POST", revoked.path+"/revoke", nil, 204, nil)
	eventually(t, "the revoked server's records are removed", func() bool { return z.marked(revoked.name(), revoked.marker) == 0 })
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}

	// The management that replaced it starts on the backup, with the same zone.
	restored := restoreDatabase(t, f, backup)
	restored.st.ApplicationDomain = zoneBase
	z.attach(restored)
	z.mu.Lock()
	z.writes = nil
	z.mu.Unlock()
	return newerZone{f, restored, z, moved, revoked}
}

// signedIn is the server as its owner sees it through the restored management.
func (n newerZone) signedIn(t *testing.T, s dnsServer) dnsServer {
	t.Helper()
	s.f, s.b = n.restored, n.restored.browser(t)
	s.b.login("owner@example.test", n.source.password)
	return s
}

// reconfigure approves new addresses for a server whose addresses were approved before.
func reconfigure(t *testing.T, s dnsServer, addresses []string) {
	t.Helper()
	var profile api.ServerDomain
	s.b.call("GET", s.path+"/domain", nil, 200, &profile)
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: addresses[0], Ipv6: addresses[1]}, 200, nil)
}

// zoneShows reports whether the zone's address records of ours at the server's name hold exactly
// these addresses.
func zoneShows(z *dnsZone, s dnsServer, addresses ...string) bool {
	want := map[string]bool{}
	for _, a := range addresses {
		want[a] = true
	}
	found := 0
	for _, r := range z.snapshot() {
		if r["name"] == s.name() && r["comment"] == s.marker && (r["type"] == "A" || r["type"] == "AAAA") {
			if !want[r["content"].(string)] {
				return false
			}
			found++
		}
	}
	return found == len(addresses)
}

// runDNSJobs has management reconcile every approved server now, as its fifteen-minute sweep does,
// and waits for the jobs to finish. A job still running would swallow one queued with its arguments.
func runDNSJobs(t *testing.T, f *fixture) {
	t.Helper()
	ctx := context.Background()
	dnsJobsDone(t, f)
	if _, e := f.st.Pool.Exec(ctx, "UPDATE server_domains SET observed_at=NULL"); e != nil {
		t.Fatal(e)
	}
	if e := f.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	dnsJobsDone(t, f)
}

// dnsJobsDone waits until no DNS job is queued or running, and none holds a server's lease.
func dnsJobsDone(t *testing.T, f *fixture) {
	t.Helper()
	eventually(t, "the DNS jobs finish", func() bool {
		var active int
		if e := f.st.Pool.QueryRow(context.Background(), "SELECT (SELECT count(*) FROM river_job WHERE kind='platform_dns_v1' AND state NOT IN ('completed','discarded','cancelled'))+(SELECT count(*) FROM server_dns_leases)").Scan(&active); e != nil {
			t.Fatal(e)
		}
		return active == 0
	})
}

// endedAsOutdated fails unless the server's approval ended because the zone is newer than it.
func endedAsOutdated(t *testing.T, f *fixture, s dnsServer) {
	t.Helper()
	var state, note string
	var approved *int64
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT state,note,approved_controller FROM server_domains WHERE server_id=$1", s.id).Scan(&state, &note, &approved); e != nil {
		t.Fatal(e)
	}
	if state != "review_required" || approved != nil || !strings.Contains(note, "restored from an older backup") || !strings.Contains(note, "approves them again") {
		t.Fatalf("the approval did not end as outdated: %s, %v, %q", state, approved, note)
	}
}

// outdatedRemovals counts the audited, confirmed deletions of the server's records as outdated.
func outdatedRemovals(t *testing.T, f *fixture, s dnsServer) int {
	t.Helper()
	var n int
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT count(*) FROM dns_record_removals WHERE server_id=$1 AND reason='approval_outdated' AND confirmed_at IS NOT NULL", s.id).Scan(&n); e != nil {
		t.Fatal(e)
	}
	return n
}

// expectNoWrites fails if the provider was asked to create or change any record.
func expectNoWrites(t *testing.T, z *dnsZone) {
	t.Helper()
	z.mu.Lock()
	defer z.mu.Unlock()
	if len(z.writes) != 0 {
		t.Fatal("published after the restore", z.writes)
	}
}

func TestZoneSweepEndsARestoredApprovalTheZoneHasMovedPast(t *testing.T) {
	n := restoreOverNewerZone(t)
	sweepZone(t, n.restored)
	endedAsOutdated(t, n.restored, n.moved)
	// The newer records are withdrawn too: no approval this database holds backs them.
	if n.z.marked(n.moved.name(), n.moved.marker) != 0 || outdatedRemovals(t, n.restored, n.moved) != 2 {
		t.Fatal("the moved server's newer records were not withdrawn as outdated", n.z.snapshot())
	}
	// The DNS jobs that follow publish neither server's older addresses. The revoked server left no
	// record for the sweep to see; its job finds the record it last observed gone.
	runDNSJobs(t, n.restored)
	expectNoWrites(t, n.z)
	endedAsOutdated(t, n.restored, n.revoked)
	if n.z.marked(n.revoked.name(), n.revoked.marker) != 0 || n.z.marked(n.moved.name(), n.moved.marker) != 0 {
		t.Fatal("records were published after the restore", n.z.snapshot())
	}
	// The owner sees why.
	moved := n.signedIn(t, n.moved)
	var profile api.ServerDomain
	moved.b.call("GET", moved.path+"/domain", nil, 200, &profile)
	if profile.State != "review_required" || !strings.Contains(profile.Note, "restored from an older backup") {
		t.Fatal("the owner is not told why the approval ended", profile)
	}
	// A second sweep finds nothing more to do.
	before := n.z.snapshot()
	sweepZone(t, n.restored)
	expectZone(t, n.z, before)
}

func TestOutdatedRecordsStayWhileTheirApprovalCannotBeEnded(t *testing.T) {
	n := restoreOverNewerZone(t)
	ctx := context.Background()
	// The database refuses to end the approval, as when its transaction fails.
	refuse := `CREATE FUNCTION refuse_review() RETURNS trigger LANGUAGE plpgsql AS $$BEGIN IF NEW.state='review_required' THEN RAISE EXCEPTION 'refused'; END IF; RETURN NEW; END$$;
		CREATE TRIGGER refuse_review BEFORE UPDATE ON server_domains FOR EACH ROW EXECUTE FUNCTION refuse_review()`
	if _, e := n.restored.st.Pool.Exec(ctx, refuse); e != nil {
		t.Fatal(e)
	}
	before := n.z.snapshot()
	if e := n.restored.execution.SweepZone(ctx); e == nil {
		t.Fatal("the sweep did not report the approval it failed to end")
	}
	// Deleting the newer records while the approval is current would leave its job nothing to judge
	// it by, so they stay, and nothing is audited.
	expectZone(t, n.z, before)
	var audited int
	if e := n.restored.st.Pool.QueryRow(ctx, "SELECT count(*) FROM dns_record_removals WHERE server_id=$1", n.moved.id).Scan(&audited); e != nil || audited != 0 {
		t.Fatal("deletions were audited while the approval was current", audited, e)
	}
	if _, e := n.restored.st.Pool.Exec(ctx, "DROP TRIGGER refuse_review ON server_domains"); e != nil {
		t.Fatal(e)
	}
	runDNSJobs(t, n.restored)
	expectNoWrites(t, n.z)
	endedAsOutdated(t, n.restored, n.moved)
}

func TestDNSJobRefusesARestoredApprovalBeforeTheZoneSweepRuns(t *testing.T) {
	n := restoreOverNewerZone(t)
	runDNSJobs(t, n.restored)
	expectNoWrites(t, n.z)
	endedAsOutdated(t, n.restored, n.moved)
	endedAsOutdated(t, n.restored, n.revoked)
	if !zoneShows(n.z, n.moved, approvedLater...) {
		t.Fatal("the job changed the moved server's records", n.z.snapshot())
	}
	// The sweep withdraws the newer records, as it judges every withdrawal against the zone.
	sweepZone(t, n.restored)
	if n.z.marked(n.moved.name(), n.moved.marker) != 0 || outdatedRemovals(t, n.restored, n.moved) != 2 {
		t.Fatal("the moved server's newer records were not withdrawn as outdated", n.z.snapshot())
	}
	expectNoWrites(t, n.z)
	// Approving the addresses again publishes them: the records this database deleted aren't
	// taken for evidence of a newer zone.
	moved := n.signedIn(t, n.moved)
	reconfigure(t, moved, approvedLater)
	moved.waitState(t, "applied")
	if !zoneShows(n.z, n.moved, approvedLater...) {
		t.Fatal("the re-approved addresses were not published", n.z.snapshot())
	}
}

func TestApprovalChangesAreNotMistakenForANewerZone(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	// A job that lost its lease puts the old address back after the new one was published. Within
	// the margin, that isn't evidence of a newer zone: the next job puts the approved address back.
	reconfigure(t, s, approvedLater)
	s.waitState(t, "applied")
	dnsJobsDone(t, f)
	z.mu.Lock()
	for _, r := range z.records {
		if r["comment"] == s.marker && r["type"] == "A" {
			r["content"], r["modified_on"] = approvedFirst[0], z.modifiedOn()
		}
	}
	z.mu.Unlock()
	runDNSJobs(t, f)
	s.waitState(t, "applied")
	if !zoneShows(z, s, approvedLater...) {
		t.Fatal("the approved address was not put back", z.snapshot())
	}
	// Removing an address, then adding it back, publishes it again.
	var profile api.ServerDomain
	s.b.call("GET", s.path+"/domain", nil, 200, &profile)
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: approvedLater[0]}, 200, nil)
	s.waitState(t, "applied")
	reconfigure(t, s, approvedLater)
	s.waitState(t, "applied")
	if !zoneShows(z, s, approvedLater...) {
		t.Fatal("an address added back was not published", z.snapshot())
	}
	// A record of ours deleted by hand ends the approval, rather than being put back.
	z.mu.Lock()
	for id, r := range z.records {
		if r["comment"] == s.marker && r["type"] == "AAAA" {
			delete(z.records, id)
		}
	}
	z.writes = nil
	z.mu.Unlock()
	runDNSJobs(t, f)
	expectNoWrites(t, z)
	endedAsOutdated(t, f, s)
	// Approving again starts afresh: the missing record isn't held against the new approval.
	reconfigure(t, s, approvedLater)
	s.waitState(t, "applied")
	if !zoneShows(z, s, approvedLater...) {
		t.Fatal("the re-approved addresses were not published", z.snapshot())
	}
}

func TestADeletionThatNeverTookEffectDoesNotExplainALaterLoss(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	dnsJobsDone(t, f)
	// The owner removes the IPv6 address, but every deletion fails, so the record stays published.
	z.mu.Lock()
	z.failDelete = 100
	z.mu.Unlock()
	var profile api.ServerDomain
	s.b.call("GET", s.path+"/domain", nil, 200, &profile)
	s.b.call("PUT", s.path+"/domain", api.ConfigureServerDomain{BaseGeneration: profile.Generation, Ipv4: approvedFirst[0]}, 200, nil)
	s.waitState(t, "failed")
	// Approving the address again keeps the record the deletion missed.
	reconfigure(t, s, approvedFirst)
	s.waitState(t, "applied")
	z.mu.Lock()
	z.failDelete = 0
	z.mu.Unlock()
	if _, e := f.st.Pool.Exec(ctx, "UPDATE river_job SET scheduled_at=now() WHERE kind='platform_dns_v1' AND state IN ('retryable','scheduled')"); e != nil {
		t.Fatal(e)
	}
	dnsJobsDone(t, f)
	// Losing it later, outside this database, ends the approval: the failed attempt explains nothing.
	deleteByHand(z, s, "AAAA")
	z.mu.Lock()
	z.writes = nil
	z.mu.Unlock()
	runDNSJobs(t, f)
	expectNoWrites(t, z)
	endedAsOutdated(t, f, s)
}

func TestAFailedJobKeepsTheRecordsItLastObserved(t *testing.T) {
	n := restoreOverNewerZone(t)
	ctx := context.Background()
	// The provider fails the restored management's first jobs before they see the zone.
	n.z.mu.Lock()
	n.z.failLookups = true
	n.z.mu.Unlock()
	if _, e := n.restored.st.Pool.Exec(ctx, "UPDATE server_domains SET observed_at=NULL"); e != nil {
		t.Fatal(e)
	}
	if e := n.restored.execution.SweepDomains(ctx); e != nil {
		t.Fatal(e)
	}
	eventually(t, "the revoked server's job fails", func() bool {
		var state string
		if e := n.restored.st.Pool.QueryRow(ctx, "SELECT state FROM server_domains WHERE server_id=$1", n.revoked.id).Scan(&state); e != nil {
			t.Fatal(e)
		}
		return state == "failed"
	})
	n.z.mu.Lock()
	n.z.failLookups = false
	n.z.mu.Unlock()
	// The next job still finds the record the last one before the restore observed gone.
	runDNSJobs(t, n.restored)
	expectNoWrites(t, n.z)
	endedAsOutdated(t, n.restored, n.revoked)
}

func TestRecordsObservedBeforeTheZoneWasRecordedStillCount(t *testing.T) {
	n := restoreOverNewerZone(t)
	// As in a backup taken before the zone was recorded with the records it names.
	if _, e := n.restored.st.Pool.Exec(context.Background(), "UPDATE server_domains SET observed_zone=''"); e != nil {
		t.Fatal(e)
	}
	runDNSJobs(t, n.restored)
	expectNoWrites(t, n.z)
	endedAsOutdated(t, n.restored, n.revoked)
}

func TestRecordsObservedUnderAnotherBaseAreNotHeldAgainstANewApproval(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = "apps.example.test"
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	dnsJobsDone(t, f)
	// The server's records under the old base are gone by the time the base changes, so their
	// withdrawal audits nothing.
	z.mu.Lock()
	for id, r := range z.records {
		if r["comment"] == s.marker {
			delete(z.records, id)
		}
	}
	z.mu.Unlock()
	if e := f.execution.Stop(ctx); e != nil {
		t.Fatal(e)
	}
	f.st.ApplicationDomain = "apps2.example.test"
	if e := f.execution.Start(ctx); e != nil {
		t.Fatal(e)
	}
	// An owner approves the server's addresses under the new base, which publishes them there.
	reconfigure(t, s, approvedLater)
	dnsJobsDone(t, f)
	var state string
	if e := f.st.Pool.QueryRow(ctx, "SELECT state FROM server_domains WHERE server_id=$1", s.id).Scan(&state); e != nil || state != "applied" {
		t.Fatal("the approval under the new base was not applied", state, e)
	}
	if z.marked(s.nameUnder("apps2.example.test"), s.marker) != 2 {
		t.Fatal("the approval under the new base was not published", z.snapshot())
	}
}

// lapsingServer is a published server in a hosted workspace, and a restart that moves the end of
// its management to until from now.
func lapsingServer(t *testing.T) (*fixture, *dnsZone, dnsServer, func(until string)) {
	t.Helper()
	f := setup(t)
	ctx := context.Background()
	f.st.ApplicationDomain = zoneBase
	var ws uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "SELECT id FROM workspaces LIMIT 1").Scan(&ws); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, "INSERT INTO hosted_subscriptions(workspace_id,status,management_until,server_limit) VALUES($1,'granted',now()+interval '1 hour',100)", ws); e != nil {
		t.Fatal(e)
	}
	z := newDNSZone(t, f)
	s := publishedServer(t, f, z)
	dnsJobsDone(t, f)
	restart := func(until string) {
		t.Helper()
		if e := f.execution.Stop(ctx); e != nil {
			t.Fatal(e)
		}
		f.st.Hosted = true
		if _, e := f.st.Pool.Exec(ctx, "UPDATE hosted_subscriptions SET management_until=now()+$2::interval WHERE workspace_id=$1", ws, until); e != nil {
			t.Fatal(e)
		}
		if e := f.execution.Start(ctx); e != nil {
			t.Fatal(e)
		}
		if e := f.execution.SweepDomains(ctx); e != nil {
			t.Fatal(e)
		}
	}
	return f, z, s, restart
}

// deleteByHand deletes the server's record of the type from the zone, as someone outside Astilba would.
func deleteByHand(z *dnsZone, s dnsServer, kind string) {
	z.mu.Lock()
	defer z.mu.Unlock()
	for id, r := range z.records {
		if r["comment"] == s.marker && r["type"] == kind {
			delete(z.records, id)
		}
	}
}

func TestInactiveManagementJudgesRecordsBeforeRemovingThem(t *testing.T) {
	f, z, s, restart := lapsingServer(t)
	// A record of the server's is deleted outside this database, then its management lapses. The
	// removal must not explain the deleted record away, or resuming would publish the approval again.
	deleteByHand(z, s, "AAAA")
	restart("-1 second")
	eventually(t, "the lapsed workspace's records are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	endedAsOutdated(t, f, s)
	z.mu.Lock()
	z.writes = nil
	z.mu.Unlock()
	restart("1 hour")
	runDNSJobs(t, f)
	expectNoWrites(t, z)
	endedAsOutdated(t, f, s)
}

func TestAnEndedApprovalKeepsItsNoteWhenManagementLapses(t *testing.T) {
	f, z, s, restart := lapsingServer(t)
	// The job ends the approval first; the record still holding the approved address stays.
	deleteByHand(z, s, "AAAA")
	runDNSJobs(t, f)
	endedAsOutdated(t, f, s)
	restart("-1 second")
	eventually(t, "the lapsed workspace's records are removed", func() bool { return z.marked(s.name(), s.marker) == 0 && s.publications(t) == 0 })
	// The note still says why the approval ended, not that resuming publishes it again.
	endedAsOutdated(t, f, s)
}

func heldOutdated(t *testing.T) (newerZone, zoneHold) {
	t.Helper()
	n := restoreOverNewerZone(t)
	// With the moved server's two records, these make more than ten, and all of the zone's.
	installation := installationOf(t, n.restored)
	for range 9 {
		server := uuid.New()
		n.z.add("A", zoneName(zoneBase, server), "203.0.113.1", zoneMarker(installation, server))
	}
	before := n.z.snapshot()
	sweepZone(t, n.restored)
	expectZone(t, n.z, before)
	// The approval ends all the same: that changes only the database.
	endedAsOutdated(t, n.restored, n.moved)
	holds := zoneHolds(t, n.restored)
	if len(holds) != 1 || holds[0].records != 11 {
		t.Fatalf("holds %+v", holds)
	}
	return n, holds[0]
}

func TestOutdatedWithdrawalsCountTowardsTheHold(t *testing.T) {
	n, hold := heldOutdated(t)
	ctx := context.Background()
	holds := []zoneHold{hold}
	if e := execution.ConfirmZoneHold(ctx, n.restored.st.Pool, holds[0].id, 11, "operator", "restored from a backup"); e != nil {
		t.Fatal(e)
	}
	sweepZone(t, n.restored)
	if n.z.marked(n.moved.name(), n.moved.marker) != 0 || outdatedRemovals(t, n.restored, n.moved) != 2 {
		t.Fatal("the confirmed outdated records were not withdrawn", n.z.snapshot())
	}
	var held int
	if e := n.restored.st.Pool.QueryRow(ctx, "SELECT count(*) FROM dns_record_removals WHERE server_id=$1 AND hold_id=$2", n.moved.id, holds[0].id).Scan(&held); e != nil || held != 2 {
		t.Fatal("the outdated withdrawals were not made under the confirmed hold", held, e)
	}
}

func TestARecordApprovedAgainWhileHeldIsNotWithdrawn(t *testing.T) {
	n, hold := heldOutdated(t)
	// The owner approves the server's current addresses before the operator confirms the hold.
	moved := n.signedIn(t, n.moved)
	reconfigure(t, moved, approvedLater)
	moved.waitState(t, "applied")
	if e := execution.ConfirmZoneHold(context.Background(), n.restored.st.Pool, hold.id, 11, "operator", "restored from a backup"); e != nil {
		t.Fatal(e)
	}
	sweepZone(t, n.restored)
	if !zoneShows(n.z, n.moved, approvedLater...) || outdatedRemovals(t, n.restored, n.moved) != 0 {
		t.Fatal("records the owner approved again were withdrawn", n.z.snapshot())
	}
}
