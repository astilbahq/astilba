package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
)

func (s *Server) ExportWorkspace(w http.ResponseWriter, r *http.Request, ws uuid.UUID) {
	b, e := decode[api.ArchivePassword](r)
	if e != nil {
		s.fail(w, r, e)
		return
	}
	service := portability.Service{Store: s.Store}
	if e = service.AdmitExport(r.Context(), *principal(r.Context()), ws); e != nil {
		s.fail(w, r, e)
		return
	}
	var raw []byte
	e = s.archives.work(w, r, func() (e error) {
		raw, e = service.Export(r.Context(), *principal(r.Context()), ws, value(b.Passphrase))
		return e
	})
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
	var ws store.Workspace
	e = s.archives.work(w, r, func() (e error) {
		ws, e = service.Import(r.Context(), *principal(r.Context()), value(b.Passphrase), archive, value(b.Invitation))
		return e
	})
	if e != nil {
		if errors.Is(e, portability.ErrArchive) {
			e = domain.Invalid("archive or passphrase")
		}
		s.fail(w, r, e)
		return
	}
	s.respond(w, 201, workspace(ws))
}

// Export and import each derive a key from a passphrase and hold a whole workspace in memory,
// so a server works on one at a time. Each request pays for itself before it waits: the
// account's hourly budget is charged before its body is read, and its authority is checked
// before it waits for the turn. It then waits in arrival order, for a short while, so a busy
// turn delays an owner rather than refusing them. An account has one archive request reading,
// waiting or running at a time, so it holds at most one place in each queue.
//
// An import's body, up to 45 MB, is read only once the request holds one of importBodies
// places, waited for in the same way, so a server holds a bounded number of them however many
// accounts send one. An export's body is a passphrase.
const (
	archiveBudget = 5
	archiveWait   = 10 * time.Second
	importBodies  = 2
	// archiveWrite is what a request has to do its work and answer once its turn comes, as long
	// as the server gives a request that did not wait.
	archiveWrite = 35 * time.Second
	// importRead is what an import has to send its body once it has a place, as long as the
	// server gives a request that did not wait.
	importRead = 15 * time.Second
)

var (
	// archivesBusy answers a request that waited out its turn: the server is busy, and the
	// account has done nothing wrong, so it asks for a retry in seconds.
	archivesBusy = &domain.Error{Code: "temporarily_busy", Message: "Another export or import is in progress. Try again in a few seconds."}
	// archiveHeld answers an account's second archive request while its first is in progress.
	archiveHeld = &domain.Error{Code: "temporarily_busy", Message: "Your other export or import is still in progress. Try again when it finishes."}
)

type archiveTurns struct {
	slot     chan struct{}
	bodies   chan struct{}
	wait     time.Duration
	mu       sync.Mutex
	accounts map[uuid.UUID]bool
}

func newArchiveTurns() *archiveTurns {
	return &archiveTurns{slot: make(chan struct{}, 1), bodies: make(chan struct{}, importBodies), wait: archiveWait, accounts: map[uuid.UUID]bool{}}
}

// hold reserves the account's one archive request, or reports that it already has one.
func (t *archiveTurns) hold(account uuid.UUID) (func(), bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.accounts[account] {
		return nil, false
	}
	t.accounts[account] = true
	return func() {
		t.mu.Lock()
		delete(t.accounts, account)
		t.mu.Unlock()
	}, true
}

// take waits up to t.wait for a place in places. Waiters are served in the order they began to
// wait.
func (t *archiveTurns) take(ctx context.Context, places chan struct{}) (func(), error) {
	wait := time.NewTimer(t.wait)
	defer wait.Stop()
	select {
	case places <- struct{}{}:
		return func() { <-places }, nil
	case <-wait.C:
		return nil, archivesBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// work runs fn in the server's archive turn, once it comes. However long the request took to get
// here, it can answer, busy or not, and fn has archiveWrite once its turn comes. The turn ends
// with fn, before the answer is written.
func (t *archiveTurns) work(w http.ResponseWriter, r *http.Request, fn func() error) error {
	if err := http.NewResponseController(w).SetWriteDeadline(time.Now().Add(t.wait + archiveWrite)); err != nil && !errors.Is(err, http.ErrNotSupported) {
		return err
	}
	release, err := t.take(r.Context(), t.slot)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

// archiveAdmission charges an archive request to its account before the request validator reads
// its body, so a body that never finishes, or does not parse, still pays. An import's account is
// admitted, and its body given a place, before the body is read.
func (s *Server) archiveAdmission(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		var scope string
		switch {
		case r.Method != "POST":
		case r.URL.Path == "/api/v1/workspaces/import":
			scope = "workspace-import:"
		case len(parts) == 6 && parts[1] == "api" && parts[2] == "v1" && parts[3] == "workspaces" && parts[5] == "export":
			scope = "workspace-export:"
		}
		if scope == "" {
			next.ServeHTTP(w, r)
			return
		}
		p := principal(r.Context())
		if p == nil {
			s.fail(w, r, domain.Unauthenticated)
			return
		}
		release, held := s.archives.hold(p.Account.ID)
		if !held {
			s.fail(w, r, archiveHeld)
			return
		}
		defer release()
		if e := s.Auth.Limit(r.Context(), scope+p.Account.ID.String(), archiveBudget, time.Hour); e != nil {
			s.fail(w, r, e)
			return
		}
		if scope == "workspace-import:" {
			if e := (&portability.Service{Store: s.Store}).AdmitImport(*p); e != nil {
				s.fail(w, r, e)
				return
			}
			read, e := s.archives.take(r.Context(), s.archives.bodies)
			if e != nil {
				s.fail(w, r, e)
				return
			}
			defer read()
			if e = http.NewResponseController(w).SetReadDeadline(time.Now().Add(importRead)); e != nil && !errors.Is(e, http.ErrNotSupported) {
				s.fail(w, r, e)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
