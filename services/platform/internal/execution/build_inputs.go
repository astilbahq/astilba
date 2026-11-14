package execution

import (
	"context"
	"errors"
	"maps"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/secretbroker"
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
		current, err := currentApplications(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		rows, err := db.New(tx).ListBuildRejections(ctx, db.ListBuildRejectionsParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if current[row.ApplicationID] {
				out = append(out, api.BuildRejection{ApplicationId: row.ApplicationID, Commit: row.RejectedCommit, Reason: api.BuildRejectionReason(row.RejectionReason), RejectedAt: row.RejectedAt})
			}
		}
		return nil
	})
	return out, err
}

// currentApplications is the applications the environment's draft holds, which build notices are
// shown for.
func currentApplications(ctx context.Context, tx pgx.Tx, ws, pid, env uuid.UUID) (map[uuid.UUID]bool, error) {
	v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
	if err != nil {
		return nil, err
	}
	current := make(map[uuid.UUID]bool, len(v.Spec.Applications))
	for _, app := range v.Spec.Applications {
		current[app.ID] = true
	}
	return current, nil
}

// BuildWaits is the automatic builds of the environment's current applications that wait for
// their runner.
func (s *Service) BuildWaits(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) ([]api.BuildWait, error) {
	out := []api.BuildWait{}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		current, err := currentApplications(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		rows, err := db.New(tx).ListBuildWaits(ctx, db.ListBuildWaitsParams{WorkspaceID: ws, EnvironmentID: env})
		if err != nil {
			return err
		}
		for _, row := range rows {
			if current[row.ApplicationID] {
				out = append(out, api.BuildWait{ApplicationId: row.ApplicationID, Commit: row.WaitingCommit, Reason: api.BuildWaitReason(row.WaitingReason), RunnerId: row.WaitingRunnerID, Since: row.WaitingSince, Until: row.WaitingUntil})
			}
		}
		return nil
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
		return "Runtime secrets exceed the build-input validation limit. An owner can retire secrets the environment no longer needs, or replace versions that the draft or the deployment still uses."
	default:
		return "Build inputs were rejected."
	}
}

// buildInputs keeps secret values out of public arguments before persistence and
// checks again at delivery, when the environment's runtime secrets may differ.
func (s *Service) buildInputs(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env uuid.UUID, build pipeline.BuildConfig) (map[string]string, error) {
	keys := git.keys.inspect
	secrets := make(map[string]string, len(build.Secrets))
	arguments := domain.NewSecretText(slices.Collect(maps.Values(build.Arguments))...)
	total := 0
	for name, id := range build.Secrets {
		cipher, err := db.New(tx).GetBuildSecretValue(ctx, db.GetBuildSecretValueParams{WorkspaceID: ws, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &buildInputRejection{"missing_build_secret", "build.secrets"}
		}
		if err != nil {
			return nil, err
		}
		plain, err := keys.open(secretbroker.Item{Kind: secretbroker.BuildSecret, ID: id}, cipher)
		if err != nil {
			return nil, err
		}
		total += len(plain)
		if total > 32768 {
			return nil, &buildInputRejection{"build_secret_budget", "build.secrets"}
		}
		if arguments.Exposes(string(plain)) {
			return nil, &buildInputRejection{"arguments_contain_secret", "build.argumentsContainSecret"}
		}
		secrets[name] = string(plain)
	}
	if len(build.Arguments) > 0 {
		files := make([]composeproject.File, 0, len(build.Arguments))
		for _, value := range build.Arguments {
			files = append(files, composeproject.File{Content: value})
		}
		if err := s.rejectRuntimeSecrets(ctx, tx, git, ws, env, composeproject.Bundle{Files: files}); err != nil {
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
