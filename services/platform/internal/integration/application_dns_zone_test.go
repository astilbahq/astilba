package integration

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
)

const zoneBase = "apps.example.test"

func zoneName(base string, server uuid.UUID) string { return "*." + domain.ServerDomain(base, server) }

func installationOf(t *testing.T, f *fixture) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if e := f.st.Pool.QueryRow(context.Background(), "SELECT id FROM installation").Scan(&id); e != nil {
		t.Fatal(e)
	}
	return id
}

func zoneMarker(installation, server uuid.UUID) string {
	return "astilba:" + installation.String() + ":" + server.String()
}

// enrolledServer enrolls a deployment server in the workspace without approving addresses for it.
func enrolledServer(t *testing.T, f *fixture, b *browser, ws uuid.UUID) uuid.UUID {
	t.Helper()
	enrolled, input, _ := enrollment(t, f, b, ws, "deployment")
	if _, e := f.execution.Enroll(context.Background(), acknowledging(input), "test"); e != nil {
		t.Fatal(e)
	}
	return enrolled.Server.Id
}

func sweepZone(t *testing.T, f *fixture) {
	t.Helper()
	if e := f.execution.SweepZone(context.Background()); e != nil {
		t.Fatal(e)
	}
}

// unbackedRemovals returns each audited unbacked deletion's workspace, by record, all confirmed.
func unbackedRemovals(t *testing.T, f *fixture) map[string]*uuid.UUID {
	t.Helper()
	rows, e := f.st.Pool.Query(context.Background(), "SELECT record_id,workspace_id,confirmed_at IS NOT NULL FROM dns_record_removals WHERE reason='unbacked'")
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	out := map[string]*uuid.UUID{}
	for rows.Next() {
		var id string
		var ws *uuid.UUID
		var confirmed bool
		if e = rows.Scan(&id, &ws, &confirmed); e != nil {
			t.Fatal(e)
		}
		if !confirmed {
			t.Fatal("an unbacked deletion was not confirmed", id)
		}
		out[id] = ws
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	return out
}

// expectZone fails unless exactly the records in gone have left the zone since before.
func expectZone(t *testing.T, z *dnsZone, before map[string]map[string]any, gone ...string) {
	t.Helper()
	after := z.snapshot()
	removed := map[string]bool{}
	for _, id := range gone {
		removed[id] = true
	}
	for id, r := range before {
		if _, left := after[id]; left == removed[id] {
			t.Errorf("record %s %v: removed %v, expected %v", id, r, !left, removed[id])
		}
	}
}

func TestZoneSweepWithdrawsOnlyRecordsNothingBacks(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	ctx := context.Background()
	approved := publishedServer(t, f, z)
	// A server whose approval is current, but whose publication the database lost, and one of whose
	// records the zone lost before the database observed it.
	lost := publishedServer(t, f, z)
	if _, e := f.st.Pool.Exec(ctx, "DELETE FROM dns_publications WHERE server_id=$1", lost.id); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, "UPDATE server_domains SET records=records-'AAAA' WHERE server_id=$1", lost.id); e != nil {
		t.Fatal(e)
	}
	z.mu.Lock()
	for id, r := range z.records {
		if r["comment"] == lost.marker && r["type"] == "AAAA" {
			delete(z.records, id)
		}
	}
	z.mu.Unlock()
	installation := installationOf(t, f)
	unknown := uuid.New()
	pending := enrolledServer(t, f, approved.b, approved.ws)
	unknownRecord := z.add("A", zoneName(zoneBase, unknown), "203.0.113.1", zoneMarker(installation, unknown))
	pendingRecord := z.add("AAAA", zoneName(zoneBase, pending), "2001:db8::5", zoneMarker(installation, pending))
	oldBase := z.add("A", zoneName("old.example.test", approved.id), "203.0.113.2", approved.marker)
	// Never touched: another installation's, unmarked, a type Astilba never makes, a name that isn't
	// the marker's server's, and a name that isn't a server's.
	z.add("A", zoneName(zoneBase, unknown), "203.0.113.3", zoneMarker(uuid.New(), unknown))
	z.add("A", zoneName(zoneBase, unknown), "203.0.113.4", "someone else's record")
	z.add("TXT", zoneName(zoneBase, unknown), "verification", zoneMarker(installation, unknown))
	z.add("A", zoneName(zoneBase, pending), "203.0.113.5", zoneMarker(installation, unknown))
	z.add("A", "www."+zoneBase, "203.0.113.6", zoneMarker(installation, unknown))
	// The provider matches our marker ignoring case, so it lists one that matches only so; Astilba
	// never writes it, and it neither stops the sweep nor is removed.
	z.add("A", zoneName(zoneBase, unknown), "203.0.113.7", strings.ToUpper(zoneMarker(installation, unknown)))
	before := z.snapshot()
	// A job still finishing would stand in for the one the sweep queues.
	dnsJobs := func() (all, active int) {
		if e := f.st.Pool.QueryRow(ctx, "SELECT count(*),count(*) FILTER (WHERE state NOT IN ('completed','discarded','cancelled')) FROM river_job WHERE kind='platform_dns_v1'").Scan(&all, &active); e != nil {
			t.Fatal(e)
		}
		return all, active
	}
	eventually(t, "the publishing jobs finish", func() bool { _, active := dnsJobs(); return active == 0 })
	queued, _ := dnsJobs()

	sweepZone(t, f)
	expectZone(t, z, before, unknownRecord, pendingRecord, oldBase)
	removals := unbackedRemovals(t, f)
	if len(removals) != 3 || removals[unknownRecord] != nil || *removals[pendingRecord] != approved.ws || *removals[oldBase] != approved.ws {
		t.Fatalf("audited %v", removals)
	}
	if lost.publications(t) != 1 {
		t.Fatal("the approved name the database lost was not recorded again")
	}
	var leases, holds int
	if e := f.st.Pool.QueryRow(ctx, "SELECT (SELECT count(*) FROM server_dns_leases WHERE server_id=ANY($1)),(SELECT count(*) FROM dns_withdrawal_holds)", []uuid.UUID{approved.id, pending}).Scan(&leases, &holds); e != nil {
		t.Fatal(e)
	}
	if leases != 0 || holds != 0 {
		t.Fatal("leases kept", leases, "holds opened", holds)
	}
	// The recorded name's job converges it on the approval, publishing the record it lost.
	if all, _ := dnsJobs(); all != queued+1 {
		t.Fatal("the adopted server's job was not queued", all-queued)
	}
	eventually(t, "the adopted server's job to publish its approved records", func() bool { return z.marked(lost.name(), lost.marker) == 2 })
	// A second sweep finds nothing more to do.
	before = z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before)
}

func TestZoneSweepWaitsForAJobHoldingTheServersLease(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	ctx := context.Background()
	b := f.browser(t)
	ws := b.login("owner@example.test", f.password).Workspaces[0].Id
	pending := enrolledServer(t, f, b, ws)
	record := z.add("A", zoneName(zoneBase, pending), "203.0.113.1", zoneMarker(installationOf(t, f), pending))
	if _, e := f.st.Pool.Exec(ctx, "INSERT INTO server_dns_leases(server_id,holder,expires_at) VALUES($1,gen_random_uuid(),now()+interval '1 hour')", pending); e != nil {
		t.Fatal(e)
	}
	before := z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before)
	if _, e := f.st.Pool.Exec(ctx, "DELETE FROM server_dns_leases WHERE server_id=$1", pending); e != nil {
		t.Fatal(e)
	}
	sweepZone(t, f)
	expectZone(t, z, before, record)
}

type zoneHold struct {
	id                  uuid.UUID
	confirmed           bool
	closeReason         *string
	records, withdrawn  int
	unconfirmedRemovals int
}

func zoneHolds(t *testing.T, f *fixture) []zoneHold {
	t.Helper()
	rows, e := f.st.Pool.Query(context.Background(), `SELECT h.id,h.confirmed_at IS NOT NULL,h.close_reason,h.records,
		(SELECT count(*) FROM dns_record_removals r WHERE r.hold_id=h.id AND r.confirmed_at IS NOT NULL),
		(SELECT count(*) FROM dns_record_removals r WHERE r.hold_id=h.id AND r.confirmed_at IS NULL)
		FROM dns_withdrawal_holds h ORDER BY h.opened_at,h.id`)
	if e != nil {
		t.Fatal(e)
	}
	defer rows.Close()
	var out []zoneHold
	for rows.Next() {
		var h zoneHold
		if e = rows.Scan(&h.id, &h.confirmed, &h.closeReason, &h.records, &h.withdrawn, &h.unconfirmedRemovals); e != nil {
			t.Fatal(e)
		}
		out = append(out, h)
	}
	return out
}

func heldAlert(t *testing.T, f *fixture, ws uuid.UUID) string {
	t.Helper()
	alertSweep(t, f, ws)
	var state string
	e := f.st.Pool.QueryRow(context.Background(), "SELECT COALESCE(max(state),'none') FROM operational_alerts WHERE workspace_id=$1 AND kind='dns_withdrawal_held'", ws).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	return state
}

func TestZoneSweepHoldsAMassWithdrawalUntilTheOperatorConfirms(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	ctx := context.Background()
	published := publishedServer(t, f, z)
	installation := installationOf(t, f)
	pending := enrolledServer(t, f, published.b, published.ws)
	held := []string{z.add("A", zoneName(zoneBase, pending), "203.0.113.1", zoneMarker(installation, pending))}
	unknown := func(n int) []string {
		var ids []string
		for range n {
			server := uuid.New()
			ids = append(ids, z.add("A", zoneName(zoneBase, server), "203.0.113.2", zoneMarker(installation, server)))
		}
		return ids
	}
	held = append(held, unknown(11)...)

	// Twelve of fourteen marked records: more than ten, and more than a fifth.
	before := z.snapshot()
	sweepZone(t, f)
	sweepZone(t, f)
	expectZone(t, z, before)
	holds := zoneHolds(t, f)
	if len(holds) != 1 || holds[0].confirmed || holds[0].closeReason != nil || holds[0].records != 12 {
		t.Fatalf("holds %+v", holds)
	}
	first := holds[0].id
	shown, e := execution.ZoneHolds(ctx, f.st.Pool, 3)
	if e != nil || len(shown) != 1 || shown[0].ID != first || len(shown[0].Sample) != 3 || len(shown[0].Workspaces) != 2 ||
		shown[0].Workspaces[0].WorkspaceID != nil || shown[0].Workspaces[0].Records != 11 || *shown[0].Workspaces[1].WorkspaceID != published.ws {
		t.Fatalf("shown %+v: %v", shown, e)
	}
	if state := heldAlert(t, f, published.ws); state != "firing" {
		t.Fatal("the held records' workspace was not alerted:", state)
	}

	// The operator confirms exactly the hold they reviewed.
	if e = execution.ConfirmZoneHold(ctx, f.st.Pool, first, 11, "operator", "restored from a backup"); !errors.Is(e, execution.ErrHoldChanged) {
		t.Fatal("a confirmation that counted differently was accepted", e)
	}
	if e = execution.ConfirmZoneHold(ctx, f.st.Pool, first, 12, "operator", ""); e == nil {
		t.Fatal("a confirmation without a note was accepted")
	}
	if e = execution.ConfirmZoneHold(ctx, f.st.Pool, first, 12, "operator", "restored from a backup"); e != nil {
		t.Fatal(e)
	}
	if state := heldAlert(t, f, published.ws); state != "resolved" {
		t.Fatal("the confirmed hold's alert did not resolve:", state)
	}

	// Records found since aren't withdrawn on the strength of that confirmation: eleven more are
	// held again, while the confirmed twelve go.
	later := unknown(11)
	before = z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before, held...)
	holds = zoneHolds(t, f)
	if len(holds) != 2 || holds[0].id != first || holds[0].withdrawn != 12 || holds[1].confirmed || holds[1].records != 11 {
		t.Fatalf("holds %+v", holds)
	}
	// The new hold can be confirmed while the first is still open.
	second := holds[1].id
	if e = execution.ConfirmZoneHold(ctx, f.st.Pool, second, 11, "operator", "checked"); e != nil {
		t.Fatal(e)
	}

	// The first hold closes once its records are gone. A confirmation lapses after a day; the
	// records it held are held again.
	if _, e = f.st.Pool.Exec(ctx, "UPDATE dns_withdrawal_holds SET confirmed_at=now()-interval '25 hours' WHERE id=$1", second); e != nil {
		t.Fatal(e)
	}
	before = z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before)
	holds = zoneHolds(t, f)
	if len(holds) != 3 || *holds[0].closeReason != "withdrawn" || *holds[1].closeReason != "lapsed" || holds[2].closeReason != nil || holds[2].records != 11 {
		t.Fatalf("holds %+v", holds)
	}
	third := holds[2].id

	// A record the hold doesn't hold replaces it, so a confirmation never reaches a record the
	// operator wasn't shown.
	extra := unknown(2)
	sweepZone(t, f)
	expectZone(t, z, before)
	if e = execution.ConfirmZoneHold(ctx, f.st.Pool, third, 11, "operator", "checked"); !errors.Is(e, execution.ErrHoldChanged) {
		t.Fatal("a superseded hold was confirmed", e)
	}
	holds = zoneHolds(t, f)
	if len(holds) != 4 || *holds[2].closeReason != "superseded" || holds[3].closeReason != nil || holds[3].records != 13 {
		t.Fatalf("holds %+v", holds)
	}
	// One that has gone since leaves the hold as it was, to be confirmed as shown.
	z.mu.Lock()
	delete(z.records, later[0])
	z.mu.Unlock()
	before = z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before)
	if holds = zoneHolds(t, f); len(holds) != 4 || holds[3].closeReason != nil || holds[3].records != 13 {
		t.Fatalf("holds %+v", holds)
	}

	// Once few enough remain, they are withdrawn without confirmation and the hold is cleared.
	z.mu.Lock()
	for _, id := range later[1:6] {
		delete(z.records, id)
	}
	z.mu.Unlock()
	before = z.snapshot()
	sweepZone(t, f)
	expectZone(t, z, before, append(later[6:], extra...)...)
	if holds = zoneHolds(t, f); *holds[3].closeReason != "cleared" {
		t.Fatalf("holds %+v", holds)
	}
	if n := z.marked(published.name(), published.marker); n != 2 {
		t.Fatal("the approved server's records were touched", n)
	}
}

// backupDatabase takes a backup of the fixture's database as docs/management-backups.md does, with
// pg_dump, and returns where it is in the test database's container.
func backupDatabase(t *testing.T, f *fixture) string {
	t.Helper()
	file := "/tmp/backup-" + uuid.NewString() + ".dump"
	if out, e := testDatabase("pg_dump", "--username", "postgres", "--format", "custom", "--file", file, f.st.Pool.Config().ConnConfig.Database); e != nil {
		t.Fatal(string(out), e)
	}
	return file
}

// restoreDatabase restores a backup into an empty database, as docs/management-backups.md does, and
// serves it with the keyring of the installation it was taken from.
func restoreDatabase(t *testing.T, source *fixture, file string) *fixture {
	t.Helper()
	name := "test_" + uuid.NewString()[:8] + "_restored"
	if out, e := testDatabase("createdb", "--username", "postgres", name); e != nil {
		t.Fatal(string(out), e)
	}
	if out, e := testDatabase("pg_restore", "--username", "postgres", "--single-transaction", "--exit-on-error", "--dbname", name, file); e != nil {
		t.Fatal(string(out), e)
	}
	return setupDatabase(t, name, false, false, keysOf(source.st))
}

func TestZoneSweepWithdrawsWhatARestoredBackupNeverKnew(t *testing.T) {
	f := setup(t)
	f.st.ApplicationDomain = zoneBase
	z := newDNSZone(t, f)
	ctx := context.Background()
	kept := publishedServer(t, f, z)
	approvedLater := enrolledServer(t, f, kept.b, kept.ws)
	backup := backupDatabase(t, f)

	// After the backup: a server enrolled and published, and one enrolled before it approved.
	enrolledLater := publishedServer(t, f, z)
	later := dnsServer{f: f, b: kept.b, ws: kept.ws, id: approvedLater, path: "/api/v1/workspaces/" + kept.ws.String() + "/servers/" + approvedLater.String(), marker: zoneMarker(installationOf(t, f), approvedLater)}
	later.b.call("PUT", later.path+"/domain", api.ConfigureServerDomain{Ipv4: "198.51.100.30", Ipv6: "2001:db8::30"}, 200, nil)
	later.waitState(t, "applied")
	var gone []string
	for id, r := range z.snapshot() {
		if r["comment"] == enrolledLater.marker || r["comment"] == later.marker {
			gone = append(gone, id)
		}
	}
	if len(gone) != 4 {
		t.Fatal("expected two servers' A and AAAA records, got", len(gone))
	}

	// The management that replaced it starts on the backup, with the same zone.
	restored := restoreDatabase(t, f, backup)
	restored.st.ApplicationDomain = zoneBase
	z.attach(restored)
	before := z.snapshot()
	sweepZone(t, restored)
	expectZone(t, z, before, gone...)
	removals := unbackedRemovals(t, restored)
	for _, id := range gone {
		ws, ok := removals[id]
		// The server enrolled after the backup is unknown to it; the other is its workspace's.
		if !ok || (before[id]["comment"] == enrolledLater.marker) != (ws == nil) {
			t.Fatalf("audited %v", removals)
		}
	}
	if n := z.marked(kept.name(), kept.marker); n != 2 {
		t.Fatal("the record the backup approved was touched", n)
	}
	if e := restored.st.Pool.QueryRow(ctx, "SELECT 1 FROM dns_withdrawal_holds").Scan(new(int)); e == nil {
		t.Fatal("four of six records were held, not withdrawn")
	}
}

// The hosted service's owners can't confirm a hold, so it never alerts them; its operator reads
// the management log.
func TestHostedServiceDoesNotAlertOwnersAboutHeldRecords(t *testing.T) {
	f := setupStore(t, true, true)
	ctx := context.Background()
	ws := f.browser(t).login("owner@example.test", f.password).Workspaces[0].Id
	var hold uuid.UUID
	if e := f.st.Pool.QueryRow(ctx, "INSERT INTO dns_withdrawal_holds(zone,records,marked) VALUES('zone',1,1) RETURNING id").Scan(&hold); e != nil {
		t.Fatal(e)
	}
	if _, e := f.st.Pool.Exec(ctx, "INSERT INTO dns_withdrawal_hold_records(hold_id,record_id,server_id,workspace_id,name,record_type,content) VALUES($1,'r',gen_random_uuid(),$2,'n','A','203.0.113.1')", hold, ws); e != nil {
		t.Fatal(e)
	}
	if state := heldAlert(t, f, ws); state != "none" {
		t.Fatal("the hosted service alerted owners about a hold:", state)
	}
	f.st.Hosted = false
	if state := heldAlert(t, f, ws); state != "firing" {
		t.Fatal("a self-hosted installation did not alert:", state)
	}
}
