package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) rejectRuntimeSecrets(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, bundle composeproject.Bundle) error {
	rows, err := tx.Query(ctx, "SELECT v.id,v.encrypted_value FROM secret_versions v JOIN secrets s ON s.workspace_id=v.workspace_id AND s.id=v.secret_id WHERE v.workspace_id=$1 AND s.environment_id=$2", ws, env)
	if err != nil {
		return err
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var id uuid.UUID
		var cipher []byte
		if err = rows.Scan(&id, &cipher); err != nil {
			return err
		}
		value, err := s.Store.Keys.Open(cipher, store.SecretIdentity(ws, id))
		if err != nil {
			return err
		}
		total += len(value)
		if total > 1<<20 {
			return domain.Invalid("source.secretBudget")
		}
		for _, file := range bundle.Files {
			if len(value) > 0 && strings.Contains(file.Content, string(value)) {
				return domain.Invalid("source.containsRuntimeSecret")
			}
		}
	}
	return rows.Err()
}
func (s *Service) hydratePipeline(ctx context.Context, c *api.ExecutionCommand) error {
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash {
		return errors.New("invalid pipeline payload")
	}
	if c.CancelRequested || time.Now().After(c.Deadline) {
		return nil
	}
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var ws uuid.UUID
	if err = tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
		return err
	}
	if p.Build != nil && p.Build.NeedsControls() {
		if _, err = runnerEligible(ctx, tx, ws, c.ServerId, p.Build.Architecture, true); err != nil {
			return err
		}
	}
	provider, _, err := s.provider(ctx, tx, ws, p.Connection.ID)
	if err != nil {
		return err
	}
	id, private, err := provider.Repository(ctx)
	if err != nil {
		return err
	}
	if id != p.Connection.RepositoryID || private != p.Connection.Private {
		return errors.New("repository identity changed")
	}
	if err = s.populatePipeline(ctx, tx, ws, &p); err != nil {
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 250000 {
		return errors.New("pipeline payload limit exceeded")
	}
	c.Pipeline = rawMessage(raw)
	return tx.Commit(ctx)
}
func (s *Service) populatePipeline(ctx context.Context, tx pgx.Tx, ws uuid.UUID, p *pipeline.Command) error {
	_, cipher, err := loadConnection(ctx, tx, ws, p.Connection.ID)
	if err != nil {
		return err
	}
	var access pipeline.Access
	if err = s.openIntegration(ws, p.Connection.ID, "git", cipher, &access); err != nil {
		return err
	}
	p.Access = &pipeline.Access{}
	if p.Action == "submit" {
		p.Access.API = access.API
	} else {
		p.Access.Clone = access.Clone
	}
	if p.Build != nil {
		p.BuildSecrets = map[string]string{}
		for name, id := range p.Build.Secrets {
			var cipher []byte
			if err = tx.QueryRow(ctx, "SELECT encrypted_value FROM build_secret_versions WHERE workspace_id=$1 AND id=$2", ws, id).Scan(&cipher); err != nil {
				return err
			}
			plain, err := s.Store.Keys.Open(cipher, integrationIdentity(ws, id, "build-secret"))
			if err != nil {
				return err
			}
			p.BuildSecrets[name] = string(plain)
		}
		if p.Registry != nil {
			_, cipher, err := loadRegistry(ctx, tx, ws, p.Registry.ID)
			if err != nil {
				return err
			}
			var access pipeline.RegistryAccess
			if err = s.openIntegration(ws, p.Registry.ID, "registry", cipher, &access); err != nil {
				return err
			}
			p.RegistryAccess = &pipeline.RegistryAccess{Push: access.Push}
		}
	}
	raw, err := json.Marshal(p)
	if err != nil || len(raw) > 250000 {
		return domain.Invalid("pipeline.payloadBudget")
	}
	return nil
}

func writePipelineRevision(ctx context.Context, tx pgx.Tx, ws, env, base, actor uuid.UUID, spec domain.Specification) (uuid.UUID, error) {
	raw, _ := json.Marshal(spec)
	_, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		return uuid.Nil, err
	}
	id := uuid.New()
	h := sha256.Sum256(canonical)
	if _, err = db.New(tx).InsertRevision(ctx, db.InsertRevisionParams{WorkspaceID: ws, EnvironmentID: env, ID: id, AuthorID: actor, Specification: canonical, ContentHash: h[:]}); err != nil {
		return uuid.Nil, err
	}
	changed, err := tx.Exec(ctx, "UPDATE environments SET draft_revision_id=$4 WHERE workspace_id=$1 AND id=$2 AND draft_revision_id=$3", ws, env, base, id)
	if err == nil && changed.RowsAffected() != 1 {
		err = domain.Conflict
	}
	return id, err
}
func (s *Service) pipelineReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash {
		return domain.Forbidden
	}
	var ws uuid.UUID
	var cancelled bool
	if err := tx.QueryRow(ctx, "SELECT workspace_id,cancel_requested FROM execution_operations WHERE id=$1", c.Id).Scan(&ws, &cancelled); err != nil {
		return err
	}
	if r.State == "succeeded" && (cancelled || time.Now().After(c.Deadline)) {
		r.State = "cancelled"
		r.Note = "Result arrived after cancellation or expiry."
		r.Pipeline = nil
	}
	if r.Pipeline == nil {
		if r.State == "succeeded" {
			return domain.Invalid("pipeline")
		}
		return nil
	}
	if len(*r.Pipeline) > 150000 {
		return domain.Invalid("pipeline")
	}
	var result pipeline.Result
	if json.Unmarshal(*r.Pipeline, &result) != nil || len(result.Logs) > 16384 || len(result.Cache) > 256 {
		return domain.Invalid("pipeline")
	}
	if r.State == "succeeded" {
		switch p.Action {
		case "import":
			if result.Commit != p.Commit || len(result.Files) != len(p.Source.Paths) {
				return domain.Invalid("source.result")
			}
			wanted := map[string]bool{}
			for _, name := range p.Source.Paths {
				wanted[name] = true
			}
			for _, f := range result.Files {
				if !wanted[f.Path] {
					return domain.Invalid("source.result")
				}
				delete(wanted, f.Path)
			}
			var raw []byte
			var current uuid.UUID
			var actor uuid.UUID
			if err := tx.QueryRow(ctx, "SELECT e.draft_revision_id,r.specification,g.policy_actor FROM environments e JOIN specification_revisions r ON r.workspace_id=e.workspace_id AND r.id=e.draft_revision_id JOIN git_sources g ON g.workspace_id=e.workspace_id AND g.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2 FOR UPDATE OF e", ws, c.EnvironmentId).Scan(&current, &raw, &actor); err != nil {
				return err
			}
			if current != p.RevisionID {
				r.State = "failed"
				r.Note = "Draft changed during import; refresh explicitly to reconcile."
				break
			}
			spec, _, err := domain.ParseSpec(raw)
			if err != nil {
				return err
			}
			spec.Compose = &composeproject.Bundle{Namespace: p.UnitID, Entrypoints: p.Source.Entrypoints, Files: result.Files, Origin: &composeproject.Origin{Provider: p.Connection.Provider, Repository: pipeline.GitURL(p.Connection), Commit: p.Commit}}
			id, err := writePipelineRevision(ctx, tx, ws, c.EnvironmentId, current, actor, spec)
			if err != nil {
				r.State = "failed"
				r.Note = "Imported source is invalid for this environment."
				break
			}
			paths, _ := json.Marshal(p.Source.Paths)
			entries, _ := json.Marshal(p.Source.Entrypoints)
			if _, err = tx.Exec(ctx, "UPDATE git_sources SET imported_revision_id=$3,imported_commit=$4,pending_commit='',status='ready',configuration=jsonb_set(jsonb_set(configuration,'{paths}',$5::jsonb),'{entrypoints}',$6::jsonb) WHERE workspace_id=$1 AND environment_id=$2", ws, c.EnvironmentId, id, p.Commit, paths, entries); err != nil {
				return err
			}
			if p.Source.AutoDeploy {
				if err = s.enqueueAutomatic(ctx, tx, c.Id, ws, c.EnvironmentId, id, actor); err != nil {
					return err
				}
			}

		case "submit":
			u, err := url.Parse(result.ReviewURL)
			expected, _ := url.Parse(p.Connection.BaseURL)
			host := expected.Host
			if p.Connection.Provider == "github" {
				host = "github.com"
			}
			if err != nil || u.Host != host || u.User != nil || u.Scheme != expected.Scheme || result.ReviewNumber < 1 || !pipeline.ValidCommit(result.Commit) {
				return domain.Invalid("review")
			}
			if _, err = tx.Exec(ctx, "UPDATE git_sources SET status='awaiting_merge' WHERE workspace_id=$1 AND environment_id=$2", ws, c.EnvironmentId); err != nil {
				return err
			}
		case "build":
			var hash, commit string
			err := tx.QueryRow(ctx, "SELECT input_hash,requested_commit FROM build_targets WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", ws, c.EnvironmentId, p.AppID).Scan(&hash, &commit)
			if err != nil {
				return err
			}
			if hash != p.InputHash || commit != p.Commit {
				r.State = "failed"
				r.Note = "Build inputs changed; this result cannot become an artifact."
				break
			}
			if result.Skipped {
				if p.CompareCommit == "" || len(p.Build.WatchPaths) == 0 || result.Commit != p.Commit || result.Image != "" || result.Digest != "" {
					return domain.Invalid("build.skipped")
				}
				r.Note = "No watched paths changed since the last compatible successful build. No image was built or deployed."
				break
			}
			if result.Commit != p.Commit || result.Architecture != p.Build.Architecture || !validImageDigest(result.Digest) {
				return domain.Invalid("artifact")
			}
			artifact := pipeline.Artifact{ID: c.Id, BuildID: c.Id, Image: result.Image, Digest: result.Digest, Architecture: result.Architecture, InputHash: p.InputHash, Commit: p.Commit}
			if p.Registry != nil {
				if result.Image != p.Registry.Repository+"@"+result.Digest {
					return domain.Invalid("artifact.image")
				}
				artifact.RegistryID = &p.Registry.ID
			} else {
				if result.Image != "astilba-build-"+c.Id.String()+":built" {
					return domain.Invalid("artifact.image")
				}
				artifact.ServerID = &c.ServerId
			}
			raw, _ := json.Marshal(artifact)
			if _, err = tx.Exec(ctx, "INSERT INTO build_artifacts(id,workspace_id,environment_id,identity) VALUES($1,$2,$3,$4)", c.Id, ws, c.EnvironmentId, raw); err != nil {
				return err
			}
			var automatic bool
			if err = tx.QueryRow(ctx, "SELECT COALESCE((configuration->>'autoDeploy')::boolean,false) FROM build_targets WHERE workspace_id=$1 AND environment_id=$2 AND application_id=$3", ws, c.EnvironmentId, p.AppID).Scan(&automatic); err != nil {
				return err
			}
			if p.Build.AutoDeploy && automatic {
				var pid, actor uuid.UUID
				if err = tx.QueryRow(ctx, "SELECT e.project_id,b.policy_actor FROM environments e JOIN build_targets b ON b.workspace_id=e.workspace_id AND b.environment_id=e.id WHERE e.workspace_id=$1 AND e.id=$2 AND b.application_id=$3", ws, c.EnvironmentId, p.AppID).Scan(&pid, &actor); err != nil {
					return err
				}
				v, err := loadVisualEnvironment(ctx, tx, ws, pid, c.EnvironmentId)
				if err != nil {
					return err
				}
				provider, _, err := s.provider(ctx, tx, ws, p.Connection.ID)
				if err != nil {
					return err
				}
				head, err := provider.Resolve(ctx, p.Branch)
				if err != nil {
					return err
				}
				if v.Revision == p.RevisionID && head == p.Commit {
					if err = promoteSpecification(ctx, &v.Spec, p.AppID, artifact); err != nil {
						return err
					}
					revision, err := writePipelineRevision(ctx, tx, ws, c.EnvironmentId, v.Revision, actor, v.Spec)
					if err != nil {
						return err
					}
					if err = s.enqueueAutomatic(ctx, tx, c.Id, ws, c.EnvironmentId, revision, actor); err != nil {
						return err
					}
				}
			}

		}
	}
	// Selected source stays in immutable revisions, rather than duplicating source files in history.
	result.Files = nil
	raw, _ := json.Marshal(result)
	_, err := tx.Exec(ctx, "UPDATE pipeline_runs SET result=$2 WHERE id=$1", c.Id, raw)
	return err
}
func validImageDigest(v string) bool {
	if len(v) != 71 || !strings.HasPrefix(v, "sha256:") {
		return false
	}
	for _, c := range v[7:] {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}
