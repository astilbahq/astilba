package execution

import (
	"context"
	"errors"
	"net"
	"sort"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"
)

type domainArgs struct {
	Server     uuid.UUID `json:"server"`
	Generation int64     `json:"generation"`
	Controller int64     `json:"controller"`
}

func (domainArgs) Kind() string { return "platform_dns_v1" }

type domainWorker struct {
	river.WorkerDefaults[domainArgs]
	service *Service
}

func (w *domainWorker) Timeout(*river.Job[domainArgs]) time.Duration { return dnsWork }

// NextRetry keeps retries within the sweep's one-minute cadence. River's default backoff
// would leave a revoked server's records published for hours after a short provider outage.
func (w *domainWorker) NextRetry(job *river.Job[domainArgs]) time.Time {
	return time.Now().Add(min(time.Duration(1)<<min(job.Attempt, 6)*time.Second, time.Minute))
}
func (w *domainWorker) Work(ctx context.Context, job *river.Job[domainArgs]) error {
	return w.service.reconcileDomain(ctx, job.Args)
}
func (s *Service) enqueueDomain(ctx context.Context, tx pgx.Tx, a domainArgs) error {
	_, e := s.Queue.InsertTx(ctx, tx, a, &river.InsertOpts{MaxAttempts: 10, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRunning, rivertype.JobStateRetryable, rivertype.JobStateScheduled}}})
	return e
}
func (s *Service) domainConfiguration(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (api.ServerDomain, error) {
	base := s.Store.ApplicationDomain
	v := api.ServerDomain{ServerId: id, Base: base, State: "unconfigured", AutomaticDNS: s.DNS != nil}
	var revoked, transfer bool
	var role string
	var controller int64
	if e := tx.QueryRow(ctx, "SELECT role,revoked,awaiting_transfer,controller_generation FROM servers WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&role, &revoked, &transfer, &controller); e != nil {
		return v, e
	}
	if role == "build" || revoked {
		return v, domain.Invalid("server")
	}
	if base == "" {
		v.State = "disabled"
		v.Note = "Generated names are disabled by the installation operator."
		return v, nil
	}
	v.Wildcard = dnsName(base, id)
	var approved *int64
	var storedBase string
	e := tx.QueryRow(ctx, "SELECT base,ipv4,ipv6,generation,approved_controller,state,observed_at,note FROM server_domains WHERE workspace_id=$1 AND server_id=$2", ws, id).Scan(&storedBase, &v.Ipv4, &v.Ipv6, &v.Generation, &approved, &v.State, &v.ObservedAt, &v.Note)
	if errors.Is(e, pgx.ErrNoRows) {
		return v, nil
	}
	if e == nil && (storedBase != base || approved == nil || *approved != controller || transfer) {
		v.State = "review_required"
		v.Note = "An owner must review and approve these addresses before DNS automation can resume."
	}
	return v, e
}
func (s *Service) ServerDomain(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.ServerDomain, error) {
	var out api.ServerDomain
	e := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error { var e error; out, e = s.domainConfiguration(ctx, tx, ws, id); return e })
	return out, e
}
func (s *Service) ConfigureServerDomain(ctx context.Context, p store.Principal, ws, id uuid.UUID, in api.ConfigureServerDomain) (api.ServerDomain, error) {
	var out api.ServerDomain
	ipv4, e := domain.PublicAddress(in.Ipv4, true)
	if e != nil {
		return out, e
	}
	ipv6, e := domain.PublicAddress(in.Ipv6, false)
	if e != nil {
		return out, e
	}
	if ipv4 == "" && ipv6 == "" {
		return out, domain.Invalid("publicAddress")
	}
	e = s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "server.domain.configure", id, func(tx pgx.Tx) error {
		if e := store.LockServerDNS(ctx, tx, id); e != nil {
			return e
		}
		current, e := s.domainConfiguration(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		if current.State == "disabled" {
			return domain.Invalid("applicationDomain")
		}
		if current.Generation != in.BaseGeneration {
			return domain.Conflict
		}
		var controller int64
		var transfer bool
		if e = tx.QueryRow(ctx, "SELECT controller_generation,awaiting_transfer FROM servers WHERE id=$1", id).Scan(&controller, &transfer); e != nil {
			return e
		}
		if transfer {
			return domain.Conflict
		}
		state := "manual"
		if s.DNS != nil {
			state = "pending"
		}
		var generation int64
		e = tx.QueryRow(ctx, `INSERT INTO server_domains(server_id,workspace_id,base,ipv4,ipv6,approved_controller,state) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(server_id) DO UPDATE SET base=$3,ipv4=$4,ipv6=$5,generation=server_domains.generation+1,approved_controller=$6,state=$7,observed_at=NULL,note='' RETURNING generation`, id, ws, current.Base, ipv4, ipv6, controller, state).Scan(&generation)
		if e != nil {
			return e
		}
		if s.DNS != nil {
			if e = s.enqueueDomain(ctx, tx, domainArgs{id, generation, controller}); e != nil {
				return e
			}
		}
		out, e = s.domainConfiguration(ctx, tx, ws, id)
		return e
	})
	return out, e
}
func (s *Service) SuggestDomain(ctx context.Context, p store.Principal, ws, pid, env, app, id uuid.UUID) (api.DomainSuggestion, error) {
	var out api.DomainSuggestion
	e := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		v, e := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if e != nil {
			return e
		}
		found := false
		for _, a := range v.Spec.Applications {
			if a.ID == app && a.ServiceKind() == "web" {
				found = true
			}
		}
		if !found {
			return domain.NotFound
		}
		if v.Server != nil && *v.Server != id {
			return domain.Conflict
		}
		out.Hostname, out.Dns, e = s.generatedHostname(ctx, tx, ws, env, app, id)
		out.ServerId = id
		return e
	})
	return out, e
}

// generatedHostname is the generated application domain of the app on the server, which must
// be the environment's server; the caller checks that. Its addresses must be in use.
func (s *Service) generatedHostname(ctx context.Context, tx pgx.Tx, ws, env, app, server uuid.UUID) (string, api.ServerDomain, error) {
	config, e := s.domainConfiguration(ctx, tx, ws, server)
	if e != nil {
		return "", config, e
	}
	if config.State == "disabled" || config.State == "unconfigured" || config.State == "review_required" {
		return "", config, domain.Invalid("server.domain")
	}
	return domain.ApplicationHostname(s.Store.ApplicationDomain, ws, env, app, server), config, nil
}
func (s *Service) CheckDomain(ctx context.Context, p store.Principal, ws, id uuid.UUID, hostname string) (api.DomainCheck, error) {
	out := api.DomainCheck{Hostname: hostname, Addresses: []string{}, State: "unverified", CheckedAt: time.Now().UTC(), Note: "DNS does not establish domain ownership, certificate readiness or application reachability. CDN/proxy addresses cannot be verified against this server."}
	if !domain.ValidHostname(hostname) {
		return out, domain.Invalid("hostname")
	}
	profile, e := s.ServerDomain(ctx, p, ws, id)
	if e != nil {
		return out, e
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	resolver := s.DNSResolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addresses, e := resolver.LookupNetIP(ctx, "ip", hostname)
	if e != nil || len(addresses) > 100 {
		out.State = "unavailable"
		return out, nil
	}
	matches := len(addresses) > 0 && (profile.Ipv4 != "" || profile.Ipv6 != "")
	for _, a := range addresses {
		value := a.Unmap().String()
		out.Addresses = append(out.Addresses, value)
		if value != profile.Ipv4 && value != profile.Ipv6 {
			matches = false
		}
	}
	sort.Strings(out.Addresses)
	if matches {
		out.State = "matches"
	}
	return out, nil
}

// SweepDomains enqueues a bounded batch; recovery-fenced profiles remain dormant
// even after the server's transfer has completed.
func (s *Service) SweepDomains(ctx context.Context) error {
	if s.DNS == nil || s.Store.ApplicationDomain == "" {
		return nil
	}
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	rows, e := tx.Query(ctx, `SELECT d.server_id,d.generation,d.approved_controller FROM server_domains d JOIN servers s ON s.id=d.server_id WHERE d.base=$1 AND d.approved_controller=s.controller_generation AND NOT s.revoked AND NOT s.awaiting_transfer AND (d.observed_at IS NULL OR d.observed_at<now()-interval '15 minutes') AND (NOT $2::boolean OR EXISTS (SELECT 1 FROM hosted_subscriptions h WHERE h.workspace_id=d.workspace_id AND h.management_until>now())) ORDER BY d.observed_at NULLS FIRST,d.server_id LIMIT 100`, s.Store.ApplicationDomain, s.Store.Hosted)
	if e != nil {
		return e
	}
	args := []domainArgs{}
	for rows.Next() {
		var a domainArgs
		if e = rows.Scan(&a.Server, &a.Generation, &a.Controller); e != nil {
			rows.Close()
			return e
		}
		args = append(args, a)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, a := range args {
		if e = s.enqueueDomain(ctx, tx, a); e != nil {
			return e
		}
	}
	if e = s.enqueueStalePublications(ctx, tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}

// reconcileDomain converges the provider on what the database says now, holding the server's
// DNS lease rather than a transaction or connection while it asks the provider.
func (s *Service) reconcileDomain(ctx context.Context, a domainArgs) error {
	if s.DNS == nil {
		return nil
	}
	lease, taken, e := s.takeDNSLease(ctx, a.Server)
	if e != nil {
		return e
	}
	if !taken {
		return river.JobSnooze(time.Second)
	}
	defer s.releaseDNSLease(lease)
	ctx, cancel := context.WithTimeout(ctx, dnsWork)
	defer cancel()
	// Withdrawal converges on what the database says now, whichever job runs it.
	withdrawn := s.withdrawStalePublications(ctx, lease)
	target, e := s.claimPublication(ctx, lease, a)
	if e != nil || target == nil {
		return errors.Join(withdrawn, e)
	}
	name := dnsName(target.Base, a.Server)
	records, providerErr := s.DNS.Reconcile(ctx, name, dnsMarker(target.InstallationID, a.Server), target.Ipv4, target.Ipv6, s.auditDeletion(ctx, target.WorkspaceID, a.Server, name, withdrawAddress))
	state, note := "applied", "Provider records observed. DNS propagation, TLS and application reachability are separate checks."
	if providerErr != nil {
		state, note = "failed", providerErr.Error()
	}
	raw, _ := stablejson.Marshal(records)
	if records == nil {
		raw = []byte(`{}`)
	}
	e = s.underDNSLease(ctx, lease, func(q *db.Queries) error {
		if providerErr == nil {
			if e := s.confirmDeletions(ctx, q, a.Server, name, records); e != nil {
				return e
			}
		}
		return q.ObserveServerDomain(ctx, db.ObserveServerDomainParams{State: state, Note: note, Records: raw, ServerID: a.Server, Generation: a.Generation, Controller: pgtype.Int8{Int64: a.Controller, Valid: true}})
	})
	return errors.Join(withdrawn, providerErr, e)
}

// claimPublication returns the addresses the job may publish, recording the name first, or nil
// when it may publish none. It takes the server's DNS lock, which revocation, recovery and
// configuration also take, so none of them commits between the check and the record: recording
// a publication ends a revocation's withdrawal.
func (s *Service) claimPublication(ctx context.Context, lease dnsLease, a domainArgs) (*db.DNSPublicationTargetRow, error) {
	tx, e := s.Store.Pool.Begin(ctx)
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(ctx)
	if e = store.LockServerDNS(ctx, tx, a.Server); e != nil {
		return nil, e
	}
	q := db.New(tx)
	if e = holdDNSLease(ctx, q, lease); e != nil {
		return nil, e
	}
	target, e := q.DNSPublicationTarget(ctx, db.DNSPublicationTargetParams{ServerID: a.Server, Generation: a.Generation, Controller: pgtype.Int8{Int64: a.Controller, Valid: true}, Base: s.Store.ApplicationDomain})
	if errors.Is(e, pgx.ErrNoRows) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	e = s.Store.ManagementAdmission(ctx, tx, target.WorkspaceID)
	if errors.Is(e, store.SubscriptionRequired) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if e = s.recordPublication(ctx, q, target.WorkspaceID, a.Server, target.Base); e != nil {
		return nil, e
	}
	return &target, tx.Commit(ctx)
}
