package httpapi

import (
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

// RetireSecret erases the value of every version of a secret that nothing may still deploy, and
// frees its name. The versions' rows stay, because revisions name them; a plan made from such a
// revision finds them missing. Only an owner who confirmed their identity recently may do it.
func (s *Server) RetireSecret(w http.ResponseWriter, r *http.Request, ws, pid, env, id uuid.UUID) {
	s.envScope(w, r, ws, pid, env, store.WorkspaceAccess{Write: true, Owner: true, Recent: true}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		ctx := r.Context()
		// The secret must be this environment's and not yet retired. The workspace lock orders this
		// with plans, operations, draft saves and new versions that could name one of its versions.
		if _, e := q.LiveSecret(ctx, db.LiveSecretParams{WorkspaceID: ws, EnvironmentID: env, ID: id}); e != nil {
			return nil, e
		}
		used, e := q.EnvironmentSecretVersionsInUse(ctx, db.EnvironmentSecretVersionsInUseParams{WorkspaceID: ws, EnvironmentID: env, PendingPlans: true})
		if e != nil {
			return nil, e
		}
		for _, v := range used {
			if v.SecretID == id {
				return nil, domain.SecretInUse
			}
		}
		if e = q.RetireSecret(ctx, db.RetireSecretParams{WorkspaceID: ws, ID: id}); e != nil {
			return nil, e
		}
		if e = q.EraseSecretValues(ctx, db.EraseSecretValuesParams{WorkspaceID: ws, SecretID: id}); e != nil {
			return nil, e
		}
		return nil, store.Audit(ctx, q, *principal(ctx), ws, "secret.retire", id)
	})
}

// RetireSecretVersion erases one superseded version's value, under the same rules as RetireSecret.
// It clears a bad value, and one that holds the runtime-secret check's budget, without giving up
// the secret. The current version is refused: a new version replaces it.
func (s *Server) RetireSecretVersion(w http.ResponseWriter, r *http.Request, ws, pid, env, secret, id uuid.UUID) {
	s.envScope(w, r, ws, pid, env, store.WorkspaceAccess{Write: true, Owner: true, Recent: true}, func(q *db.Queries, _ db.GetEnvironmentRow) (any, error) {
		ctx := r.Context()
		// The workspace lock orders this with new versions, which change which one is current.
		v, e := q.LiveSecretVersion(ctx, db.LiveSecretVersionParams{WorkspaceID: ws, EnvironmentID: env, SecretID: secret, ID: id})
		if e != nil {
			return nil, e
		}
		if v.Current {
			return nil, domain.SecretVersionCurrent
		}
		used, e := q.EnvironmentSecretVersionsInUse(ctx, db.EnvironmentSecretVersionsInUseParams{WorkspaceID: ws, EnvironmentID: env, PendingPlans: true})
		if e != nil {
			return nil, e
		}
		for _, u := range used {
			if u.ID == id {
				return nil, domain.SecretVersionInUse
			}
		}
		if e = q.EraseSecretVersion(ctx, db.EraseSecretVersionParams{WorkspaceID: ws, ID: id}); e != nil {
			return nil, e
		}
		return nil, store.Audit(ctx, q, *principal(ctx), ws, "secret.version.retire", id)
	})
}
