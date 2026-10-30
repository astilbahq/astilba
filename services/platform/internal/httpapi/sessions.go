package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/alexedwards/scs/pgxstore"
	"github.com/jackc/pgx/v5/pgxpool"
)

var errSessionRevoked = errors.New("browser session was revoked")
var errSessionContext = errors.New("browser session context is unavailable")
var errSessionResponse = errors.New("browser session response is unavailable")

type sessionLoadKey struct{}
type sessionLoad struct{ token string }

type sessionStore struct {
	*pgxstore.PostgresStore
	pool *pgxpool.Pool
}

func (s *sessionStore) FindCtx(ctx context.Context, token string) ([]byte, bool, error) {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return nil, false, errSessionContext
	}
	data, found, err := s.PostgresStore.FindCtx(ctx, token)
	if err == nil && found {
		loaded.token = token
	}
	return data, found, err
}

// SCS must use its context-aware store API to distinguish a loaded token from
// a freshly minted token. A context-free save cannot establish that distinction.
func (s *sessionStore) Commit(string, []byte, time.Time) error { return errSessionContext }

func (s *sessionStore) CommitCtx(ctx context.Context, token string, data []byte, expiry time.Time) error {
	loaded, _ := ctx.Value(sessionLoadKey{}).(*sessionLoad)
	if loaded == nil {
		return errSessionContext
	}
	if loaded.token == token {
		// A concurrent logout/renewal or expiry cleanup may have deleted the row.
		// UPDATE serializes with that deletion and never recreates its authority.
		tag, err := s.pool.Exec(ctx, "UPDATE sessions SET data=$2,expiry=$3 WHERE token=$1", token, data, expiry)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return errSessionRevoked
		}
	} else {
		if _, err := s.pool.Exec(ctx, "INSERT INTO sessions(token,data,expiry) VALUES($1,$2,$3)", token, data, expiry); err != nil {
			return err
		}
	}
	loaded.token = token
	return nil
}

type sessionResponseGuard struct {
	http.ResponseWriter
	failed bool
}

func (w *sessionResponseGuard) WriteHeader(status int) {
	if !w.failed {
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *sessionResponseGuard) Write(data []byte) (int, error) {
	if w.failed {
		return 0, errSessionResponse
	}
	return w.ResponseWriter.Write(data)
}
func (w *sessionResponseGuard) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *sessionResponseGuard) FlushError() error {
	if w.failed {
		return errSessionResponse
	}
	return http.NewResponseController(w.ResponseWriter).Flush()
}

func browserSessionRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), sessionLoadKey{}, &sessionLoad{})
		next.ServeHTTP(&sessionResponseGuard{ResponseWriter: w}, r.WithContext(ctx))
	})
}
