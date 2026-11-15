package execution

import (
	"context"
	"encoding/json/v2"
	"maps"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func sourceBaseline(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, revision *uuid.UUID) (*composeproject.Bundle, error) {
	if revision == nil {
		return nil, nil
	}
	var raw []byte
	if err := tx.QueryRow(ctx, "SELECT specification FROM specification_revisions WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, env, *revision).Scan(&raw); err != nil {
		return nil, err
	}
	var spec domain.Specification
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	return spec.Compose, nil
}
func sourceFiles(b *composeproject.Bundle) map[string]string {
	out := map[string]string{}
	if b != nil {
		for _, f := range b.Files {
			out[f.Path] = f.Content
		}
	}
	return out
}
func sameSourceFiles(a, b *composeproject.Bundle) bool {
	return a != nil && b != nil && maps.Equal(sourceFiles(a), sourceFiles(b))
}
func sourceSelection(c pipeline.Source, b *composeproject.Bundle) pipeline.Source {
	if b != nil {
		c.Paths = []string{}
		for _, f := range b.Files {
			c.Paths = append(c.Paths, f.Path)
		}
		c.Entrypoints = b.Entrypoints
	}
	return c
}

// Only the provider's merged commit authorizes replacing edited Git files.
func mergedSourceSelection(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env uuid.UUID, bundle *composeproject.Bundle, head string, c pipeline.Source) (pipeline.Source, bool, error) {
	if bundle == nil || bundle.Origin == nil {
		return c, false, nil
	}
	files, _ := stablejson.Marshal(sourceFiles(bundle))
	numbers, err := db.New(tx).SubmittedReviews(ctx, db.SubmittedReviewsParams{WorkspaceID: ws, EnvironmentID: env, Commit: bundle.Origin.Commit, ConnectionID: c.ConnectionID.String(), Branch: c.Branch, Files: files})
	if err != nil || len(numbers) == 0 {
		return c, false, err
	}
	reviews := make([]int, 0, len(numbers))
	for _, n := range numbers {
		reviews = append(reviews, int(n))
	}
	merged, err := git.mergedAt(ctx, tx, c.ConnectionID, reviews, head)
	if err != nil {
		return c, false, err
	}
	if merged.Err != nil {
		return c, false, &reviewUnanswered{merged.Err}
	}
	if merged.Value {
		return sourceSelection(c, bundle), true, nil
	}
	return c, false, nil
}
