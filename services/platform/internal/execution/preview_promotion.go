package execution

import (
	"context"
	"encoding/json/v2"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// promotePreview writes a preview's succeeded build into its draft and plans the draft on the
// preview host. A promotion refused for good is recorded under its key (preview_refusals.go).
func (s *Service) promotePreview(ctx context.Context, tx pgx.Tx, git *gitAnswers, p previewPolicy, r *previewRecord) error {
	id := r.View.Id
	key, err := s.previewPromotionKey(ctx, tx, p, r)
	if err != nil {
		return err
	}
	if key == r.RefusedKey {
		return stillRefused(ctx, tx, p.Workspace, r)
	}
	var raw []byte
	if err = tx.QueryRow(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", p.Workspace, *r.View.EnvironmentId, *r.Build).Scan(&raw); err != nil {
		return err
	}
	var artifact pipeline.Artifact
	if err = json.Unmarshal(raw, &artifact); err != nil {
		return err
	}
	v, err := loadVisualEnvironment(ctx, tx, p.Workspace, p.Project, *r.View.EnvironmentId)
	if err != nil {
		return err
	}
	// Only the revision is refused for good. Planning also refuses on the server's state, such
	// as a preflight that is not ready, which a later pass can find changed.
	var rev uuid.UUID
	why, err := refusedChange(ctx, tx, func(tx pgx.Tx) (err error) {
		if err = promoteSpecification(ctx, &v.Spec, *r.App, artifact); err != nil {
			return err
		}
		rev, err = s.writePipelineRevision(ctx, tx, p.Workspace, *r.View.EnvironmentId, v.Revision, p.Actor, v.Spec)
		return err
	})
	if err != nil {
		return err
	}
	if why != "" {
		return refusePreview(ctx, tx, p.Workspace, id, key, "Preview was not deployed: "+why, true)
	}
	status, err := s.createVisualPlan(ctx, tx, git.keys.inspect, p.Workspace, p.Project, *r.View.EnvironmentId, api.VisualPlanRequest{BaseRevisionId: rev, ServerId: p.Server}, true)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, "UPDATE preview_requests SET plan_id=$2,operation_id=$3,refused_key='',refusal_note='' WHERE id=$1", id, status.ID, status.OperationID); err != nil {
		return err
	}
	return s.previewState(ctx, tx, id, "deploying", "Resolving preview images and runtime state.", time.Minute)
}
