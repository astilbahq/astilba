package execution

import (
	"context"
	"errors"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Only failures owned by build-input validation may complete automatic refresh
// or fail work known never to have reached a runner.
type buildInputRejection struct {
	reason string
	field  string
}

func (s *Service) BuildRejections(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.BuildRejection, error) {
	out := []api.BuildRejection{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		current := make(map[uuid.UUID]bool, len(v.Spec.Applications))
		for _, app := range v.Spec.Applications {
			current[app.ID] = true
		}
		rows, err := tx.Query(ctx, `SELECT application_id,rejected_commit,rejection_reason,rejected_at FROM build_targets WHERE workspace_id=$1 AND environment_id=$2 AND rejected_at IS NOT NULL ORDER BY application_id`, ws, env)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var rejection api.BuildRejection
			if err = rows.Scan(&rejection.ApplicationId, &rejection.Commit, &rejection.Reason, &rejection.RejectedAt); err != nil {
				return err
			}
			if current[rejection.ApplicationId] {
				out = append(out, rejection)
			}
		}
		return rows.Err()
	})
	return out, err
}

func (e *buildInputRejection) Error() string { return e.note() }
func (e *buildInputRejection) Unwrap() error { return domain.Invalid(e.field) }
func (e *buildInputRejection) note() string {
	switch e.reason {
	case "missing_build_secret":
		return "A declared build-secret version is unavailable."
	case "build_secret_budget":
		return "Declared build secrets exceed the build-input limit."
	case "arguments_contain_secret":
		return "Public build arguments contain a known secret value."
	case "runtime_secret_budget":
		return "Runtime secrets exceed the build-input validation limit."
	default:
		return "Build inputs were rejected."
	}
}

// buildInputs keeps secret values out of public arguments before persistence and
// checks again at delivery, when the environment's runtime secrets may differ.
func (s *Service) buildInputs(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, build pipeline.BuildConfig) (map[string]string, error) {
	secrets := make(map[string]string, len(build.Secrets))
	total := 0
	for name, id := range build.Secrets {
		var cipher []byte
		if err := tx.QueryRow(ctx, "SELECT encrypted_value FROM build_secret_versions WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&cipher); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, &buildInputRejection{"missing_build_secret", "build.secrets"}
			}
			return nil, err
		}
		plain, err := s.Store.Keys.Open(cipher, integrationIdentity(ws, id, "build-secret"))
		if err != nil {
			return nil, err
		}
		total += len(plain)
		if total > 32768 {
			return nil, &buildInputRejection{"build_secret_budget", "build.secrets"}
		}
		for _, value := range build.Arguments {
			if len(plain) > 0 && strings.Contains(value, string(plain)) {
				return nil, &buildInputRejection{"arguments_contain_secret", "build.argumentsContainSecret"}
			}
		}
		secrets[name] = string(plain)
	}
	if len(build.Arguments) > 0 {
		files := make([]composeproject.File, 0, len(build.Arguments))
		for _, value := range build.Arguments {
			files = append(files, composeproject.File{Content: value})
		}
		if err := s.rejectRuntimeSecrets(ctx, tx, ws, env, composeproject.Bundle{Files: files}); err != nil {
			var invalid *domain.Error
			if errors.As(err, &invalid) && invalid.Code == "invalid_input" && len(invalid.Fields) == 1 {
				switch invalid.Fields[0] {
				case "source.containsRuntimeSecret":
					return nil, &buildInputRejection{"arguments_contain_secret", "build.argumentsContainSecret"}
				case "source.secretBudget":
					return nil, &buildInputRejection{"runtime_secret_budget", "build.arguments"}
				}
			}
			return nil, err
		}
	}
	return secrets, nil
}
