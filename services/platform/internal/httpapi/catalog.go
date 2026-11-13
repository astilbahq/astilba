package httpapi

import (
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Server) ListServiceCatalog(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(_ *db.Queries, _ store.Workspace) (any, error) {
		return catalog.Recipes(), nil
	})
}

// ListPostgresReleases lists the PostgreSQL releases managed databases may name, so the dashboard
// offers the current release of each major and the in-place update from a retired one.
func (s *Server) ListPostgresReleases(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, store.WorkspaceAccess{}, func(_ *db.Queries, _ store.Workspace) (any, error) {
		out := []api.PostgresRelease{}
		for _, release := range domain.PostgresReleases() {
			out = append(out, api.PostgresRelease{Image: release.Image, Name: release.Name(), Major: release.Major(), Current: !release.Retired(), Mount: release.Mount})
		}
		return out, nil
	})
}
