package execution

import (
	"context"
	"encoding/hex"
	"encoding/json"
	db "github.com/astilbahq/astilba/services/platform/db/generated"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) PromoteArtifact(ctx context.Context, p store.Principal, ws, pid, env, id uuid.UUID, in api.PromoteArtifact) (api.Revision, error) {
	var out api.Revision
	err := s.scope(ctx, p, ws, true, false, false, "artifact.promote", id, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if v.Revision != in.BaseRevisionId {
			return domain.Conflict
		}
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&raw); err != nil {
			return err
		}
		var a pipeline.Artifact
		if err = json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if a.InputHash != in.InputHash {
			return domain.Conflict
		}
		if a.ServerID != nil && v.Server != nil && *a.ServerID != *v.Server {
			return domain.Invalid("artifact.server")
		}
		if err = promoteSpecification(ctx, &v.Spec, in.ApplicationId, a); err != nil {
			return err
		}
		revision, err := writePipelineRevision(ctx, tx, ws, env, v.Revision, p.Account.ID, v.Spec)
		if err != nil {
			return err
		}
		row, err := db.New(tx).GetRevision(ctx, db.GetRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: revision})
		if err != nil {
			return err
		}
		out = api.Revision{Id: row.ID, AuthorId: row.AuthorID, CreatedAt: row.CreatedAt, Hash: hex.EncodeToString(row.ContentHash), Specification: json.RawMessage(row.Specification)}
		return nil
	})
	return out, err
}
func (s *Service) planArtifacts(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p *deployment.Plan) error {
	p.Artifacts = map[string]pipeline.Artifact{}
	for _, app := range p.Specification.Applications {
		var raw []byte
		if app.Source.ArtifactID != nil {
			if err := tx.QueryRow(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 AND id=$2", ws, *app.Source.ArtifactID).Scan(&raw); err != nil {
				return domain.Invalid("artifactId")
			}
		} else if p.Specification.Compose != nil && app.Source.Image != nil {
			err := tx.QueryRow(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 AND identity->>'image'=$2 ORDER BY created_at DESC LIMIT 1", ws, *app.Source.Image).Scan(&raw)
			if err != nil && err != pgx.ErrNoRows {
				return err
			}
		}
		if len(raw) == 0 {
			continue
		}
		var a pipeline.Artifact
		if err := json.Unmarshal(raw, &a); err != nil {
			return err
		}
		if app.Source.Image == nil || *app.Source.Image != a.Image || a.ServerID != nil && *a.ServerID != p.ServerID {
			return domain.Invalid("artifact.image")
		}
		var arch string
		if err := tx.QueryRow(ctx, "SELECT capabilities->>'arch' FROM servers WHERE workspace_id=$1 AND id=$2", ws, p.ServerID).Scan(&arch); err != nil {
			return err
		}
		if arch != a.Architecture {
			return domain.Invalid("artifact.architecture")
		}
		p.Artifacts[app.ID.String()] = a
	}
	return nil
}

func promoteSpecification(ctx context.Context, spec *domain.Specification, appID uuid.UUID, a pipeline.Artifact) error {
	for i := range spec.Applications {
		app := &spec.Applications[i]
		if app.ID != appID {
			continue
		}
		if app.Postgres != nil {
			return domain.Invalid("artifact.application")
		}
		if spec.Compose != nil {
			b, err := composeproject.EditImage(ctx, *spec.Compose, domain.VariableValues(spec.Variables, nil), app.Name, a.Image)
			if err != nil {
				return err
			}
			spec.Compose = &b
		} else {
			app.Source = domain.ApplicationSource{Kind: "image", Image: &a.Image, ArtifactID: &a.ID}
		}
		return nil
	}
	return domain.Invalid("applicationId")
}

func (s *Service) Artifacts(ctx context.Context, p store.Principal, ws uuid.UUID) ([]pipeline.Artifact, error) {
	out := []pipeline.Artifact{}
	err := s.scope(ctx, p, ws, false, false, false, "", uuid.Nil, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, "SELECT identity FROM build_artifacts WHERE workspace_id=$1 ORDER BY created_at DESC LIMIT 100", ws)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var raw []byte
			var a pipeline.Artifact
			if err = rows.Scan(&raw); err != nil {
				return err
			}
			if err = json.Unmarshal(raw, &a); err != nil {
				return err
			}
			out = append(out, a)
		}
		return rows.Err()
	})
	return out, err
}
