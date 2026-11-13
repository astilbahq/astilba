package httpapi

import (
	"net/http"
	"net/netip"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Server) ListServiceCatalog(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(_ *db.Queries, _ store.Workspace) (any, error) {
		return catalog.Recipes(), nil
	})
}

func (s *Server) StageCatalogEntry(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	b, e := decode[api.CatalogStageRequest](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	p := principal(r.Context())
	rev, e := s.Execution.StageCatalogEntry(r.Context(), *p, ws, pid, env, b)
	// As with a draft save, the saved configuration goes only to a caller that may read it.
	out := api.SavedDraft{Id: rev.ID, AuthorId: rev.AuthorID, CreatedAt: rev.CreatedAt}
	if e == nil && p.MayReadEnvironment() {
		v := revision(rev)
		out.Hash, out.Specification = &v.Hash, &v.Specification
	}
	s.executionResponse(w, r, 201, out, e)
}

func (s *Server) ListCatalogStagings(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	v, e := s.Execution.CatalogStagings(r.Context(), *principal(r.Context()), ws, pid, env)
	s.executionResponse(w, r, 200, v, e)
}

// GetClientAddress answers with the network a route's client allowlist would name to admit
// only the caller: its IPv4 address, or its IPv6 /64, which a host's temporary addresses share.
func (s *Server) GetClientAddress(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(_ *db.Queries, _ store.Workspace) (any, error) {
		out := api.ClientAddress{}
		if a := s.clientAddress(r); a.IsValid() && !a.IsUnspecified() {
			bits := 32
			if a.Is6() {
				bits = 64
			}
			prefix := netip.PrefixFrom(a, bits).Masked().String()
			out.Prefix = &prefix
		}
		return out, nil
	})
}
