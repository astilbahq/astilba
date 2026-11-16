package execution

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ZoneSweepInterval is how often management compares the provider's zone with the database. It
// also runs once at startup, which is what follows a restore from an older backup.
const ZoneSweepInterval = 5 * time.Minute

// A zone sweep that would withdraw more than holdRecords records, which are also more than one in
// holdShare of the installation's marked address records it listed, withdraws none of them and
// waits for the installation operator to confirm. A restore drill given the live zone, or a fault
// in the sweep itself, looks like that; a restore from an older backup does too, and goes ahead
// once confirmed.
const (
	holdRecords = 10
	holdShare   = 5
)

// zoneRecord is one of this installation's address records in the provider's zone, with the
// server and base its marker and name name, and, once the sweep has judged it, the server's
// workspace and why it is withdrawn.
type zoneRecord struct {
	applicationdns.Marked
	server    uuid.UUID
	base      string
	workspace *uuid.UUID
	reason    string
}

// zoneName is a server's name under one base, and the records the zone holds there, which the
// sweep withdraws for one reason when it has judged them.
type zoneName struct {
	server  uuid.UUID
	base    string
	reason  string
	records []zoneRecord
}

// SweepZone compares the provider's zone with the database. It lists every record whose comment
// carries this installation's marker, with nothing held. A record at the name of its server's last
// approval that the provider changed after the approval, to an address the approval doesn't hold,
// shows the zone is newer than the database: the approval ends, if it is current, and the record
// is withdrawn as outdated (see outdatedMargin). Any other record at a name the database records
// is left to the publication's own withdrawal. One at a name it has lost, whose server's approval
// for that base is current, is recorded as a publication again, and the server's job queued. Any
// other is unbacked: the database doesn't know it made it, or its server no longer may have it, so
// it may point at an address the server no longer holds. Outdated and unbacked records are
// withdrawn, under the server's DNS lease where the server is known, unless there are too many at
// once (see holdRecords). Records of another installation, records without our marker, and
// records Astilba never makes (other types, or a name that isn't the marker's server's) are never
// touched.
func (s *Service) SweepZone(ctx context.Context) error {
	if s.DNS == nil {
		return nil
	}
	installation, e := db.New(s.Store.Pool).InstallationID(ctx)
	if e != nil {
		return e
	}
	prefix := "astilba:" + installation.String() + ":"
	listed, e := s.DNS.ListMarked(ctx, prefix)
	if e != nil {
		return e
	}
	names, marked, strays := parseZone(listed, prefix)
	if strays > 0 {
		slog.Warn("the DNS zone holds records with this installation's marker that Astilba never makes; they are left alone, inspect them", "records", strays)
	}
	if len(names) == 0 {
		return s.decideHolds(ctx, nil, marked, nil)
	}
	backing, e := s.zoneBacking(ctx, db.New(s.Store.Pool), names)
	if e != nil {
		return e
	}
	var failures []error
	var unbacked []zoneRecord
	known := map[uuid.UUID]bool{}
	for i, n := range names {
		b := backing[i]
		known[n.server] = b.Known
		approved := b.Approved
		rest := n.records
		if outdated := outdatedRecords(n, b); len(outdated) > 0 {
			if approved {
				failures = append(failures, s.endOutdatedZoneApproval(ctx, n, b))
				approved = false
			}
			rest = slices.DeleteFunc(slices.Clone(rest), func(r zoneRecord) bool {
				return slices.ContainsFunc(outdated, func(o zoneRecord) bool { return o.ID == r.ID })
			})
			for _, r := range outdated {
				r.workspace, r.reason = b.WorkspaceID, withdrawOutdated
				unbacked = append(unbacked, r)
			}
		}
		switch {
		case b.Recorded || len(rest) == 0:
		case approved:
			failures = append(failures, s.adoptZoneName(ctx, n, b))
		default:
			for _, r := range rest {
				r.workspace, r.reason = b.WorkspaceID, withdrawUnbacked
				unbacked = append(unbacked, r)
			}
		}
	}
	// Records are withdrawn only once the holds are settled.
	confirmed := map[string]uuid.UUID{}
	if e = s.decideHolds(ctx, unbacked, marked, func(withdraw []zoneRecord, holds map[string]uuid.UUID) {
		unbacked, confirmed = withdraw, holds
	}); e != nil {
		return errors.Join(append(failures, e)...)
	}
	for _, n := range groupZone(unbacked) {
		failures = append(failures, s.withdrawZoneRecords(ctx, installation, n, known[n.server], confirmed))
	}
	return errors.Join(failures...)
}

// parseZone keeps the address records whose marker is exactly this installation's and a server's,
// at that server's name under some base. It counts the address records listed, which the hold
// threshold compares with, and the records it left out.
func parseZone(listed []applicationdns.Marked, prefix string) ([]zoneName, int, int) {
	byName := map[[2]string]*zoneName{}
	var order [][2]string
	marked, strays := 0, 0
	for _, r := range listed {
		if r.Type != "A" && r.Type != "AAAA" {
			strays++
			continue
		}
		marked++
		server, e := uuid.Parse(strings.TrimPrefix(r.Comment, prefix))
		if e != nil || prefix+server.String() != r.Comment {
			strays++
			continue
		}
		label := "*." + domain.ServerDomain("", server)
		base, ok := strings.CutPrefix(r.Name, label)
		if !ok || !domain.ValidApplicationDomain(base) {
			strays++
			continue
		}
		key := [2]string{server.String(), base}
		if byName[key] == nil {
			byName[key] = &zoneName{server: server, base: base}
			order = append(order, key)
		}
		byName[key].records = append(byName[key].records, zoneRecord{Marked: r, server: server, base: base})
	}
	out := make([]zoneName, 0, len(order))
	for _, k := range order {
		out = append(out, *byName[k])
	}
	return out, marked, strays
}

// outdatedRecords are the records at a name that show its server's last approval there is older
// than the zone.
func outdatedRecords(n zoneName, b db.ZoneRecordBackingRow) []zoneRecord {
	var out []zoneRecord
	for _, r := range n.records {
		if outdatedBy(b, r) {
			out = append(out, r)
		}
	}
	return out
}

// outdatedBy reports whether a record shows its server's last approval at its name is older than
// the zone.
func outdatedBy(b db.ZoneRecordBackingRow, r zoneRecord) bool {
	return b.ApprovalHere && storedApproval{b.Ipv4, b.Ipv6, b.ApprovedAt}.outdates(r.Type, r.Content, r.ModifiedOn)
}

// groupZone gathers records by the server and name they're at, and why they are withdrawn.
func groupZone(records []zoneRecord) []zoneName {
	at := map[[3]string]int{}
	var out []zoneName
	for _, r := range records {
		key := [3]string{r.server.String(), r.base, r.reason}
		i, ok := at[key]
		if !ok {
			i = len(out)
			at[key] = i
			out = append(out, zoneName{server: r.server, base: r.base, reason: r.reason})
		}
		out[i].records = append(out[i].records, r)
	}
	return out
}

// zoneBacking reads what backs each name, one row for each, in the order of names.
func (s *Service) zoneBacking(ctx context.Context, q *db.Queries, names []zoneName) ([]db.ZoneRecordBackingRow, error) {
	params := db.ZoneRecordBackingParams{Zone: s.DNS.Zone, Configured: s.Store.ApplicationDomain}
	for _, n := range names {
		params.ServerIds = append(params.ServerIds, n.server)
		params.Bases = append(params.Bases, n.base)
	}
	return q.ZoneRecordBacking(ctx, params)
}

// adoptZoneName records a name the database lost while the server's approval for it is current,
// and queues the server's job, which publishes the approved addresses there, or withdraws them if
// management is inactive. It holds the server's DNS lock, as revocation does, so it never records
// a name for a server revoked meanwhile.
func (s *Service) adoptZoneName(ctx context.Context, n zoneName, b db.ZoneRecordBackingRow) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = store.LockServerDNS(ctx, tx, n.server); e != nil {
		return e
	}
	adopted, e := db.New(tx).AdoptZonePublication(ctx, db.AdoptZonePublicationParams{Zone: s.DNS.Zone, ServerID: n.server, Base: n.base, Configured: s.Store.ApplicationDomain})
	if e != nil || adopted == 0 {
		return e
	}
	slog.Info("the DNS zone held a server's approved name the database had lost; it is recorded again", "server", n.server)
	if e = s.enqueueDomain(ctx, tx, domainArgs{Server: n.server, Generation: b.Generation, Controller: b.Controller}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// decideHolds settles which unbacked records this sweep withdraws, under the zone's sweep lock.
// Those a confirmed hold holds are withdrawn under it. The rest are withdrawn too, unless there are
// too many of them: then they are held, in the hold awaiting confirmation if it holds every one of
// them, or in a new one that supersedes it. A hold that is no longer needed is closed. done is told
// what to withdraw, and the confirmed hold each record is withdrawn under, if any, once that is
// committed.
func (s *Service) decideHolds(ctx context.Context, unbacked []zoneRecord, marked int, done func([]zoneRecord, map[string]uuid.UUID)) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	zone := s.DNS.Zone
	if e = q.LockDNSZoneSweep(ctx, zone); e != nil {
		return e
	}
	if e = q.LapseDNSWithdrawalHolds(ctx, zone); e != nil {
		return e
	}
	if e = q.CloseWithdrawnDNSWithdrawalHolds(ctx, zone); e != nil {
		return e
	}
	awaiting, e := q.AwaitingDNSWithdrawalHold(ctx, zone)
	if errors.Is(e, pgx.ErrNoRows) {
		awaiting = uuid.Nil
	} else if e != nil {
		return e
	}
	confirmed, e := q.ConfirmedDNSWithdrawalHoldRecords(ctx, zone)
	if e != nil {
		return e
	}
	under := make(map[string]uuid.UUID, len(confirmed))
	for _, r := range confirmed {
		under[r.RecordID] = r.HoldID
	}
	withdraw, rest := []zoneRecord{}, []zoneRecord{}
	for _, r := range unbacked {
		if _, ok := under[r.ID]; ok {
			withdraw = append(withdraw, r)
		} else {
			rest = append(rest, r)
		}
	}
	if len(rest) > holdRecords && len(rest)*holdShare > marked {
		if e = s.holdZoneRecords(ctx, q, awaiting, rest, marked); e != nil {
			return e
		}
	} else {
		withdraw = append(withdraw, rest...)
		if awaiting != uuid.Nil {
			if e = q.CloseDNSWithdrawalHold(ctx, db.CloseDNSWithdrawalHoldParams{ID: awaiting, CloseReason: "cleared"}); e != nil {
				return e
			}
		}
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	if done != nil {
		done(withdraw, under)
	}
	return nil
}

// holdZoneRecords keeps records held for the operator. The hold awaiting confirmation is kept if
// it holds every one of these records, and otherwise superseded, so a confirmation only ever
// applies to records the operator was shown.
func (s *Service) holdZoneRecords(ctx context.Context, q *db.Queries, awaiting uuid.UUID, records []zoneRecord, marked int) error {
	hold := uuid.Nil
	if awaiting != uuid.Nil {
		held, e := q.DNSWithdrawalHoldRecordIDs(ctx, awaiting)
		if e != nil {
			return e
		}
		holds := make(map[string]bool, len(held))
		for _, id := range held {
			holds[id] = true
		}
		if !slices.ContainsFunc(records, func(r zoneRecord) bool { return !holds[r.ID] }) {
			hold = awaiting
			if e = q.SeeDNSWithdrawalHold(ctx, db.SeeDNSWithdrawalHoldParams{ID: hold, Marked: int32(marked)}); e != nil {
				return e
			}
		} else if e = q.CloseDNSWithdrawalHold(ctx, db.CloseDNSWithdrawalHoldParams{ID: awaiting, CloseReason: "superseded"}); e != nil {
			return e
		}
	}
	if hold == uuid.Nil {
		var e error
		if hold, e = q.OpenDNSWithdrawalHold(ctx, db.OpenDNSWithdrawalHoldParams{Zone: s.DNS.Zone, Records: int32(len(records)), Marked: int32(marked)}); e != nil {
			return e
		}
		rows := make([]db.InsertDNSWithdrawalHoldRecordsParams, 0, len(records))
		for _, r := range records {
			rows = append(rows, db.InsertDNSWithdrawalHoldRecordsParams{HoldID: hold, RecordID: r.ID, ServerID: r.server, WorkspaceID: r.workspace, Name: r.Name, RecordType: r.Type, Content: r.Content})
		}
		if _, e = q.InsertDNSWithdrawalHoldRecords(ctx, rows); e != nil {
			return e
		}
	}
	slog.Error("the DNS zone sweep is holding back the withdrawal of many records nothing backs, or newer than their server's approval; they may resolve to addresses Astilba no longer controls. Review them with `astilba dns withdrawals` and confirm with `astilba dns confirm-withdrawals`; see docs/hosting.md#records-the-database-has-lost", "records", len(records), "marked", marked, "hold", hold)
	return nil
}

// withdrawZoneRecords withdraws the listed records at one of a server's names, once it has checked
// they are still unbacked, or still outdated by the server's approval there. A known server's
// records are withdrawn under its DNS lease, so no job publishes there meanwhile; if a job holds
// it, the next sweep tries again. A server this database has never heard of has no lease to take.
// One can appear while the sweep works, when a workspace is imported with the server's ID, so each
// deletion's audit row is written only while the server is still as known or unknown as the sweep
// found it; an imported server never has a publication or a current approval, so its records
// would be unbacked either way. A record the provider changed since the sweep listed it is left
// for the next sweep to judge.
func (s *Service) withdrawZoneRecords(ctx context.Context, installation uuid.UUID, n zoneName, known bool, holds map[string]uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, dnsWork)
	defer cancel()
	name, marker := dnsName(n.base, n.server), dnsMarker(installation, n.server)
	var listed []applicationdns.Marked
	// still narrows listed to the records that may still be withdrawn, and reports whether any are.
	still := func(ctx context.Context, q *db.Queries) (bool, error) {
		rows, e := s.zoneBacking(ctx, q, []zoneName{n})
		if e != nil {
			return false, e
		}
		b := rows[0]
		listed = listed[:0]
		for _, r := range n.records {
			switch n.reason {
			case withdrawUnbacked:
				if b.Known == known && !b.Recorded && !b.Approved {
					listed = append(listed, r.Marked)
				}
			case withdrawOutdated:
				if b.Known == known && outdatedBy(b, r) {
					listed = append(listed, r.Marked)
				}
			}
		}
		return len(listed) > 0, nil
	}
	audit := func(r applicationdns.Removed) error {
		slog.Info("removing an application DNS record", "server", n.server, "record", r.ID, "type", r.Type, "reason", n.reason)
		var hold *uuid.UUID
		if h, ok := holds[r.ID]; ok {
			hold = &h
		}
		written, e := db.New(s.Store.Pool).InsertZoneDNSRecordRemoval(ctx, db.InsertZoneDNSRecordRemovalParams{ServerID: n.server, Name: name, Zone: s.DNS.Zone, RecordID: r.ID, RecordType: r.Type, Content: r.Content, Reason: n.reason, HoldID: hold, Known: known})
		if e == nil && written != 1 {
			e = errors.New("the server's state changed after the zone sweep judged its records; the next sweep judges them again")
		}
		return e
	}
	confirm := func(ctx context.Context, q *db.Queries, left []string) error {
		return q.ConfirmDNSRecordRemovals(ctx, db.ConfirmDNSRecordRemovalsParams{ServerID: n.server, Name: name, Zone: s.DNS.Zone, Kept: left})
	}
	if !known {
		remaining, e := still(ctx, db.New(s.Store.Pool))
		if e != nil || !remaining {
			return e
		}
		left, e := s.DNS.RemoveListed(ctx, name, marker, listed, audit)
		if e != nil {
			return e
		}
		return confirm(ctx, db.New(s.Store.Pool), left)
	}
	lease, taken, e := s.takeDNSLease(ctx, n.server)
	if e != nil || !taken {
		return e
	}
	remaining := false
	e = s.underDNSLease(ctx, lease, func(ctx context.Context, tx pgx.Tx) error {
		var e error
		remaining, e = still(ctx, db.New(tx))
		return e
	})
	var left []string
	if e == nil && remaining {
		left, e = s.DNS.RemoveListed(ctx, name, marker, listed, audit)
		if e == nil {
			e = s.underDNSLease(ctx, lease, func(ctx context.Context, tx pgx.Tx) error { return confirm(ctx, db.New(tx), left) })
		}
	}
	// A request that got no answer may still reach the provider, so the lease runs out on its own.
	if !errors.Is(e, applicationdns.ErrUnanswered) {
		s.releaseDNSLease(lease)
	}
	return e
}

// ErrHoldChanged refuses an operator's confirmation of a hold that no longer awaits it, or no
// longer holds as many records as they counted.
var ErrHoldChanged = errors.New("that hold no longer awaits confirmation with that many records; run `astilba dns withdrawals` again and confirm what it shows")

// ZoneHold is a hold as the operator's commands show it.
type ZoneHold struct {
	db.ListDNSWithdrawalHoldsRow
	Workspaces []db.DNSWithdrawalHoldWorkspacesRow
	Sample     []db.DNSWithdrawalHoldSampleRow
}

// ZoneHolds lists the holds open in every zone, with the records each holds per workspace and a
// sample of them.
func ZoneHolds(ctx context.Context, pool *pgxpool.Pool, sample int32) ([]ZoneHold, error) {
	q := db.New(pool)
	holds, e := q.ListDNSWithdrawalHolds(ctx)
	if e != nil {
		return nil, e
	}
	out := make([]ZoneHold, 0, len(holds))
	for _, h := range holds {
		v := ZoneHold{ListDNSWithdrawalHoldsRow: h}
		if v.Workspaces, e = q.DNSWithdrawalHoldWorkspaces(ctx, h.ID); e != nil {
			return nil, e
		}
		if v.Sample, e = q.DNSWithdrawalHoldSample(ctx, db.DNSWithdrawalHoldSampleParams{HoldID: h.ID, Max: sample}); e != nil {
			return nil, e
		}
		out = append(out, v)
	}
	return out, nil
}

// ConfirmZoneHold records the operator's confirmation of a hold, which lets the next zone sweep
// withdraw the records it holds that are still unbacked then. The operator names how many records
// it holds, so they confirm what they reviewed. Taken under the zone's sweep lock, it never
// confirms a hold a sweep is superseding.
func ConfirmZoneHold(ctx context.Context, pool *pgxpool.Pool, hold uuid.UUID, records int32, operator, note string) error {
	if strings.TrimSpace(operator) == "" || strings.TrimSpace(note) == "" {
		return errors.New("name who confirms with --operator and why with --note; both are kept with the hold")
	}
	tx, e := pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	zone, e := q.DNSWithdrawalHoldZone(ctx, hold)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrHoldChanged
	} else if e != nil {
		return e
	}
	if e = q.LockDNSZoneSweep(ctx, zone); e != nil {
		return e
	}
	n, e := q.ConfirmDNSWithdrawalHold(ctx, db.ConfirmDNSWithdrawalHoldParams{ConfirmedBy: operator, Note: note, ID: hold, Records: records})
	if e != nil {
		return e
	}
	if n != 1 {
		return ErrHoldChanged
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	slog.Info("the installation operator confirmed a held DNS withdrawal; the next zone sweep withdraws its records that are still unbacked", "hold", hold, "records", records, "operator", operator)
	return nil
}
