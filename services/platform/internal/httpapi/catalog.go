package httpapi

import (
	"net/http"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Server) ListServiceCatalog(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	s.scope(w, r, ws, false, false, false, func(_ *db.Queries, _ store.Workspace) (any, error) {
		return catalog.Recipes(), nil
	})
}
