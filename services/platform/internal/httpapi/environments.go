package httpapi

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"net/http"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func project(v db.Project) api.Project {
	return api.Project{Id: v.ID, Name: v.Name, CreatedAt: v.CreatedAt}
}
func environment(v db.GetEnvironmentRow) api.Environment {
	purpose := api.EnvironmentPurpose(v.Purpose)
	return api.Environment{Purpose: &purpose, Id: v.ID, ProjectId: v.ProjectID, Name: v.Name, ConfigurationOwner: api.EnvironmentConfigurationOwner(v.ConfigurationOwner), DraftRevisionId: v.DraftRevisionID, AppliedRevisionId: v.AppliedRevisionID, DeploymentUnit: api.DeploymentUnit{Id: v.UnitID, Runtime: api.Docker, ServerId: v.ServerID}}
}
func revision(v db.SpecificationRevision) api.Revision {
	return api.Revision{Id: v.ID, AuthorId: v.AuthorID, CreatedAt: v.CreatedAt, Hash: hex.EncodeToString(v.ContentHash), Specification: jsontext.Value(v.Specification)}
}
func (s *Server) ListProjects(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(q *db.Queries, _ store.Workspace) (any, error) {
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
	s.scope(w, r, ws, store.WorkspaceAccess{Write: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.InsertProject(r.Context(), db.InsertProjectParams{WorkspaceID: ws, ID: uuid.New(), Name: name})
		if e != nil {
			return nil, e
		}
		return project(v), store.Audit(r.Context(), q, *principal(r.Context()), ws, "project.create", v.ID)
	})
}
func (s *Server) GetProject(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.GetProject(r.Context(), db.GetProjectParams{WorkspaceID: ws, ID: id})
		return project(v), e
	})
}
func (s *Server) ListEnvironments(w http.ResponseWriter, r *http.Request, ws, pid uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(q *db.Queries, _ store.Workspace) (any, error) {
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
	return store.InsertRevision(r.Context(), q, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: id, AuthorID: principal(r.Context()).Account.ID, Specification: canonical, ContentHash: h[:]})
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
	s.scope(w, r, ws, store.WorkspaceAccess{Write: true}, func(q *db.Queries, _ store.Workspace) (any, error) {
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
		raw, _ := stablejson.Marshal(initial)
		_, canonical, e := domain.ParseSpec(raw)
		if e != nil {
			return nil, e
		}
		if b.Purpose != nil && *b.Purpose == api.EnvironmentCreatePurposePreviewTemplate && owner != "dashboard" {
			return nil, domain.Invalid("preview.composeUnsupported")
		}
		if _, e := q.InsertEnvironment(ctx, db.InsertEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id, Name: name, DraftRevisionID: rev, ConfigurationOwner: owner, Purpose: func() string {
			if b.Purpose != nil {
				return string(*b.Purpose)
			}
			return "standard"
		}()}); e != nil {
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
func (s *Server) envScope(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID, access store.WorkspaceAccess, fn func(*db.Queries, db.GetEnvironmentRow) (any, error)) {
	s.scope(w, r, ws, access, func(q *db.Queries, _ store.Workspace) (any, error) {
		v, e := q.GetEnvironment(r.Context(), db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id})
		if e != nil {
			return nil, e
		}
		return fn(q, v)
	})
}
func (s *Server) GetEnvironment(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(_ *db.Queries, v db.GetEnvironmentRow) (any, error) { return environment(v), nil })
}
func (s *Server) GetDraft(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(q *db.Queries, v db.GetEnvironmentRow) (any, error) {
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
	var out api.Revision
	var current *api.Revision
	reader := principal(r.Context()).MayReadEnvironment()
	e = s.Store.InWorkspaceTx(r.Context(), *principal(r.Context()), ws, store.WorkspaceAccess{Write: true}, func(tx pgx.Tx, q *db.Queries, _ store.Workspace) error {
		ctx := r.Context()
		env, e := q.GetEnvironment(ctx, db.GetEnvironmentParams{WorkspaceID: ws, ProjectID: pid, ID: id})
		if e != nil {
			return e
		}
		// The base revision is checked before anything that compares the save with the environment or
		// its draft. A credential that may write but not read would otherwise learn from which error it
		// gets, without naming the current draft, whether its guesses about them are right.
		if env.DraftRevisionID != b.BaseRevisionId {
			if reader {
				v, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID})
				if e != nil {
					return e
				}
				c := revision(v)
				current = &c
			}
			return domain.Conflict
		}
		spec, canonical, e := parseDraft(ctx, q, env, b.Specification, reader)
		if e != nil {
			return e
		}
		if e := s.Store.ValidateApplicationRoutes(ctx, tx, ws, id, spec, nil); e != nil {
			return e
		}
		var applied domain.Specification
		if env.AppliedRevisionID != nil {
			v, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: *env.AppliedRevisionID})
			if e != nil {
				return e
			}
			if e = json.Unmarshal(v.Specification, &applied); e != nil {
				return e
			}
		}
		if e = domain.ValidateNewDraft(spec, applied); e != nil {
			return e
		}
		for _, secretID := range domain.SecretReferences(spec) {
			if _, e := q.GetSecretVersion(ctx, db.GetSecretVersionParams{WorkspaceID: ws, EnvironmentID: id, ID: secretID}); e != nil {
				return store.IfMissing(e, domain.Invalid("specification.secretVersionId"))
			}
		}
		// Saving the draft unchanged adds no revision. The kept revision shows who saved it and when,
		// so only a principal that may read the environment gets it, as on a conflict.
		if reader {
			same, e := q.GetRevisionHolding(ctx, db.GetRevisionHoldingParams{WorkspaceID: ws, EnvironmentID: id, ID: env.DraftRevisionID, Specification: canonical})
			if e == nil {
				out = revision(same)
				return nil
			}
			if !errors.Is(e, pgx.ErrNoRows) {
				return e
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
	// The saved revision holds what the save kept from the draft, a Compose source's origin included,
	// and its hash covers them, so only a principal that may read the environment gets either.
	saved := api.SavedDraft{Id: out.Id, AuthorId: out.AuthorId, CreatedAt: out.CreatedAt}
	if reader {
		saved.Hash, saved.Specification = &out.Hash, &out.Specification
	}
	s.respond(w, 200, saved)
}

// parseDraft parses a draft save for the environment. A Compose bundle's namespace and Git origin
// belong to the environment, not to the save: the namespace is its deployment unit, and only an import
// sets the origin. So a save may leave both out and the draft keeps its own. A principal that may read
// the environment may send them unchanged. A credential that may not can't know them, and comparing
// what it sent would confirm its guesses, so it must leave them out and gets the same refusal whatever
// it sends.
func parseDraft(ctx context.Context, q *db.Queries, env db.GetEnvironmentRow, raw []byte, reader bool) (domain.Specification, []byte, error) {
	var sent struct {
		Compose *struct {
			Namespace uuid.UUID              `json:"namespace"`
			Origin    *composeproject.Origin `json:"origin"`
		} `json:"compose"`
	}
	// This reads only the Compose identity; ParseSpec checks the whole specification.
	if json.Unmarshal(raw, &sent) != nil {
		return domain.Specification{}, nil, domain.Invalid("specification")
	}
	if (env.ConfigurationOwner == "compose") != (sent.Compose != nil) {
		return domain.Specification{}, nil, domain.Invalid("configurationOwner")
	}
	if sent.Compose == nil {
		return domain.ParseSpec(raw)
	}
	if !reader && sent.Compose.Namespace != uuid.Nil {
		return domain.Specification{}, nil, domain.Invalid("compose.namespace")
	}
	if !reader && sent.Compose.Origin != nil {
		return domain.Specification{}, nil, domain.Invalid("compose.origin")
	}
	if sent.Compose.Namespace != uuid.Nil && sent.Compose.Namespace != env.UnitID {
		return domain.Specification{}, nil, domain.Invalid("configurationOwner")
	}
	previous, e := q.GetRevision(ctx, db.GetRevisionParams{WorkspaceID: env.WorkspaceID, EnvironmentID: env.ID, ID: env.DraftRevisionID})
	if e != nil {
		return domain.Specification{}, nil, e
	}
	var old domain.Specification
	if json.Unmarshal(previous.Specification, &old) != nil || old.Compose == nil {
		return domain.Specification{}, nil, domain.Conflict
	}
	before, _ := stablejson.Marshal(old.Compose.Origin)
	after, _ := stablejson.Marshal(sent.Compose.Origin)
	if sent.Compose.Origin != nil && !bytes.Equal(before, after) {
		return domain.Specification{}, nil, domain.Invalid("compose.origin")
	}
	kept, e := withComposeIdentity(raw, env.UnitID)
	if e != nil {
		return domain.Specification{}, nil, e
	}
	spec, canonical, e := domain.ParseSpec(kept)
	if e != nil {
		return spec, nil, e
	}
	// The origin counts towards the size limits. So that whether a save fits can't show whether the
	// draft has an origin or how long it is, a credential that may not read the environment must leave
	// room for the longest origin an import can record, whatever the draft holds.
	if !reader {
		fits, e := fitsAnyOrigin(ctx, q, spec)
		if e != nil {
			return domain.Specification{}, nil, e
		}
		if !fits {
			return domain.Specification{}, nil, domain.SpecificationTooLarge
		}
	}
	if old.Compose.Origin == nil {
		return spec, canonical, nil
	}
	// Nothing ParseSpec derives depends on the origin, so the draft's is added to what it parsed.
	spec.Compose.Origin = old.Compose.Origin
	if canonical, e = stablejson.Marshal(spec); e == nil && len(canonical) > domain.SpecificationLimit {
		e = domain.SpecificationTooLarge
	}
	return spec, canonical, e
}

// fitsAnyOrigin is whether a Compose specification stays within both size limits, canonical JSON
// and the stored column's jsonb text, with any origin an import can record. The longest repository
// URL takes the most room when every byte is one each form escapes most: '<' in canonical JSON and
// '"' in jsonb text.
func fitsAnyOrigin(ctx context.Context, q *db.Queries, spec domain.Specification) (bool, error) {
	for _, char := range []string{"<", `"`} {
		bundle := *spec.Compose
		bundle.Origin = &composeproject.Origin{Provider: "github", Repository: strings.Repeat(char, 2048), Commit: strings.Repeat("0", 64)}
		spec.Compose = &bundle
		raw, e := stablejson.Marshal(spec)
		if e != nil {
			return false, e
		}
		if fits, e := store.SpecificationFits(ctx, q, raw); e != nil || !fits {
			return false, e
		}
	}
	return true, nil
}

// withComposeIdentity sets the namespace of a specification's Compose bundle, removes any origin and
// leaves the rest as sent, for ParseSpec to check.
func withComposeIdentity(raw []byte, namespace uuid.UUID) ([]byte, error) {
	var spec, bundle map[string]jsontext.Value
	if json.Unmarshal(raw, &spec) != nil || json.Unmarshal(spec["compose"], &bundle) != nil {
		return nil, domain.Invalid("specification")
	}
	var e error
	if bundle["namespace"], e = stablejson.Marshal(namespace); e != nil {
		return nil, e
	}
	delete(bundle, "origin")
	if spec["compose"], e = stablejson.Marshal(bundle); e != nil {
		return nil, e
	}
	return stablejson.Marshal(spec)
}
func (s *Server) ListRevisions(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		rows, e := q.ListRevisions(r.Context(), db.ListRevisionsParams{WorkspaceID: ws, EnvironmentID: id})
		out := []api.RevisionSummary{}
		for _, v := range rows {
			out = append(out, api.RevisionSummary{Id: v.ID, Hash: hex.EncodeToString(v.ContentHash), AuthorId: v.AuthorID, CreatedAt: v.CreatedAt})
		}
		return out, e
	})
}
func (s *Server) GetRevision(w http.ResponseWriter, r *http.Request, ws, pid, id, rid uuid.UUID) {
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		v, e := q.GetRevision(r.Context(), db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: id, ID: rid})
		return revision(v), e
	})
}
func (s *Server) GetLayout(w http.ResponseWriter, r *http.Request, ws, pid, id uuid.UUID) {
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
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
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{Write: true}, func(q *db.Queries, env db.GetEnvironmentRow) (any, error) {
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
		raw, e := stablejson.Marshal(b.Positions)
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
	s.envScope(w, r, ws, pid, id, store.WorkspaceAccess{}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
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
	versionID := uuid.New()
	item := secretbroker.Item{Kind: secretbroker.SecretVersion, ID: versionID}
	access := store.WorkspaceAccess{Write: true, Recent: true}
	keys, e := s.Store.BeginKeys(r.Context(), *principal(r.Context()), access, false, secretbroker.Request{Workspace: ws, Purpose: secretbroker.Seal, Items: []secretbroker.Item{item}})
	if e != nil {
		s.fail(w, r, e)
		return
	}
	sealed := false
	defer func() { keys.Close(r.Context(), secretbroker.UsedIf(sealed)) }()
	s.envScope(w, r, ws, pid, env, access, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
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
		cipher, e := keys.Seal(item, []byte(value))
		if e != nil {
			return nil, e
		}
		sealed = true
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
