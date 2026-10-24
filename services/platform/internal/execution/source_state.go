package execution

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
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
func mergedSourceSelection(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, bundle *composeproject.Bundle, provider *pipeline.Provider, head string, c pipeline.Source) (pipeline.Source, bool, error) {
	if bundle == nil || bundle.Origin == nil {
		return c, false, nil
	}
	files, _ := json.Marshal(sourceFiles(bundle))
	rows, err := tx.Query(ctx, `SELECT r.result FROM git_submissions g JOIN pipeline_runs r ON r.id=g.operation_id JOIN execution_operations o ON o.id=r.id
 WHERE g.workspace_id=$1 AND g.environment_id=$2 AND o.state='succeeded'
 AND r.configuration->>'commit'=$3 AND r.configuration#>>'{connection,id}'=$4 AND r.configuration->>'branch'=$5
 AND (SELECT jsonb_object_agg(f->>'path',f->>'content') FROM jsonb_array_elements(r.configuration#>'{bundle,files}') f)=$6::jsonb
 ORDER BY r.created_at DESC`, ws, env, bundle.Origin.Commit, c.ConnectionID.String(), c.Branch, files)
	if err != nil {
		return c, false, err
	}
	reviews := []int{}
	for rows.Next() {
		var result []byte
		if err = rows.Scan(&result); err != nil {
			rows.Close()
			return c, false, err
		}
		var r pipeline.Result
		if json.Unmarshal(result, &r) == nil && r.ReviewNumber > 0 {
			reviews = append(reviews, r.ReviewNumber)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return c, false, err
	}
	for _, number := range reviews {
		merged, err := provider.MergedCommit(ctx, number)
		if err != nil {
			return c, false, err
		}
		if merged != "" && merged == head {
			return sourceSelection(c, bundle), true, nil
		}
	}
	return c, false, nil
}
