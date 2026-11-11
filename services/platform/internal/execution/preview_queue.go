package execution

import (
	"context"
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// enqueuePreview adds a preview job unless the same one is already waiting to run. Each job reads
// the provider and its records afresh, so a waiting one does what a second would.
func (s *Service) enqueuePreview(ctx context.Context, tx pgx.Tx, ws uuid.UUID, args previewArgs) error {
	return s.insertGitJob(ctx, tx, workspaceMetadata{Workspace: ws}, args, river.InsertOpts{MaxAttempts: 20}, true)
}
func (s *Service) enqueueConnectionPreviews(ctx context.Context, tx pgx.Tx, ws, connection uuid.UUID, number int) error {
	rows, err := tx.Query(ctx, "SELECT id FROM preview_policies WHERE workspace_id=$1 AND connection_id=$2 AND enabled ORDER BY id LIMIT 100", ws, connection)
	if err != nil {
		return err
	}
	ids := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	// A workspace can sign as many request events as it likes, naming numbers its provider may not
	// have. Past previewEventLimit such jobs waiting, an event adds nothing: discovery, on its own
	// cycle, finds the provider's open requests and no others.
	filed := workspaceMetadata{Workspace: ws, Event: true}
	events, err := stablejson.Marshal(filed)
	if err != nil {
		return err
	}
	waiting, err := s.Queue.JobListTx(ctx, tx, river.NewJobListParams().Queues(gitQueue).Kinds(previewArgs{}.Kind()).States(queuedStates...).
		Metadata(string(events)).First(previewEventLimit))
	if err != nil || len(waiting.Jobs) >= previewEventLimit {
		return err
	}
	for _, id := range ids {
		if err = s.insertGitJob(ctx, tx, filed, previewArgs{Policy: id, Number: number}, river.InsertOpts{MaxAttempts: 20}, true); err != nil {
			return err
		}
	}
	return nil
}

// previewEventLimit is one discovery page of request events, and previewDiscoveryLimit two pages
// of request jobs from any source, past which discovery waits for its next cycle.
const (
	previewEventLimit     = 50
	previewDiscoveryLimit = 100
)

// previewBacklogFull reports whether a workspace has previewDiscoveryLimit request jobs waiting.
func (s *Service) previewBacklogFull(ctx context.Context, tx pgx.Tx, ws uuid.UUID) (bool, error) {
	filed, err := stablejson.Marshal(workspaceMetadata{Workspace: ws})
	if err != nil {
		return false, err
	}
	waiting, err := s.Queue.JobListTx(ctx, tx, river.NewJobListParams().Queues(gitQueue).Kinds(previewArgs{}.Kind()).States(queuedStates...).
		Metadata(string(filed)).Where("(args->>'number')::int <> 0").First(previewDiscoveryLimit))
	return len(waiting.Jobs) >= previewDiscoveryLimit, err
}

func (s *Service) enqueuePreviewForEnvironment(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID) error {
	var id uuid.UUID
	err := tx.QueryRow(ctx, "SELECT id FROM preview_requests WHERE workspace_id=$2 AND environment_id=$1", env, ws).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.enqueuePreview(ctx, tx, ws, previewArgs{Request: id})
}
