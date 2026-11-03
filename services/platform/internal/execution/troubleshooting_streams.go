package execution

import (
	"context"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hosting"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// TroubleshootingStream is what both stream handlers need once a side is admitted.
type TroubleshootingStream struct {
	Workspace, Session uuid.UUID
	TicketExpires      time.Time
	Deadline           time.Time
}

func (s *Service) troubleshootingOffers(ctx context.Context, a Identity) ([]api.AgentTroubleshootingOffer, error) {
	rows, err := s.Store.Queries.ListTroubleshootingOffers(ctx, db.ListTroubleshootingOffersParams{WorkspaceID: a.Workspace, ServerID: a.ID})
	out := make([]api.AgentTroubleshootingOffer, 0, len(rows))
	for _, row := range rows {
		out = append(out, api.AgentTroubleshootingOffer{SessionId: row.ID, UnitId: row.UnitID, ApplicationId: row.ApplicationID, Instance: int(row.Instance), ControllerGeneration: row.ControllerGeneration, ActorId: row.ActorID})
	}
	return out, err
}

// AdmitTroubleshootingAgent admits the agent stream once, for a session offered to
// this server under its current controller generation.
func (s *Service) AdmitTroubleshootingAgent(ctx context.Context, a Identity, id uuid.UUID) (TroubleshootingStream, error) {
	row, err := s.Store.Queries.AdmitTroubleshootingAgent(ctx, db.AdmitTroubleshootingAgentParams{CredentialHash: hash(a.Token), WorkspaceID: a.Workspace, ID: id, ServerID: a.ID})
	if errors.Is(err, pgx.ErrNoRows) {
		return TroubleshootingStream{}, domain.NotFound
	}
	return TroubleshootingStream{Workspace: a.Workspace, Session: id, TicketExpires: row.TicketExpiresAt, Deadline: row.DeadlineAt}, err
}

// RedeemTroubleshootingTicket admits the browser stream once. Admission re-checks the
// workspace's write requirements, the role and any owners-only restriction, but not
// recent authentication, which applies only when a session opens.
func (s *Service) RedeemTroubleshootingTicket(ctx context.Context, p store.Principal, browser BrowserBinding, ws, id uuid.UUID, ticket string) (TroubleshootingStream, error) {
	out := TroubleshootingStream{Workspace: ws, Session: id}
	err := s.Store.InWorkspace(ctx, p, ws, store.WorkspaceAccess{Write: true}, func(q *db.Queries, w store.Workspace) error {
		row, err := q.RedeemTroubleshootingTicket(ctx, db.RedeemTroubleshootingTicketParams{WorkspaceID: ws, ID: id, ActorID: p.Account.ID, TicketHash: hash(ticket), BrowserSessionHash: hash(browser.Token), AuthVersion: p.Account.AuthVersion})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Forbidden
		}
		if err != nil {
			return err
		}
		policy, err := q.GetTroubleshootingPolicy(ctx, db.GetTroubleshootingPolicyParams{WorkspaceID: ws, EnvironmentID: row.EnvironmentID})
		if err != nil || !policy.Enabled || policy.OwnersOnly && w.Role != "owner" {
			return domain.Forbidden
		}
		out.TicketExpires, out.Deadline = row.TicketExpiresAt, row.DeadlineAt
		return nil
	})
	return out, err
}

// ActivateTroubleshootingSession marks a session active when both streams have
// attached, which is the moment the agent is told to start the shell.
func (s *Service) ActivateTroubleshootingSession(ctx context.Context, ws, id uuid.UUID) error {
	n, err := s.Store.Queries.ActivateTroubleshootingSession(ctx, db.ActivateTroubleshootingSessionParams{WorkspaceID: ws, ID: id})
	if err == nil && n != 1 {
		return domain.Conflict
	}
	return err
}

// ReviewTroubleshootingSession returns why an open session must end, or "" if it may
// continue. It re-reads every condition from PostgreSQL, because a change could come
// from any request or worker.
func (s *Service) ReviewTroubleshootingSession(ctx context.Context, ws, id uuid.UUID) (string, error) {
	row, err := s.Store.Queries.ReviewTroubleshootingSession(ctx, db.ReviewTroubleshootingSessionParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return "closed", nil
	}
	if err != nil {
		return "", err
	}
	if row.EndReason == "" && s.Store.Hosted {
		entitlement := hosting.Entitlement{Status: row.SubscriptionStatus.String, Until: row.SubscriptionUntil}
		if !entitlement.Active(time.Now()) {
			return "subscription expired", nil
		}
	}
	return row.EndReason, nil
}

// RefreshTroubleshootingBrowser keeps the browser session alive while the person types,
// since a terminal session sends no ordinary dashboard requests.
func (s *Service) RefreshTroubleshootingBrowser(ctx context.Context, ws, id uuid.UUID, idle time.Duration, deadline time.Time) error {
	return s.Store.Queries.RefreshTroubleshootingBrowser(ctx, db.RefreshTroubleshootingBrowserParams{IdleSeconds: idle.Seconds(), DeadlineAt: deadline, WorkspaceID: ws, ID: id})
}

// EndTroubleshootingSession records how a stream ended. A session already closed keeps
// its reason and audit event, and gains the byte counts only.
func (s *Service) EndTroubleshootingSession(ctx context.Context, ws, id uuid.UUID, reason string, input, output int64) error {
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		row, err := q.GetTroubleshootingSessionForEnd(ctx, db.GetTroubleshootingSessionForEndParams{WorkspaceID: ws, ID: id})
		if err != nil {
			return err
		}
		if err = q.EndTroubleshootingSession(ctx, db.EndTroubleshootingSessionParams{CloseReason: reason, InputBytes: input, OutputBytes: output, WorkspaceID: ws, ID: id}); err != nil {
			return err
		}
		if row.State == "closed" {
			return nil
		}
		return store.Audit(ctx, q, store.Principal{Account: db.Account{ID: row.ActorID}}, ws, "session.close", id)
	})
}

// CloseAbandonedTroubleshootingSessions runs at startup: the single instance holds no
// streams yet, so any session the database shows as open was left by a restart.
func (s *Service) CloseAbandonedTroubleshootingSessions(ctx context.Context) error {
	return s.Store.Transaction(ctx, func(q *db.Queries) error {
		rows, err := q.CloseAbandonedTroubleshootingSessions(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = store.Audit(ctx, q, store.Principal{Account: db.Account{ID: row.ActorID}}, row.WorkspaceID, "session.close", row.ID); err != nil {
				return err
			}
		}
		return nil
	})
}

type troubleshootingSweepArgs struct{}

func (troubleshootingSweepArgs) Kind() string { return "platform_troubleshooting_sweep_v1" }

type troubleshootingSweepWorker struct {
	river.WorkerDefaults[troubleshootingSweepArgs]
	service *Service
}

// Work closes offers nobody attached to and sessions past their deadline whose stream
// no longer exists. A live relay ends its session at the deadline itself first.
func (w *troubleshootingSweepWorker) Work(ctx context.Context, _ *river.Job[troubleshootingSweepArgs]) error {
	return w.service.Store.Transaction(ctx, func(q *db.Queries) error {
		rows, err := q.SweepTroubleshootingSessions(ctx)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if err = store.Audit(ctx, q, store.Principal{Account: db.Account{ID: row.ActorID}}, row.WorkspaceID, "session.close", row.ID); err != nil {
				return err
			}
		}
		return nil
	})
}
