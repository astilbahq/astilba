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
		used, e := q.SecretInUse(ctx, db.SecretInUseParams{WorkspaceID: ws, SecretID: id, EnvironmentID: env})
		if e != nil {
			return nil, e
		}
		if used {
			return nil, domain.SecretInUse
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
