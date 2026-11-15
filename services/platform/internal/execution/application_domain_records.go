package execution

import (
	"context"
	"errors"
	"log/slog"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/applicationdns"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Why a publication is withdrawn. These are the reasons dns_record_removals accepts.
const (
	withdrawRevoked     = "revoked"
	withdrawInactive    = "management_inactive"
	withdrawBaseChanged = "base_changed"
	withdrawAddress     = "address_removed"
)

const (
	revokedDomainNote  = "The server was revoked, so its DNS approval ended. After renewing it, an owner must approve its addresses again."
	withdrawnNoteFirst = "Astilba removed this server's DNS records because "
)

var withdrawnNotes = map[string]string{
	withdrawRevoked:  withdrawnNoteFirst + "the server was revoked. After renewing it, an owner must approve its addresses again.",
	withdrawInactive: withdrawnNoteFirst + "hosted management is inactive. They are published again when management resumes.",
}

func dnsMarker(installation, server uuid.UUID) string {
	return "astilba:" + installation.String() + ":" + server.String()
}
func dnsName(base string, server uuid.UUID) string { return "*." + domain.ServerDomain(base, server) }

// withdrawRevokedDomain runs in the revocation's transaction. It ends the address approval and
// marks every name the server may hold for removal, so neither a renewal nor a transfer that
// lifts the revocation keeps the records. The job it queues starts removal straight away.
func (s *Service) withdrawRevokedDomain(ctx context.Context, tx pgx.Tx, ws, server uuid.UUID) error {
	if e := store.LockServerDNS(ctx, tx, server); e != nil {
		return e
	}
	q := db.New(tx)
	generation, e := q.WithdrawRevokedServerDomain(ctx, db.WithdrawRevokedServerDomainParams{Note: revokedDomainNote, WorkspaceID: ws, ServerID: server})
	if errors.Is(e, pgx.ErrNoRows) {
		generation = 0
	} else if e != nil {
		return e
	}
	// Marked even while automation is off, so the removal still happens once it's back.
	marked, e := q.RevokeDNSPublications(ctx, server)
	if e != nil || marked == 0 || s.DNS == nil {
		return e
	}
	return s.enqueueDomain(ctx, tx, domainArgs{Server: server, Generation: generation})
}

// A DNS job asks the provider for at most dnsWork. Its lease on the server lasts twice that,
// so the lease runs out only after its job has stopped asking.
const (
	dnsWork      = time.Minute
	dnsLeaseTerm = 2 * dnsWork
)

// dnsLease is one DNS job's hold on a server. Only its holder asks the provider about the
// server's names, and every write the job makes after asking checks it still holds the lease.
type dnsLease struct{ server, holder uuid.UUID }

var errDNSLeaseLost = errors.New("the server's DNS lease ran out and another job took it")

// takeDNSLease takes the server's lease if it is free or has run out.
func (s *Service) takeDNSLease(ctx context.Context, server uuid.UUID) (dnsLease, bool, error) {
	lease := dnsLease{server: server, holder: uuid.New()}
	n, e := db.New(s.Store.Pool).TakeDNSLease(ctx, db.TakeDNSLeaseParams{ServerID: server, Holder: lease.holder, Seconds: dnsLeaseTerm.Seconds()})
	return lease, n == 1, e
}

// releaseDNSLease frees the lease for the next job. Should that fail, the lease runs out.
func (s *Service) releaseDNSLease(lease dnsLease) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if e := db.New(s.Store.Pool).ReleaseDNSLease(ctx, db.ReleaseDNSLeaseParams{ServerID: lease.server, Holder: lease.holder}); e != nil {
		slog.Warn("releasing a server's DNS lease failed; it runs out on its own", "server", lease.server, "error", e)
	}
}

// holdDNSLease locks the lease for the rest of the transaction, or refuses one whose lease was
// taken over. A job taking it over waits until the transaction ends.
func holdDNSLease(ctx context.Context, q *db.Queries, lease dnsLease) error {
	n, e := q.HoldDNSLease(ctx, db.HoldDNSLeaseParams{ServerID: lease.server, Holder: lease.holder})
	if e == nil && n != 1 {
		return errDNSLeaseLost
	}
	return e
}

// underDNSLease runs fn in a transaction that holds the lease, for a write made after the
// provider was asked.
func (s *Service) underDNSLease(ctx context.Context, lease dnsLease, fn func(*db.Queries) error) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	q := db.New(tx)
	if e = holdDNSLease(ctx, q, lease); e != nil {
		return e
	}
	if e = fn(q); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// withdrawReason says why a publication must go, or "" while it may stay. A server that's
// fenced for recovery keeps its records until an owner reviews them: it is still the
// workspace's server, and its approval only blocks changes.
func (s *Service) withdrawReason(ctx context.Context, p db.ListDNSPublicationsRow) (string, error) {
	if p.Revoked {
		return withdrawRevoked, nil
	}
	if p.Base != s.Store.ApplicationDomain {
		return withdrawBaseChanged, nil
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return "", e
	}
	e = s.Store.ManagementAdmission(ctx, tx, p.WorkspaceID)
	_ = tx.Rollback(ctx)
	if errors.Is(e, store.SubscriptionRequired) {
		return withdrawInactive, nil
	}
	return "", e
}

// withdrawStalePublications removes the server's records from every name it may no longer
// hold. The caller holds the server's DNS lease and nothing else. A row in a zone this
// installation no longer has credentials for is stranded and left for the operator.
func (s *Service) withdrawStalePublications(ctx context.Context, lease dnsLease) error {
	q := db.New(s.Store.Pool)
	publications, e := q.ListDNSPublications(ctx, lease.server)
	if e != nil || len(publications) == 0 {
		return e
	}
	installation, e := q.InstallationID(ctx)
	if e != nil {
		return e
	}
	var failures []error
	for _, p := range publications {
		if p.Zone != "" && p.Zone != s.DNS.Zone {
			continue
		}
		reason, e := s.withdrawReason(ctx, p)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if reason == "" {
			continue
		}
		name := dnsName(p.Base, lease.server)
		providerErr := s.DNS.Remove(ctx, name, dnsMarker(installation, lease.server), s.auditDeletion(ctx, p.WorkspaceID, lease.server, name, reason))
		failures = append(failures, providerErr, s.recordWithdrawal(ctx, lease, p, name, reason, providerErr == nil))
	}
	return errors.Join(failures...)
}

// recordWithdrawal forgets the publication once nothing of ours is left at the name, and
// confirms the deletions audited for it. A failed attempt keeps the row, so the sweep retries it.
func (s *Service) recordWithdrawal(ctx context.Context, lease dnsLease, p db.ListDNSPublicationsRow, name, reason string, complete bool) error {
	key := db.CheckDNSPublicationParams{ServerID: lease.server, Base: p.Base, Zone: p.Zone}
	return s.underDNSLease(ctx, lease, func(q *db.Queries) error {
		if !complete {
			return q.CheckDNSPublication(ctx, key)
		}
		if e := q.ConfirmDNSRecordRemovals(ctx, db.ConfirmDNSRecordRemovalsParams{ServerID: lease.server, Name: name, Zone: s.DNS.Zone, Kept: []string{}}); e != nil {
			return e
		}
		if e := q.DeleteDNSPublication(ctx, db.DeleteDNSPublicationParams(key)); e != nil {
			return e
		}
		if note := withdrawnNotes[reason]; note != "" {
			return q.NoteWithdrawnServerDomain(ctx, db.NoteWithdrawnServerDomainParams{Note: note, ServerID: lease.server, Base: p.Base})
		}
		return nil
	})
}

// auditDeletion records each deletion before it's sent, so the audit holds even when the
// provider's answer is lost. Each record is its own statement on the pool: nothing is held
// while the provider is called.
func (s *Service) auditDeletion(ctx context.Context, ws, server uuid.UUID, name, reason string) applicationdns.Deleting {
	return func(r applicationdns.Removed) error {
		slog.Info("removing an application DNS record", "server", server, "record", r.ID, "type", r.Type, "reason", reason)
		return db.New(s.Store.Pool).InsertDNSRecordRemoval(ctx, db.InsertDNSRecordRemovalParams{WorkspaceID: ws, ServerID: server, Name: name, Zone: s.DNS.Zone, RecordID: r.ID, RecordType: r.Type, Content: r.Content, Reason: reason})
	}
}

// confirmDeletions confirms the audited deletions at a name the provider was just reconciled
// for, except the records it still publishes there.
func (s *Service) confirmDeletions(ctx context.Context, q *db.Queries, server uuid.UUID, name string, records map[string]string) error {
	kept := []string{}
	for _, id := range records {
		kept = append(kept, id)
	}
	return q.ConfirmDNSRecordRemovals(ctx, db.ConfirmDNSRecordRemovalsParams{ServerID: server, Name: name, Zone: s.DNS.Zone, Kept: kept})
}

// recordPublication commits the name before the provider is asked to create anything there.
func (s *Service) recordPublication(ctx context.Context, q *db.Queries, ws, server uuid.UUID, base string) error {
	return q.RecordDNSPublication(ctx, db.RecordDNSPublicationParams{ServerID: server, WorkspaceID: ws, Base: base, Zone: s.DNS.Zone})
}

// enqueueStalePublications is the sweep's half of withdrawal: it finds every server holding a
// name it may no longer hold, including after a restart, a lapse or a change of base.
func (s *Service) enqueueStalePublications(ctx context.Context, tx pgx.Tx) error {
	q := db.New(tx)
	if e := q.MarkRevokedServersDNSPublications(ctx); e != nil {
		return e
	}
	rows, e := q.StaleDNSPublicationServers(ctx, db.StaleDNSPublicationServersParams{Zone: s.DNS.Zone, Base: s.Store.ApplicationDomain, Hosted: s.Store.Hosted})
	if e != nil {
		return e
	}
	for _, r := range rows {
		if e = s.enqueueDomain(ctx, tx, domainArgs{Server: r.ServerID, Generation: r.Generation, Controller: r.Controller}); e != nil {
			return e
		}
	}
	return nil
}

// WarnStrandedDomains reports publications this installation can no longer remove: DNS
// automation is off, or the records are in a zone it no longer has credentials for.
func (s *Service) WarnStrandedDomains(ctx context.Context) error {
	params := db.CountStrandedDNSPublicationsParams{Automatic: s.DNS != nil}
	if s.DNS != nil {
		params.Zone = s.DNS.Zone
	}
	n, e := db.New(s.Store.Pool).CountStrandedDNSPublications(ctx, params)
	if e == nil && n > 0 {
		slog.Warn("application DNS records may remain that this installation can't remove; see docs/hosting.md#removing-generated-records", "publications", n)
	}
	return e
}
