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
	"github.com/riverqueue/river"
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

// River ends a DNS job's context after dnsWork, which it starts before the job takes its lease.
// The lease lasts a little longer, for a request already sent when the context ended, so it
// runs out only after its job has stopped asking. A job paused for longer than that can still
// send a request late; recheckAfterLostLease deals with what it may have changed.
const (
	dnsWork      = time.Minute
	dnsLeaseTerm = dnsWork + 30*time.Second
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

// underDNSLease runs fn in a transaction that holds the server's DNS lock and then its lease,
// for a write made after the provider was asked. Revocation, recovery and configuration take
// the same lock first, so the job's writes and theirs never wait on each other's rows. A job
// whose lease was taken over writes nothing, and one taking it over waits until this commits.
func (s *Service) underDNSLease(ctx context.Context, lease dnsLease, fn func(pgx.Tx) error) error {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = store.LockServerDNS(ctx, tx, lease.server); e != nil {
		return e
	}
	held, e := db.New(tx).HoldDNSLease(ctx, db.HoldDNSLeaseParams{ServerID: lease.server, Holder: lease.holder})
	if e != nil {
		return e
	}
	if held != 1 {
		return errDNSLeaseLost
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// recheckAfterLostLease runs once a job finds its lease was taken over after it asked the
// provider. A paused job's request can reach the provider after the next job's, so the
// provider may hold records the database doesn't know of, or lack ones it does. The name the
// job may have published is recorded again, which keeps it for withdrawal if the server is no
// longer approved, and a job for the server as it is now is queued to converge the provider.
func (s *Service) recheckAfterLostLease(ctx context.Context, server uuid.UUID, published *db.DNSPublicationTargetRow) error {
	// The job's own deadline has often passed by now: that's how its lease ran out.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = store.LockServerDNS(ctx, tx, server); e != nil {
		return e
	}
	q := db.New(tx)
	if published != nil {
		if e = q.RecordLateDNSPublication(ctx, db.RecordLateDNSPublicationParams{Base: published.Base, Zone: s.DNS.Zone, ServerID: server}); e != nil {
			return e
		}
	}
	job, e := q.CurrentDNSJob(ctx, server)
	if e != nil {
		return e
	}
	// Not unique: a job with the same arguments may be running, holding the lease, and have
	// looked at the provider before this job's late request. This one starts after it.
	if _, e = s.Queue.InsertTx(ctx, tx, domainArgs{Server: server, Generation: job.Generation, Controller: job.Controller}, &river.InsertOpts{MaxAttempts: 10}); e != nil {
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
	return s.underDNSLease(ctx, lease, func(tx pgx.Tx) error {
		q := db.New(tx)
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
