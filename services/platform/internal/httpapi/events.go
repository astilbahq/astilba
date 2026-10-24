package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Events are bounded invalidation hints. The browser fetches authorized snapshots;
// it never treats delivery order or an open stream as the state of a deployment.
func (s *Server) EnvironmentEvents(w http.ResponseWriter, r *http.Request, ws, pid, env uuid.UUID) {
	p := *principal(r.Context())
	snapshot := func() (string, error) {
		var revision uuid.UUID
		var observed, operations string
		err := s.Store.InWorkspaceTx(r.Context(), p, ws, false, false, false, func(tx pgx.Tx, _ *db.Queries, _ store.Workspace) error {
			return tx.QueryRow(r.Context(), `SELECT e.draft_revision_id,COALESCE(v.observed_at::text,''),COALESCE((SELECT string_agg(o.id::text||o.state||o.report_sequence::text,',' ORDER BY o.id) FROM (SELECT id,state,report_sequence FROM execution_operations WHERE workspace_id=$1 AND environment_id=$3 ORDER BY created_at DESC LIMIT 20) o),'') FROM environments e LEFT JOIN visual_runtime v ON v.workspace_id=e.workspace_id AND v.environment_id=e.id WHERE e.workspace_id=$1 AND e.project_id=$2 AND e.id=$3`, ws, pid, env).Scan(&revision, &observed, &operations)
		})
		return deployment.Hash([]any{revision, observed, operations}), err
	}
	previous, err := snapshot()
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	controller := http.NewResponseController(w)
	send := func() error {
		if _, err := fmt.Fprint(w, "event: changed\ndata: {}\n\n"); err != nil {
			return err
		}
		return controller.Flush()
	}
	if send() != nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			next, err := snapshot()
			if err != nil {
				return
			}
			if next != previous {
				previous = next
				if send() != nil {
					return
				}
			}
		}
	}
}
