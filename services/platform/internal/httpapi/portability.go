package httpapi

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
)

func (s *Server) ExportWorkspace(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	if e := s.Auth.Limit(r.Context(), "workspace-export:"+principal(r.Context()).Account.ID.String(), 5, time.Hour); e != nil {
		s.fail(w, r, e)
		return
	}
	b, e := decode[api.ArchivePassword](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	service := portability.Service{Store: s.Store}
	raw, e := service.Export(r.Context(), *principal(r.Context()), ws, value(b.Passphrase))
	if e != nil {
		if errors.Is(e, portability.ErrArchive) {
			e = domain.Invalid("archive or passphrase")
		}
		s.fail(w, r, e)
		return
	}
	s.respond(w, 200, api.WorkspaceArchive{Archive: raw})
}
func (s *Server) PrepareServerTransfer(w http.ResponseWriter, r *http.Request, ws, id uuid.UUID) {
	v, e := s.Execution.PrepareTransfer(r.Context(), *principal(r.Context()), ws, id)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	v.ManagementOrigin = s.Origin
	s.respond(w, 201, v)
}

func (s *Server) ImportWorkspace(w http.ResponseWriter, r *http.Request) {
	if e := s.Auth.Limit(r.Context(), "workspace-import:"+principal(r.Context()).Account.ID.String(), 5, time.Hour); e != nil {
		s.fail(w, r, e)
		return
	}
	b, e := decode[api.WorkspaceImport](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	var archive []byte
	if b.Archive != nil {
		archive = *b.Archive
	}
	service := portability.Service{Store: s.Store}
	ws, e := service.Import(r.Context(), *principal(r.Context()), value(b.Passphrase), archive, value(b.Invitation))
	if e != nil {
		if errors.Is(e, portability.ErrArchive) {
			e = domain.Invalid("archive or passphrase")
		}
		s.fail(w, r, e)
		return
	}
	s.respond(w, 201, workspace(ws))
}

var archiveRequests = make(chan struct{}, 1)

func (s *Server) archiveAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		archive := r.Method == "POST" && (r.URL.Path == "/api/v1/workspaces/import" || len(parts) == 6 && parts[1] == "api" && parts[2] == "v1" && parts[3] == "workspaces" && parts[5] == "export")
		if !archive {
			next.ServeHTTP(w, r)
			return
		}
		if principal(r.Context()) == nil {
			s.fail(w, r, domain.Unauthenticated)
			return
		}
		select {
		case archiveRequests <- struct{}{}:
			defer func() { <-archiveRequests }()
		default:
			s.fail(w, r, domain.RateLimited)
			return
		}
		next.ServeHTTP(w, r)
	})
}
