package execution

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// BrowserBinding ties a session to the browser session that opened it. Expires is
// that session's absolute expiry, which also bounds the terminal session.
type BrowserBinding struct {
	Token   string
	Expires time.Time
}

func (s *Service) TroubleshootingPolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (api.TroubleshootingPolicy, error) {
	out := api.TroubleshootingPolicy{Available: s.Troubleshooting}
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{}, func(q *db.Queries, _ store.Workspace) error {
		exists, err := q.TroubleshootingEnvironmentExists(ctx, db.TroubleshootingEnvironmentExistsParams{WorkspaceID: ws, ProjectID: pid, EnvironmentID: env})
		if err != nil {
			return err
		}
		if !exists {
			return domain.NotFound
		}
		row, err := q.GetTroubleshootingPolicy(ctx, db.GetTroubleshootingPolicyParams{WorkspaceID: ws, EnvironmentID: env})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		out.Enabled, out.OwnersOnly, out.Generation = row.Enabled, row.OwnersOnly, row.Generation
		return err
	})
	return out, err
}

// SaveTroubleshootingPolicy requires recent authentication only for changes that
// widen access. Narrowing it ends the sessions the new setting no longer allows.
func (s *Service) SaveTroubleshootingPolicy(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, in api.SaveTroubleshootingPolicy) (api.TroubleshootingPolicy, error) {
	out := api.TroubleshootingPolicy{Available: s.Troubleshooting}
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Write: true, Owner: true}, func(q *db.Queries, _ store.Workspace) error {
		exists, err := q.TroubleshootingEnvironmentExists(ctx, db.TroubleshootingEnvironmentExistsParams{WorkspaceID: ws, ProjectID: pid, EnvironmentID: env})
		if err != nil {
			return err
		}
		if !exists {
			return domain.NotFound
		}
		old, err := q.GetTroubleshootingPolicy(ctx, db.GetTroubleshootingPolicyParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if old.Generation != in.Generation {
			return domain.Conflict
		}
		widened := in.Enabled && (!old.Enabled || old.OwnersOnly && !in.OwnersOnly)
		if widened && !p.Recent {
			return domain.RecentRequired
		}
		next := old.Generation + 1
		if err = q.SaveTroubleshootingPolicy(ctx, db.SaveTroubleshootingPolicyParams{WorkspaceID: ws, EnvironmentID: env, Enabled: in.Enabled, OwnersOnly: in.OwnersOnly, Generation: next, UpdatedBy: p.Account.ID}); err != nil {
			return err
		}
		if !in.Enabled || in.OwnersOnly {
			ended, err := q.CloseEnvironmentTroubleshootingSessions(ctx, db.CloseEnvironmentTroubleshootingSessionsParams{CloseReason: "environment setting changed", WorkspaceID: ws, EnvironmentID: env, OnlyNonOwners: in.Enabled})
			if err != nil {
				return err
			}
			for _, id := range ended {
				if err = store.Audit(ctx, q, p, ws, "session.close", id); err != nil {
					return err
				}
			}
		}
		out.Enabled, out.OwnersOnly, out.Generation = in.Enabled, in.OwnersOnly, next
		return store.Audit(ctx, q, p, ws, "troubleshooting.policy", env)
	})
	return out, err
}

// OpenTroubleshootingSession admits one session. The row and its audit event commit
// together, so a session never opens unaudited. No shell starts until both the agent
// and the browser have attached.
func (s *Service) OpenTroubleshootingSession(ctx context.Context, p store.Principal, browser BrowserBinding, ws, pid, env uuid.UUID, in api.OpenTroubleshootingSession) (api.OpenedTroubleshootingSession, error) {
	var out api.OpenedTroubleshootingSession
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Write: true, Recent: true}, func(q *db.Queries, w store.Workspace) error {
		if !s.Troubleshooting {
			return domain.TroubleshootingRefused(domain.TroubleshootingInstallation)
		}
		row, err := q.TroubleshootingEnvironment(ctx, db.TroubleshootingEnvironmentParams{WorkspaceID: ws, ProjectID: pid, EnvironmentID: env})
		if err != nil {
			return err
		}
		policy, err := q.GetTroubleshootingPolicy(ctx, db.GetTroubleshootingPolicyParams{WorkspaceID: ws, EnvironmentID: env})
		if errors.Is(err, pgx.ErrNoRows) || err == nil && !policy.Enabled {
			return domain.TroubleshootingRefused(domain.TroubleshootingEnvironment)
		}
		if err != nil {
			return err
		}
		if policy.OwnersOnly && w.Role != "owner" {
			return domain.Forbidden
		}
		if row.AppliedRevisionID == nil || row.PrimaryServerID == nil {
			return domain.TroubleshootingRefused(domain.TroubleshootingNotDeployed)
		}
		spec, _, err := domain.ParseSpec(row.Specification)
		if err != nil {
			return err
		}
		server, err := troubleshootingServer(spec, *row.PrimaryServerID, in)
		if err != nil {
			return err
		}
		unit, err := q.TroubleshootingUnit(ctx, db.TroubleshootingUnitParams{WorkspaceID: ws, EnvironmentID: env, ServerID: server})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.TroubleshootingRefused(domain.TroubleshootingNotDeployed)
		}
		if err != nil {
			return err
		}
		if err = troubleshootingCapability(unit.Capabilities); err != nil {
			return err
		}
		if err = q.LockTroubleshootingAdmission(ctx); err != nil {
			return err
		}
		expired, err := q.ExpireTroubleshootingSessions(ctx, ws)
		if err != nil {
			return err
		}
		for _, session := range expired {
			if err = store.Audit(ctx, q, store.Principal{Account: db.Account{ID: session.ActorID}}, ws, "session.close", session.ID); err != nil {
				return err
			}
		}
		counts, err := q.TroubleshootingAdmissionCounts(ctx, db.TroubleshootingAdmissionCountsParams{WorkspaceID: ws, ActorID: p.Account.ID, ServerID: server, EnvironmentID: env, ApplicationID: in.ApplicationId, Instance: int32(in.Instance)})
		if err != nil {
			return err
		}
		if counts.Target > 0 || counts.Actor >= domain.TroubleshootingPerActor || counts.Server >= domain.TroubleshootingPerHost || counts.Workspace >= domain.TroubleshootingPerWorkspace || counts.Installation >= domain.TroubleshootingPerInstallation {
			return domain.TroubleshootingRefused(domain.TroubleshootingLimit)
		}
		now := time.Now().UTC()
		deadline := now.Add(domain.TroubleshootingMaxDuration)
		if browser.Expires.Before(deadline) {
			deadline = browser.Expires
		}
		ticket := rand.Text()
		session, err := q.InsertTroubleshootingSession(ctx, db.InsertTroubleshootingSessionParams{ID: uuid.New(), WorkspaceID: ws, EnvironmentID: env, UnitID: unit.UnitID, ServerID: server, ApplicationID: in.ApplicationId, Instance: int32(in.Instance), ActorID: p.Account.ID, AuthVersion: p.Account.AuthVersion, BrowserSessionHash: hash(browser.Token), ControllerGeneration: unit.ControllerGeneration, TicketHash: hash(ticket), TicketExpiresAt: now.Add(domain.TroubleshootingTicketLifetime), DeadlineAt: deadline})
		if err != nil {
			return err
		}
		out = api.OpenedTroubleshootingSession{Session: troubleshootingSessionView(db.ListTroubleshootingSessionsRow(session)), Ticket: ticket}
		return store.Audit(ctx, q, p, ws, "session.open", session.ID)
	})
	if err != nil {
		s.auditTroubleshootingRefusal(ctx, p, ws, env, err)
	}
	return out, err
}

// troubleshootingServer finds the host of the requested instance in the applied
// revision: its placement target for a cross-host application, or the primary unit.
func troubleshootingServer(spec domain.Specification, primary uuid.UUID, in api.OpenTroubleshootingSession) (uuid.UUID, error) {
	var app *domain.Application
	for i := range spec.Applications {
		if spec.Applications[i].ID == in.ApplicationId {
			app = &spec.Applications[i]
		}
	}
	if app == nil {
		return uuid.Nil, domain.TroubleshootingRefused(domain.TroubleshootingNotDeployed)
	}
	if spec.Placement != nil {
		for _, group := range spec.Placement.Applications {
			if group.ApplicationID != app.ID {
				continue
			}
			for _, target := range group.Targets {
				if in.ServerId != nil && *in.ServerId == target.ServerID && in.Instance <= target.Count {
					return target.ServerID, nil
				}
			}
			return uuid.Nil, domain.Invalid("target")
		}
	}
	if in.ServerId != nil && *in.ServerId != primary || in.Instance > app.ReplicaCount() {
		return uuid.Nil, domain.Invalid("target")
	}
	return primary, nil
}

func troubleshootingCapability(raw []byte) error {
	var capabilities api.AgentCapabilities
	if err := json.Unmarshal(raw, &capabilities); err != nil {
		return err
	}
	if capabilities.TroubleshootingUnavailable != nil {
		if *capabilities.TroubleshootingUnavailable == api.Runtime {
			return domain.TroubleshootingRefused(domain.TroubleshootingRuntime)
		}
		return domain.TroubleshootingRefused(domain.TroubleshootingHostNotReady)
	}
	if capabilities.Troubleshooting == nil || !*capabilities.Troubleshooting {
		return domain.TroubleshootingRefused(domain.TroubleshootingAgentUpdate)
	}
	if capabilities.TroubleshootingEnabled == nil || !*capabilities.TroubleshootingEnabled {
		return domain.TroubleshootingRefused(domain.TroubleshootingHostOptIn)
	}
	return nil
}

// auditTroubleshootingRefusal records a refused attempt by a workspace member in its
// own transaction, because the refusal rolled back the admission transaction. A prompt
// for recent authentication is not a refusal, and a missing resource reveals nothing.
// Repeats are coalesced per hour, so a member cannot flood the workspace's audit log
// and push it past the export limit.
func (s *Service) auditTroubleshootingRefusal(ctx context.Context, p store.Principal, ws, env uuid.UUID, refusal error) {
	var refused *domain.Error
	if !errors.As(refusal, &refused) || refused.Code == domain.RecentRequired.Code || refused.Code == domain.NotFound.Code {
		return
	}
	reason := refused.Code
	if reason == "troubleshooting_refused" {
		reason = refused.Fields[0]
	}
	action := "session.refused." + reason
	_ = s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{}, func(q *db.Queries, _ store.Workspace) error {
		recent, err := q.RecentlyAudited(ctx, db.RecentlyAuditedParams{WorkspaceID: ws, ActorID: p.Account.ID, Action: action})
		if err != nil || recent {
			return err
		}
		return store.Audit(ctx, q, p, ws, action, env)
	})
}

func (s *Service) OpenTroubleshootingSessions(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.TroubleshootingSession, error) {
	out := []api.TroubleshootingSession{}
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{}, func(q *db.Queries, w store.Workspace) error {
		if !domain.CanWrite(w.Role) {
			return domain.Forbidden
		}
		exists, err := q.TroubleshootingEnvironmentExists(ctx, db.TroubleshootingEnvironmentExistsParams{WorkspaceID: ws, ProjectID: pid, EnvironmentID: env})
		if err != nil {
			return err
		}
		if !exists {
			return domain.NotFound
		}
		rows, err := q.ListOpenTroubleshootingSessions(ctx, db.ListOpenTroubleshootingSessionsParams{WorkspaceID: ws, EnvironmentID: env})
		for _, row := range rows {
			out = append(out, troubleshootingSessionView(db.ListTroubleshootingSessionsRow(row)))
		}
		return err
	})
	return out, err
}

func (s *Service) TroubleshootingSessionHistory(ctx context.Context, p store.Principal, ws uuid.UUID) ([]api.TroubleshootingSession, error) {
	out := []api.TroubleshootingSession{}
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Owner: true}, func(q *db.Queries, _ store.Workspace) error {
		rows, err := q.ListTroubleshootingSessions(ctx, ws)
		for _, row := range rows {
			out = append(out, troubleshootingSessionView(row))
		}
		return err
	})
	return out, err
}

// CloseTroubleshootingSession lets the person who opened a session, or an owner, end it.
func (s *Service) CloseTroubleshootingSession(ctx context.Context, p store.Principal, ws, id uuid.UUID) error {
	return s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Write: true}, func(q *db.Queries, w store.Workspace) error {
		actor, err := q.GetTroubleshootingSessionActor(ctx, db.GetTroubleshootingSessionActorParams{WorkspaceID: ws, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		}
		if err != nil {
			return err
		}
		reason := "closed by owner"
		if actor == p.Account.ID {
			reason = "closed"
		} else if w.Role != "owner" {
			return domain.Forbidden
		}
		if err = q.CloseTroubleshootingSession(ctx, db.CloseTroubleshootingSessionParams{CloseReason: reason, WorkspaceID: ws, ID: id}); err != nil {
			return err
		}
		return store.Audit(ctx, q, p, ws, "session.close", id)
	})
}

func (s *Service) CloseAllTroubleshootingSessions(ctx context.Context, p store.Principal, ws uuid.UUID) error {
	return s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Write: true, Owner: true}, func(q *db.Queries, _ store.Workspace) error {
		ended, err := q.CloseWorkspaceTroubleshootingSessions(ctx, db.CloseWorkspaceTroubleshootingSessionsParams{CloseReason: "closed by owner", WorkspaceID: ws})
		if err != nil {
			return err
		}
		for _, session := range ended {
			if err = store.Audit(ctx, q, p, ws, "session.close", session.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

func troubleshootingSessionView(row db.ListTroubleshootingSessionsRow) api.TroubleshootingSession {
	out := api.TroubleshootingSession{Id: row.ID, EnvironmentId: row.EnvironmentID, ServerId: row.ServerID, ApplicationId: row.ApplicationID, Instance: int(row.Instance), ActorId: row.ActorID, State: api.TroubleshootingSessionState(row.State), CreatedAt: row.CreatedAt, DeadlineAt: row.DeadlineAt, ClosedAt: row.ClosedAt}
	if row.CloseReason.Valid {
		out.CloseReason = &row.CloseReason.String
	}
	return out
}
