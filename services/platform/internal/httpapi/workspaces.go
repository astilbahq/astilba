package httpapi

import (
	"context"
	"net/http"
	"net/url"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/randtoken"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func workspace(w store.Workspace) api.Workspace {
	return api.Workspace{Id: w.ID, Name: w.Name, Role: api.Role(w.Role), RequireMfa: w.RequireMFA}
}
func (s *Server) scope(w http.ResponseWriter, r *http.Request, id uuid.UUID, access store.WorkspaceAccess, fn func(*db.Queries, store.Workspace) (any, error)) {
	s.admitted(w, r, s.Store.InWorkspaceTx, id, access, func(_ pgx.Tx, q *db.Queries, ws store.Workspace) (any, error) { return fn(q, ws) })
}

// revocationScope admits an action that only takes access away in any entitlement state. An
// action that may also widen access calls store.ManagementAdmission with tx when it does.
func (s *Server) revocationScope(w http.ResponseWriter, r *http.Request, id uuid.UUID, access store.WorkspaceAccess, fn func(pgx.Tx, *db.Queries, store.Workspace) (any, error)) {
	s.admitted(w, r, s.Store.InWorkspaceRevocation, id, access, fn)
}

type workspaceAdmission func(context.Context, store.Principal, uuid.UUID, store.WorkspaceAccess, func(pgx.Tx, *db.Queries, store.Workspace) error) error

func (s *Server) admitted(w http.ResponseWriter, r *http.Request, admit workspaceAdmission, id uuid.UUID, access store.WorkspaceAccess, fn func(pgx.Tx, *db.Queries, store.Workspace) (any, error)) {
	var out any
	e := admit(r.Context(), *principal(r.Context()), id, access, func(tx pgx.Tx, q *db.Queries, ws store.Workspace) error {
		var err error
		out, err = fn(tx, q, ws)
		return err
	})
	if e != nil {
		s.fail(w, r, e)
		return
	}
	if out == nil {
		w.WriteHeader(204)
	} else {
		status := http.StatusOK
		if r.Method == http.MethodPost {
			status = http.StatusCreated
		}
		s.respond(w, status, out)
	}
}
func (s *Server) CreateWorkspace(w http.ResponseWriter, r *http.Request) {
	b, e := decode[api.CreateWorkspace](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	name, e := domain.Name(b.Name)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	p := *principal(r.Context())
	if s.Store.Hosted {
		_, id, e := s.Auth.AcceptHosted(r.Context(), value(b.Invitation), "", "", name, s.clientSource(r), &p)
		if e != nil {
			s.fail(w, r, e)
			return
		}
		s.respond(w, 201, api.Workspace{Id: id, Name: name, Role: api.Owner})
		return
	}
	id := uuid.New()
	e = s.Store.Transaction(r.Context(), func(q *db.Queries) error {
		if e := q.InsertWorkspace(r.Context(), db.InsertWorkspaceParams{ID: id, Name: name}); e != nil {
			return e
		}
		if e := q.InsertMembership(r.Context(), db.InsertMembershipParams{WorkspaceID: id, AccountID: p.Account.ID, Role: "owner"}); e != nil {
			return e
		}
		return store.Audit(r.Context(), q, p, id, "workspace.create", id)
	})
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 201, api.Workspace{Id: id, Name: name, Role: api.Owner})
}
func (s *Server) GetWorkspace(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.scope(w, r, id, store.WorkspaceAccess{}, func(_ *db.Queries, ws store.Workspace) (any, error) { return workspace(ws), nil })
}
func (s *Server) UpdateWorkspace(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	b, e := decode[api.WorkspaceSettings](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	name, e := domain.Name(b.Name)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.revocationScope(w, r, id, store.WorkspaceAccess{Owner: true, Credential: true}, func(tx pgx.Tx, q *db.Queries, ws store.Workspace) (any, error) {
		if b.RequireMfa && !principal(r.Context()).MFA {
			return nil, domain.MFARequired
		}
		// Requiring two-factor authentication only takes access away; renaming the workspace or
		// dropping the requirement is management.
		if name != ws.Name || ws.RequireMFA && !b.RequireMfa {
			if e := s.Store.ManagementAdmission(r.Context(), tx, id); e != nil {
				return nil, e
			}
		}
		if e := q.SetWorkspaceSettings(r.Context(), db.SetWorkspaceSettingsParams{ID: id, Name: name, RequireMfa: b.RequireMfa}); e != nil {
			return nil, e
		}
		ws.Name = name
		ws.RequireMFA = b.RequireMfa
		return workspace(ws), store.Audit(r.Context(), q, *principal(r.Context()), id, "workspace.settings", id)
	})
}
func (s *Server) ListMembers(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.scope(w, r, id, store.WorkspaceAccess{Owner: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListMembers(r.Context(), id)
		out := []api.Member{}
		for _, v := range rows {
			out = append(out, api.Member{Id: v.ID, Email: openapi_types.Email(v.Email), Name: v.Name, Role: api.Role(v.Role), MfaEnabled: v.MfaEnabled})
		}
		return out, e
	})
}
func (s *Server) member(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID, role *api.Role) {
	// Removal and demotion stay possible on a lapsed workspace, so an owner can always cut a member
	// off; promotion grants access, so it needs active management.
	s.revocationScope(w, r, ws, store.WorkspaceAccess{Owner: true, Credential: true}, func(tx pgx.Tx, q *db.Queries, _ store.Workspace) (any, error) {
		ctx := r.Context()
		m, e := q.GetMembership(ctx, db.GetMembershipParams{WorkspaceID: ws, AccountID: account})
		if e != nil {
			return nil, e
		}
		to := ""
		if role != nil {
			to = string(*role)
		}
		if domain.Widens(m.Role, to) {
			if e = s.Store.ManagementAdmission(ctx, tx, ws); e != nil {
				return nil, e
			}
		}
		if m.Role == "owner" && (role == nil || *role != api.Owner) {
			n, e := q.OwnerCount(ctx, ws)
			if e != nil {
				return nil, e
			}
			if n <= 1 {
				return nil, &domain.Error{Code: "last_owner", Message: "Keep at least one workspace owner."}
			}
		}
		if role == nil {
			e = q.DeleteMembership(ctx, db.DeleteMembershipParams{WorkspaceID: ws, AccountID: account})
		} else {
			e = q.UpdateMembership(ctx, db.UpdateMembershipParams{WorkspaceID: ws, AccountID: account, Role: to})
		}
		if e != nil {
			return nil, e
		}
		if e = store.Audit(ctx, q, *principal(ctx), ws, "membership.change", account); e != nil {
			return nil, e
		}
		return nil, store.EndAuthority(ctx, q, *principal(ctx), ws, account, m.Role, to)
	})
}
func (s *Server) UpdateMember(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	b, e := decode[api.RoleChange](r)
	if e != nil || !domain.ValidRole(string(b.Role)) {
		s.fail(w, r, domain.Invalid("role"))
		return
	}
	s.member(w, r, ws, id, &b.Role)
}
func (s *Server) RemoveMember(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	s.member(w, r, ws, id, nil)
}
func (s *Server) ListInvitations(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.scope(w, r, id, store.WorkspaceAccess{Owner: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListInvitations(r.Context(), id)
		out := []api.Invitation{}
		for _, v := range rows {
			out = append(out, api.Invitation{Id: v.ID, Email: openapi_types.Email(v.Email), Role: api.Role(v.Role), ExpiresAt: v.ExpiresAt})
		}
		return out, e
	})
}
func (s *Server) InviteMember(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.InvitationRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	email, e := domain.Email(string(b.Email))
	if e != nil || !domain.ValidRole(string(b.Role)) {
		s.fail(w, r, domain.Invalid("invitation"))
		return
	}
	token, e := randtoken.New()
	if e != nil {
		s.fail(w, r, e)
		return
	}
	id := uuid.New()
	expiry := s.Auth.Now().Add(24 * time.Hour)
	e = s.Store.InWorkspace(r.Context(), *principal(r.Context()), ws, store.WorkspaceAccess{Write: true, Owner: true, Credential: true}, func(q *db.Queries, _ store.Workspace) error {
		if e := q.InsertInvitation(r.Context(), db.InsertInvitationParams{WorkspaceID: ws, ID: id, Email: email, Role: string(b.Role), TokenHash: auth.Hash(token), ExpiresAt: expiry, IssuerID: principal(r.Context()).Account.ID, IssuerAuthVersion: principal(r.Context()).Account.AuthVersion}); e != nil {
			return e
		}
		return store.Audit(r.Context(), q, *principal(r.Context()), ws, "invitation.create", id)
	})
	if e == nil {
		e = s.Auth.Mail.Send(r.Context(), email, "Join an Astilba workspace", "Open this one-use link to join the Astilba workspace within 24 hours:\n\n"+s.Origin+"/invitation?"+url.Values{"token": {token}}.Encode())
		if e != nil {
			undo, cancel := auth.Detached(r.Context())
			defer cancel()
			_ = s.Store.Queries.DeleteInvitation(undo, db.DeleteInvitationParams{WorkspaceID: ws, ID: id})
		}
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 201, api.Invitation{Id: id, Email: openapi_types.Email(email), Role: b.Role, ExpiresAt: expiry})
}
func (s *Server) RevokeInvitation(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	s.revocationScope(w, r, ws, store.WorkspaceAccess{Owner: true, Recent: true}, func(_ pgx.Tx, q *db.Queries, _ store.Workspace) (any, error) {
		if e := q.DeleteInvitation(r.Context(), db.DeleteInvitationParams{WorkspaceID: ws, ID: id}); e != nil {
			return nil, e
		}
		return nil, store.Audit(r.Context(), q, *principal(r.Context()), ws, "invitation.revoke", id)
	})
}
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.scope(w, r, id, store.WorkspaceAccess{Owner: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListAudit(r.Context(), id)
		out := []api.AuditEvent{}
		for _, v := range rows {
			out = append(out, api.AuditEvent{Id: v.ID, Action: v.Action, ActorId: v.ActorID, ResourceId: v.ResourceID, CreatedAt: v.CreatedAt, AutomationCredentialId: v.AutomationCredentialID})
		}
		return out, e
	})
}
