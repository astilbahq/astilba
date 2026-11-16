package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// A management restored from an older backup holds approvals the owner may have replaced since.
// Publishing one again could point the server's names at an address someone else now holds, so
// before anything is published, the provider's records are judged against the approval. A record
// of ours the provider changed after the approval, to an address the approval doesn't hold, shows
// the zone is newer than the database. So does a record the last job observed that is now gone
// when this database never deleted it. Either way the approval ends, nothing is published, and an
// owner approves the addresses again; the zone sweep withdraws the newer records meanwhile.
//
// A change counts only once it is more than outdatedMargin after the approval. Within the DNS
// lease's term, a job's request may still land after a newer approval (see dnsLeaseTerm), and
// the job after it puts the approved address back. The extra minute allows for the database's
// clock, which wrote the approval's time, against the provider's.
const outdatedMargin = dnsLeaseTerm + time.Minute

const outdatedApprovalNote = "Astilba stopped publishing this server's DNS records: they were changed or removed in DNS after these addresses were approved, which happens when management is restored from an older backup, so the approved addresses may no longer be the server's. Astilba withdraws the records that changed, and publishes nothing for the server until an owner checks its addresses and approves them again."

var errApprovalOutdated = errors.New("the DNS zone shows the server's records changed after its address approval")

// storedApproval is a server's last address approval for a base: its addresses, and when it was
// given. A time of nil is an approval given before times were recorded, older than any record.
type storedApproval struct {
	ipv4, ipv6 string
	at         *time.Time
}

func (a storedApproval) address(kind string) string {
	if kind == "AAAA" {
		return a.ipv6
	}
	return a.ipv4
}

// outdates reports whether one of our address records at the approval's name shows the zone moved
// past the approval: it holds something else, and changed after the approval and its margin.
func (a storedApproval) outdates(kind, content string, modified time.Time) bool {
	return content != a.address(kind) && (a.at == nil || modified.After(a.at.Add(outdatedMargin)))
}

// lost reports whether a record the last job observed at the name, for an address the approval
// still holds, is gone with nothing of ours of its type in its place, though this database never
// deleted it. Something removed it after this database last looked. observed is empty when the
// records were observed in another zone.
func (a storedApproval) lost(observed map[string]string, audited map[string]bool, present []applicationdns.Listed) bool {
	for _, kind := range []string{"A", "AAAA"} {
		id := observed[kind]
		if a.address(kind) == "" || id == "" || audited[id] {
			continue
		}
		replaced := false
		for _, r := range present {
			replaced = replaced || r.Type == kind
		}
		if !replaced {
			return true
		}
	}
	return false
}

// approvalJudge judges the provider's records at the approval's name against it, given the
// records the last job observed there and the zone it observed them in. Records observed before
// the zone was recorded are taken to be in the configured zone, as publications are. It reads, on
// the pool, which of the observed records this database has deleted, before the provider is asked
// anything.
func (s *Service) approvalJudge(ctx context.Context, approval storedApproval, records []byte, zone string) (applicationdns.Judge, error) {
	observed := map[string]string{}
	if zone == s.DNS.Zone || zone == "" {
		if e := json.Unmarshal(records, &observed); e != nil {
			return nil, e
		}
	}
	ids := make([]string, 0, len(observed))
	for _, id := range observed {
		ids = append(ids, id)
	}
	audited := map[string]bool{}
	if len(ids) > 0 {
		found, e := db.New(s.Store.Pool).AuditedDNSRecordRemovals(ctx, db.AuditedDNSRecordRemovalsParams{Zone: s.DNS.Zone, RecordIds: ids})
		if e != nil {
			return nil, e
		}
		for _, id := range found {
			audited[id] = true
		}
	}
	return func(present []applicationdns.Listed) error {
		for _, r := range present {
			if approval.outdates(r.Type, r.Content, r.ModifiedOn) {
				return errApprovalOutdated
			}
		}
		if approval.lost(observed, audited, present) {
			return errApprovalOutdated
		}
		return nil
	}, nil
}

// endOutdatedApproval ends the approval a job found outdated, under its lease, as long as it is
// still the approval the job published.
func (s *Service) endOutdatedApproval(ctx context.Context, lease dnsLease, a domainArgs) error {
	return s.underDNSLease(ctx, lease, func(ctx context.Context, tx pgx.Tx) error {
		return s.endOutdated(ctx, db.New(tx), a)
	})
}

// endOutdatedZoneApproval ends an approval the zone sweep found outdated, under the server's DNS
// lock, as revocation does, as long as it is still the approval the sweep judged.
func (s *Service) endOutdatedZoneApproval(ctx context.Context, n zoneName, b db.ZoneRecordBackingRow) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = store.LockServerDNS(ctx, tx, n.server); e != nil {
		return e
	}
	if e = s.endOutdated(ctx, db.New(tx), domainArgs{Server: n.server, Generation: b.Generation, Controller: b.Controller}); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

func (s *Service) endOutdated(ctx context.Context, q *db.Queries, a domainArgs) error {
	ended, e := q.EndOutdatedServerDomain(ctx, db.EndOutdatedServerDomainParams{Note: outdatedApprovalNote, ServerID: a.Server, Generation: a.Generation, Controller: pgtype.Int8{Int64: a.Controller, Valid: true}})
	if e == nil && ended == 1 {
		slog.Error("the DNS zone shows a server's records changed after its address approval, as after a restore from an older backup; the approval ended and an owner must approve the addresses again", "server", a.Server)
	}
	return e
}
