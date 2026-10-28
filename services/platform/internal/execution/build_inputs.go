package execution

import (
	"context"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// buildInputs keeps secret values out of public arguments before persistence and
// checks again at delivery, when the environment's runtime secrets may differ.
func (s *Service) buildInputs(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, build pipeline.BuildConfig) (map[string]string, error) {
	secrets := make(map[string]string, len(build.Secrets))
	total := 0
	for name, id := range build.Secrets {
		var cipher []byte
		if err := tx.QueryRow(ctx, "SELECT encrypted_value FROM build_secret_versions WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&cipher); err != nil {
			return nil, domain.Invalid("build.secrets")
		}
		plain, err := s.Store.Keys.Open(cipher, integrationIdentity(ws, id, "build-secret"))
		if err != nil {
			return nil, err
		}
		total += len(plain)
		if total > 32768 {
			return nil, domain.Invalid("build.secrets")
		}
		for _, value := range build.Arguments {
			if len(plain) > 0 && strings.Contains(value, string(plain)) {
				return nil, domain.Invalid("build.argumentsContainSecret")
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
			return nil, err
		}
	}
	return secrets, nil
}
