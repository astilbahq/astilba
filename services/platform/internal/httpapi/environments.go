package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func project(v db.Project) api.Project {
	return api.Project{Id: v.ID, Name: v.Name, CreatedAt: v.CreatedAt}
}
func environment(v db.GetEnvironmentRow) api.Environment {
	return api.Environment{Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, ConfigurationOwner: api.EnvironmentConfigurationOwner(v.ConfigurationOwner), DraftRevisionId: v.DraftRevisionID, AppliedRevisionId: v.AppliedRevisionID, DeploymentUnit: api.DeploymentUnit{Id: v.UnitID, Runtime: api.Docker, ServerId: v.ServerID}}
}
func revision(v db.SpecificationRevision) api.Revision {
	return api.Revision{Id: v.ID, AuthorId: v.AuthorID, CreatedAt: v.CreatedAt, Hash: hex.EncodeToString(v.ContentHash), Specification: json.RawMessage(v.Specification)}
}
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, false, false, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		rows, e := q.ListProjects(r.Context(), ws)
		out := []api.Project{}
		for _, v := range rows {
			out = append(out, project(v))
		}
		return out, e
	})
}
func (s *Server) CreateProject(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.Name](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	name, e := domain.Name(b.Name)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.scope(w, r, ws, true, false, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.InsertProject(r.Context(), db.InsertProjectParams{WorkspaceID: ws, ID: uuid.New(), Name: name})
		if e != nil {
			return nil, e
		}
		return project(v), store.Audit(r.Context(), q, *principal(r.Context()), ws, "project.create", v.ID)
	})
}
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	s.scope(w, r, ws, false, false, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.GetProject(r.Context(), db.GetProjectParams{WorkspaceID: ws, ID: id})
		return project(v), e
	})
}
func (s *Server) ListEnvironments(w http.ResponseWriter, r *http.Request, ws, pid uuid.UUID) {
	s.scope(w, r, ws, false, false, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		if _, e := q.GetProject(r.Context(), db.GetProjectParams{WorkspaceID: ws, ID: pid}); e != nil {
			return nil, e
		}
		rows, e := q.ListEnvironments(r.Context(), db.ListEnvironmentsParams{WorkspaceID: ws, ProjectID: pid})
		out := []api.Environment{}
		for _, v := range rows {
			x, e := q.GetEnvironment(r.Context(), db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: v.ID})
			if e != nil {
				return nil, e
			}
			out = append(out, environment(x))
		}
		return out, e
	})
}
func insertRevision(r *http.Request, q *db.Queries, ws, env, id uuid.UUID, canonical []byte) (db.SpecificationRevision, error) {
	h := sha256.Sum256(canonical)
	return q.InsertRevision(r.Context(), db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: id, AuthorID: principal(r.Context()).Account.ID, Specification: canonical, ContentHash: h[:]})
}
func (s *Server) CreateEnvironment(w http.ResponseWriter, r *http.Request, ws, pid uuid.UUID) {
	b, e := decode[api.EnvironmentCreate](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	name, e := domain.Name(b.Name)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.scope(w, r, ws, true, false, false, func(q *db.Queries, _ store.Workspace) (any, error) {
		ctx := r.Context()
		if _, e := q.GetProject(ctx, db.GetProjectParams{WorkspaceID: ws, ID: pid}); e != nil {
			return nil, e
		}
		id, rev, unit := uuid.New(), uuid.New(), uuid.New()
		owner := "dashboard"
		if b.ConfigurationOwner != nil {
			owner = string(*b.ConfigurationOwner)
		}
		if owner != "dashboard" && owner != "compose" {
			return nil, domain.Invalid("configurationOwner")
		}
		initial := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}
		if owner == "compose" {
			initial.Compose = &composeproject.Bundle{Namespace: unit, Entrypoints: []string{"compose.yaml"}, Files: []composeproject.File{{Path: "compose.yaml", Content: "services: {}\n"}}}
		}
		raw, _ := json.Marshal(initial)
		_, canonical, e := domain.ParseSpec(raw)
		if e != nil {
			return nil, e
		}
		if _, e := q.InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id, Name: name, DraftRevisionID: rev, ConfigurationOwner: owner}); e != nil {
			return nil, e
		}
		if _, e := insertRevision(r, q, ws, id, rev, canonical); e != nil {
			return nil, e
		}
		if e := q.InsertDeploymentUnit(ctx, db.InsertDeploymentUnitParams{WorkspaceID: ws, ID: unit, EnvironmentID: id}); e != nil {
			return nil, e
		}
		if _, e := q.PutLayout(ctx, db.PutLayoutParams{WorkspaceID: ws, EnvironmentID: id, Positions: []byte(`{}`)}); e != nil {
			return nil, e
		}
		v, e := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id})
		if e != nil {
			return nil, e
		}
		return environment(v), store.Audit(ctx, q, *principal(ctx), ws, "environment.create", id)
	})
}
func (s *Server) envScope(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID, write, recent bool, fn func(*db.Queries, db.GetEnvironmentRow) (any, error)) {
	s.scope(w, r, ws, write, false, recent, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.GetEnvironment(r.Context(), db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id})
		if e != nil {
			return nil, e
		}
		return fn(q, v)
	})
}
func (s *Server) GetEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(_ *db.Queries, v db.GetEnvironmentRow) (any, error) { return environment(v), nil })
}
func (s *Server) GetDraft(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(q *db.Queries, v db.GetEnvironmentRow) (any, error) {
		rev, e := q.GetRevision(r.Context(), db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: v.DraftRevisionID})
		return revision(rev), e
	})
}
func (s *Server) UpdateDraft(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	b, e := decode[api.DraftUpdate](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	spec, canonical, e := domain.ParseSpec(b.Specification)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	var out api.Revision
	var current *api.Revision
	e = s.Store.InWorkspace(r.Context(), *principal(r.Context()), ws, true, false, false, func(q *db.Queries, _ store.Workspace) error {
		ctx := r.Context()
		env, e := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id})
		if e != nil {
			return e
		}
		if (env.ConfigurationOwner == "compose") != (spec.Compose != nil) || spec.Compose != nil && spec.Compose.Namespace != env.UnitID {
			return domain.Invalid("configurationOwner")
		}
		if spec.Compose != nil {
			previous, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID})
			if e != nil {
				return e
			}
			var old domain.Specification
			if json.Unmarshal(previous.Specification, &old) != nil || old.Compose == nil {
				return domain.Conflict
			}
			before, _ := json.Marshal(old.Compose.Origin)
			after, _ := json.Marshal(spec.Compose.Origin)
			if !bytes.Equal(before, after) {
				return domain.Invalid("compose.origin")
			}
		}
		if env.DraftRevisionID != b.BaseRevisionId {
			v, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID})
			if e != nil {
				return e
			}
			c := revision(v)
			current = &c
			return domain.Conflict
		}
		for _, secretID := range domain.SecretReferences(spec) {
			if _, e := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: id, ID: secretID}); e != nil {
				return domain.Invalid("specification.secretVersionId")
			}
		}
		newID := uuid.New()
		rev, e := insertRevision(r, q, ws, id, newID, canonical)
		if e != nil {
			return e
		}
		if _, e := q.AdvanceDraft(ctx, db.AdvanceDraftParams{WorkspaceID: ws, ID: id, DraftRevisionID: b.BaseRevisionId, DraftRevisionID_2: newID}); e != nil {
			return e
		}
		out = revision(rev)
		return store.Audit(ctx, q, *principal(ctx), ws, "draft.save", newID)
	})
	if current != nil {
		s.respond(w, 409, api.Error{Code: "conflict", Message: domain.Conflict.Message, RequestId: r.Context().Value(requestIDKey).(string), CurrentRevision: current})
		return
	}
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, out)
}
func (s *Server) ListRevisions(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		rows, e := q.ListRevisions(r.Context(), db.ListRevisionsParams{WorkspaceID: ws, EnvironmentID: id})
		out := []api.RevisionSummary{}
		for _, v := range rows {
			out = append(out, api.RevisionSummary{Id: v.ID, Hash: hex.EncodeToString(v.ContentHash), AuthorId: v.AuthorID, CreatedAt: v.CreatedAt})
		}
		return out, e
	})
}
func (s *Server) GetRevision(w http.ResponseWriter, r *http.Request, ws, pid, id, rid uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		v, e := q.GetRevision(r.Context(), db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: rid})
		return revision(v), e
	})
}
func (s *Server) GetLayout(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		v, e := q.GetLayout(r.Context(), db.GetLayoutParams{WorkspaceID: ws, EnvironmentID: id})
		if e != nil {
			return nil, e
		}
		var positions map[string]api.Position
		e = json.Unmarshal(v.Positions, &positions)
		return api.Layout{Positions: positions}, e
	})
}
func (s *Server) UpdateLayout(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	b, e := decode[api.Layout](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.envScope(w, r, ws, pid, id, true, false, func(q *db.Queries, env db.GetEnvironmentRow) (any, error) {
		v, e := q.GetRevision(r.Context(), db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID})
		if e != nil {
			return nil, e
		}
		spec, _, e := domain.ParseSpec(v.Specification)
		if e != nil {
			return nil, e
		}
		positions := map[string]domain.Position{}
		for id, p := range b.Positions {
			positions[id] = domain.Position{X: float64(p.X), Y: float64(p.Y)}
		}
		if e = domain.ValidateLayout(positions, spec); e != nil {
			return nil, e
		}
		raw, e := json.Marshal(b.Positions)
		if e != nil {
			return nil, e
		}
		if _, e = q.PutLayout(r.Context(), db.PutLayoutParams{WorkspaceID: ws, EnvironmentID: id, Positions: raw}); e != nil {
			return nil, e
		}
		return b, store.Audit(r.Context(), q, *principal(r.Context()), ws, "layout.save", id)
	})
}
func (s *Server) ListSecrets(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, false, false, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		rows, e := q.ListSecrets(r.Context(), db.ListSecretsParams{WorkspaceID: ws, EnvironmentID: id})
		out := []api.SecretVersion{}
		for _, v := range rows {
			out = append(out, api.SecretVersion{Id: v.VersionID, SecretId: v.ID, Name: v.Name, CreatedAt: v.CreatedAt})
		}
		return out, e
	})
}
func (s *Server) secret(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID, name, value string, newSecret bool) {
	if len(value) == 0 || len(value) > 65536 {
		s.fail(w, r, domain.Invalid("value"))
		return
	}
	s.envScope(w, r, ws, pid, env, true, true, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		ctx := r.Context()
		if newSecret {
			if e := q.InsertSecret(ctx, db.InsertSecretParams{WorkspaceID: ws, EnvironmentID: env, ID: id, Name: name}); e != nil {
				return nil, e
			}
		} else {
			v, e := q.GetSecret(ctx, db.GetSecretParams{WorkspaceID: ws, EnvironmentID: env, ID: id})
			if e != nil {
				return nil, e
			}
			name = v.Name
		}
		versionID := uuid.New()
		cipher, e := s.Store.Keys.Seal([]byte(value), store.SecretIdentity(ws, versionID))
		if e != nil {
			return nil, e
		}
		v, e := q.InsertSecretVersion(ctx, db.InsertSecretVersionParams{WorkspaceID: ws, SecretID: id, ID: versionID, EncryptedValue: cipher, AuthorID: principal(ctx).Account.ID})
		if e != nil {
			return nil, e
		}
		return api.SecretVersion{Id: v.ID, SecretId: id, Name: name, CreatedAt: v.CreatedAt}, store.Audit(ctx, q, *principal(ctx), ws, "secret.version", versionID)
	})
}
func (s *Server) CreateSecret(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.SecretRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	name, e := domain.Name(b.Name)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.secret(w, r, ws, pid, env, uuid.New(), name, value(b.Value), true)
}
func (s *Server) CreateSecretVersion(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	b, e := decode[api.SecretValue](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	s.secret(w, r, ws, pid, env, id, "", value(b.Value), false)
}
