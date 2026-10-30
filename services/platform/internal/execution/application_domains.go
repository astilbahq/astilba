package execution

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func (w *domainWorker) Timeout(*river.Job[domainArgs]) time.Duration { return time.Minute }
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
	v.Wildcard = "*." + domain.ServerDomain(base, id)
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
		out.Dns, e = s.domainConfiguration(ctx, tx, ws, id)
		if e != nil {
			return e
		}
		if out.Dns.State == "disabled" || out.Dns.State == "unconfigured" || out.Dns.State == "review_required" {
			return domain.Invalid("server.domain")
		}
		out.Hostname = domain.ApplicationHostname(s.Store.ApplicationDomain, ws, env, app, id)
		out.ServerId = id
		return nil
	})
	return out, e
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
	return tx.Commit(ctx)
}
func (s *Service) reconcileDomain(ctx context.Context, a domainArgs) error {
	if s.DNS == nil {
		return nil
	}
	conn, e := s.Store.Pool.Acquire(ctx)
	if e != nil {
		return e
	}
	defer conn.Release()
	var locked bool
	if e = conn.QueryRow(ctx, "SELECT pg_try_advisory_lock(hashtextextended($1,32))", a.Server.String()).Scan(&locked); e != nil {
		return e
	}
	if !locked {
		return river.JobSnooze(time.Second)
	}
	defer func() {
		unlock, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlock, "SELECT pg_advisory_unlock(hashtextextended($1,32))", a.Server.String()); err != nil {
			conn.Conn().Close(unlock)
		}
	}()
	var ws, installation uuid.UUID
	var base, ipv4, ipv6 string
	e = conn.QueryRow(ctx, `SELECT d.workspace_id,d.base,d.ipv4,d.ipv6,i.id FROM server_domains d JOIN servers s ON s.id=d.server_id CROSS JOIN dns_installation i WHERE d.server_id=$1 AND d.generation=$2 AND d.approved_controller=$3 AND s.controller_generation=$3 AND NOT s.revoked AND NOT s.awaiting_transfer AND d.base=$4`, a.Server, a.Generation, a.Controller, s.Store.ApplicationDomain).Scan(&ws, &base, &ipv4, &ipv6, &installation)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	// Hosted entitlement is checked without keeping a transaction open over DNS.
	tx, e := conn.Begin(ctx)
	if e != nil {
		return e
	}
	e = s.Store.ManagementAdmission(ctx, tx, ws)
	_ = tx.Rollback(ctx)
	if errors.Is(e, store.SubscriptionRequired) {
		return nil
	}
	if e != nil {
		return e
	}
	records, providerErr := s.DNS.Reconcile(ctx, "*."+domain.ServerDomain(base, a.Server), "astilba:"+installation.String()+":"+a.Server.String(), ipv4, ipv6)
	state, note := "applied", "Provider records observed. DNS propagation, TLS and application reachability are separate checks."
	if providerErr != nil {
		state, note = "failed", providerErr.Error()
	}
	raw, _ := json.Marshal(records)
	if records == nil {
		raw = []byte(`{}`)
	}
	_, e = conn.Exec(ctx, `UPDATE server_domains SET state=$4,note=$5,observed_at=clock_timestamp(),records=$6 WHERE server_id=$1 AND generation=$2 AND approved_controller=$3`, a.Server, a.Generation, a.Controller, state, note, raw)
	if e != nil {
		return e
	}
	return providerErr
}
