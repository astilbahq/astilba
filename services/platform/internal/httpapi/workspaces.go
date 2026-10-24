package httpapi

import (
	"net/http"
	"net/url"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/auth"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	openapi_types "github.com/oapi-codegen/runtime/types"
)

func workspace(w store.Workspace) api.Workspace {
	return api.Workspace{Id: w.ID, Name: w.Name, Role: api.Role(w.Role), RequireMfa: w.RequireMFA}
}
func (s *Server) scope(w http.ResponseWriter, r *http.Request, id uuid.UUID, write, owner, recent bool, fn func(*db.Queries, store.Workspace) (any, error)) {
	var out any
	e := s.Store.InWorkspace(r.Context(), *principal(r.Context()), id, write, owner, recent, func(q *db.Queries, ws store.Workspace) error { var err error; out, err = fn(q, ws); return err })
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
		_, id, e := s.Auth.AcceptHosted(r.Context(), value(b.Invitation), "", "", name, &p)
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
	s.scope(w, r, id, false, false, false, func(_ *db.Queries, ws store.Workspace) (any, error) { return workspace(ws), nil })
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
	s.scope(w, r, id, true, true, true, func(q *db.Queries, ws store.Workspace) (any, error) {
		if b.RequireMfa && !principal(r.Context()).MFA {
			return nil, domain.MFARequired
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
	s.scope(w, r, id, false, true, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListMembers(r.Context(), id)
		out := []api.Member{}
		for _, v := range rows {
			out = append(out, api.Member{Id: v.ID, Email: openapi_types.Email(v.Email), Name: v.Name, Role: api.Role(v.Role), MfaEnabled: v.MfaEnabled})
		}
		return out, e
	})
}
func (s *Server) member(w http.ResponseWriter, r *http.Request, ws, account uuid.UUID, role *api.Role) {
	s.scope(w, r, ws, true, true, true, func(q *db.Queries, _ store.Workspace) (any, error) {
		ctx := r.Context()
		m, e := q.GetMembership(ctx, db.GetMembershipParams{WorkspaceID: ws, AccountID: account})
		if e != nil {
			return nil, e
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
			e = q.UpdateMembership(ctx, db.UpdateMembershipParams{WorkspaceID: ws, AccountID: account, Role: string(*role)})
		}
		if e != nil {
			return nil, e
		}
		return nil, store.Audit(ctx, q, *principal(ctx), ws, "membership.change", account)
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
	s.scope(w, r, id, false, true, false, func(q *db.Queries, _ store.Workspace) (any, error) {
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
	token, e := cryptobox.Token()
	if e != nil {
		s.fail(w, r, e)
		return
	}
	id := uuid.New()
	expiry := s.Auth.Now().Add(24 * time.Hour)
	e = s.Store.InWorkspace(r.Context(), *principal(r.Context()), ws, true, true, true, func(q *db.Queries, _ store.Workspace) error {
		if e := q.InsertInvitation(r.Context(), db.InsertInvitationParams{WorkspaceID: ws, ID: id, Email: email, Role: string(b.Role), TokenHash: auth.Hash(token), ExpiresAt: expiry}); e != nil {
			return e
		}
		return store.Audit(r.Context(), q, *principal(r.Context()), ws, "invitation.create", id)
	})
	if e == nil {
		e = s.Auth.Mail.Send(r.Context(), email, "Join an Astilba workspace", "Open this one-use link to join the Astilba workspace within 24 hours:\n\n"+s.Origin+"/invitation?"+url.Values{"token": {token}}.Encode())
		if e != nil {
			_ = s.Store.Queries.DeleteInvitation(r.Context(), db.DeleteInvitationParams{WorkspaceID: ws, ID: id})
		}
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 201, api.Invitation{Id: id, Email: openapi_types.Email(email), Role: b.Role, ExpiresAt: expiry})
}
func (s *Server) RevokeInvitation(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	s.scope(w, r, ws, true, true, true, func(q *db.Queries, _ store.Workspace) (any, error) {
		if e := q.DeleteInvitation(r.Context(), db.DeleteInvitationParams{WorkspaceID: ws, ID: id}); e != nil {
			return nil, e
		}
		return nil, store.Audit(r.Context(), q, *principal(r.Context()), ws, "invitation.revoke", id)
	})
}
func (s *Server) ListAudit(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	s.scope(w, r, id, false, true, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListAudit(r.Context(), id)
		out := []api.AuditEvent{}
		for _, v := range rows {
			out = append(out, api.AuditEvent{Id: v.ID, Action: v.Action, ActorId: v.ActorID, ResourceId: v.ResourceID, CreatedAt: v.CreatedAt})
		}
		return out, e
	})
}
