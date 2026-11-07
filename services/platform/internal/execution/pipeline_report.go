package execution

import (
	"context"
	"crypto/sha256"
	"encoding/json/v2"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const secretVersionPage = 64

// stoppedSubmissionWarning stays on the operation through later agent reports, because
// only the controller knows the runner may already have sent these files onward. Reports
// recognise a stopped run by this exact text, including on a replica or rollback still
// running an older release, so extend the note below rather than rewording this.
func stoppedSubmissionWarning(id uuid.UUID) string {
	return "Stopped: these files contain a known runtime secret value, or runtime secrets exceed the limit for checking them. A runner may already have pushed them to branch astilba/" + id.String() + " at the Git provider: check it and any review, and rotate any exposed secret."
}
func stoppedSubmissionNote(id uuid.UUID) string {
	return stoppedSubmissionWarning(id) + " The value may be in a file's name rather than its contents."
}

func (s *Service) rejectRuntimeSecrets(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, bundle composeproject.Bundle) error {
	q := db.New(tx)
	// A submission pushes both: the path names a tree entry, the content fills its blob.
	texts := make([]string, 0, 2*len(bundle.Files))
	for _, file := range bundle.Files {
		texts = append(texts, file.Path, file.Content)
	}
	searched := domain.NewSecretText(texts...)
	total := 0
	// Pages bound the ciphertext in memory; versions per environment are unbounded.
	var after uuid.UUID
	for {
		rows, err := q.PageEnvironmentSecretVersions(ctx, db.PageEnvironmentSecretVersionsParams{WorkspaceID: ws, EnvironmentID: env, AfterID: after, PageSize: secretVersionPage})
		if err != nil {
			return err
		}
		for _, row := range rows {
			value, err := s.Store.Keys.Open(row.EncryptedValue, store.SecretIdentity(ws, row.ID))
			if err != nil {
				return err
			}
			total += len(value)
			if total > 1<<20 {
				return domain.Invalid("source.secretBudget")
			}
			if searched.Exposes(string(value)) {
				return &domain.Error{Code: "invalid_input", Message: "A file's name or contents include a known runtime secret value.", Fields: []string{"source.containsRuntimeSecret"}}
			}
			after = row.ID
		}
		if len(rows) < secretVersionPage {
			return nil
		}
	}
}

// sourceSecretRejection names a frozen submission's failed secret check, leaving
// other errors, such as an undecryptable version, to fail the poll and be retried.
func sourceSecretRejection(err error) (string, bool) {
	var invalid *domain.Error
	if !errors.As(err, &invalid) {
		return "", false
	}
	if slices.Contains(invalid.Fields, "source.secretBudget") {
		return "This attempt was rejected before reaching a runner: runtime secrets exceed the limit for checking its files.", true
	}
	return "This attempt was rejected before reaching a runner: the names or contents of its files include a known runtime secret value. If an earlier attempt of this draft ran, its astilba/ branch may already hold them.", true
}

// hydratePipeline holds the agent's row and the operation only while it decides. Provider
// and GitHub calls run between passes, and the pass that delivers re-makes every decision
// below: a revoked or rotated agent, a cancelled or expired operation, a new runtime secret
// or a changed connection all stop the credential after the call as they would before it.
func (s *Service) hydratePipeline(ctx context.Context, a Identity, c *api.ExecutionCommand) (bool, error) {
	var deliver bool
	err := s.withGit(ctx, a.Workspace, func(git *gitAnswers) error {
		var err error
		deliver, err = s.decidePipelineDelivery(ctx, git, a, c)
		return err
	})
	return deliver, err
}
func (s *Service) decidePipelineDelivery(ctx context.Context, git *gitAnswers, a Identity, c *api.ExecutionCommand) (bool, error) {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	// Match reporting's identity-before-operation lock order. An expired poll
	// lease can overlap another poll, so its earlier operation read is not proof
	// that this command is still deliverable.
	valid, err := lockAgentCredential(ctx, tx, a)
	if err != nil {
		return false, err
	}
	if !valid {
		return false, domain.Unauthenticated
	}
	var raw []byte
	var state api.ExecutionState
	var cancelled bool
	var sequence int64
	if err = tx.QueryRow(ctx, `SELECT command,state,cancel_requested,report_sequence FROM execution_operations WHERE id=$1 AND server_id=$2 FOR UPDATE`, c.Id, a.ID).Scan(&raw, &state, &cancelled, &sequence); err != nil {
		return false, err
	}
	if Terminal(state) {
		return false, nil
	}
	pollCancelled := c.CancelRequested
	if err = json.Unmarshal(raw, c); err != nil {
		return false, err
	}
	// The dispatcher stops a command that loses its authority or subscription, but only on
	// its next run, so this pass, the last before credentials leave, checks for itself.
	refused, err := s.deliveryRefused(ctx, tx, a.Workspace, *c)
	if err != nil {
		return false, err
	}
	// This command carries no credentials, so an agent whose clock lags must not start it.
	c.CancelRequested = pollCancelled || cancelled || refused || time.Now().After(c.Deadline)
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash {
		return false, errors.New("invalid pipeline payload")
	}
	var started *bool
	if err = tx.QueryRow(ctx, "SELECT delivery_started FROM pipeline_runs WHERE id=$1", c.Id).Scan(&started); err != nil {
		return false, err
	}
	ws := a.Workspace
	queries := db.New(tx)
	undelivered := started != nil && !*started && sequence == 0
	if c.CancelRequested {
		if !undelivered {
			return true, tx.Commit(ctx)
		}
		// No runner holds this command, and its public arguments have not passed the
		// delivery check, so it finishes here rather than reaching one to be cancelled.
		if err = queries.FinishUndeliveredPipeline(ctx, db.FinishUndeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, State: "cancelled", Note: "Cancelled or expired before runner delivery."}); err != nil {
			return false, err
		}
		if err = s.enqueuePreviewForEnvironment(ctx, tx, ws, c.EnvironmentId); err != nil {
			return false, err
		}
		return false, tx.Commit(ctx)
	}
	// A plain Invalid would fail every poll and hold the runner queue. This runs before
	// provider calls and credential minting, so neither can delay or outlive a rejection.
	if p.Action == "submit" {
		if err = s.rejectRuntimeSecrets(ctx, tx, ws, c.EnvironmentId, *p.Bundle); err != nil {
			note, rejected := sourceSecretRejection(err)
			if !rejected {
				return false, err
			}
			if undelivered {
				if err = queries.FinishUndeliveredPipeline(ctx, db.FinishUndeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, State: "failed", Note: note}); err != nil {
					return false, err
				}
				return false, tx.Commit(ctx)
			}
			// A runner may hold this command, so it alone can settle it. Recording the
			// stop also records a late success from that runner as cancelled.
			if err = queries.StopPossiblyDeliveredPipeline(ctx, db.StopPossiblyDeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, Note: stoppedSubmissionNote(p.SubmissionID)}); err != nil {
				return false, err
			}
			c.CancelRequested = true
			return true, tx.Commit(ctx)
		}
	}
	if p.Build != nil && (p.Build.NeedsControls() || p.Build.UsesRailpack()) {
		if _, err = runnerEligible(ctx, tx, ws, c.ServerId, p.Build.Architecture, p.Build.NeedsControls(), p.Build.UsesRailpack()); err != nil {
			return false, err
		}
	}
	if p.CacheScope != "" {
		var raw []byte
		if err = tx.QueryRow(ctx, "SELECT capabilities FROM servers WHERE id=$1", c.ServerId).Scan(&raw); err != nil {
			return false, err
		}
		var capabilities api.AgentCapabilities
		if json.Unmarshal(raw, &capabilities) != nil || capabilities.BuildCache == nil || !*capabilities.BuildCache {
			return false, errors.New("runner no longer supports the frozen project cache assignment")
		}
	}
	current, app, err := git.connection(ctx, tx, p.Connection.ID)
	if err != nil {
		return false, err
	}
	if app {
		if current.Provider != p.Connection.Provider || current.BaseURL != p.Connection.BaseURL || current.Repository != p.Connection.Repository || current.RepositoryID != p.Connection.RepositoryID || current.Private != p.Connection.Private {
			return false, errors.New("repository identity changed")
		}
	} else {
		repository, err := git.repository(ctx, tx, p.Connection.ID)
		if err != nil {
			return false, err
		}
		if repository.Err != nil {
			return false, repository.Err
		}
		if repository.Value.ID != p.Connection.RepositoryID || repository.Value.Private != p.Connection.Private {
			return false, errors.New("repository identity changed")
		}
	}
	if err = s.populatePipeline(ctx, tx, git, ws, c.EnvironmentId, &p); err != nil {
		var rejected *buildInputRejection
		if errors.As(err, &rejected) && undelivered && p.Action == "build" && p.Preview == nil {
			if err = queries.FinishUndeliveredPipeline(ctx, db.FinishUndeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, State: "failed", Note: "Build rejected before runner delivery: " + rejected.note()}); err != nil {
				return false, err
			}
			return false, tx.Commit(ctx)
		}
		return false, err
	}
	raw, err = stablejson.Marshal(p)
	if err != nil || len(raw) > 250000 {
		return false, errors.New("pipeline payload limit exceeded")
	}
	if err = queries.MarkPipelineDeliveryStarted(ctx, db.MarkPipelineDeliveryStartedParams{WorkspaceID: ws, ID: c.Id}); err != nil {
		return false, err
	}
	// Commit before the hydrated command can leave management. A failed response
	// is still a possible delivery and cannot later justify a terminal rejection.
	c.Pipeline = rawMessage(raw)
	return true, tx.Commit(ctx)
}
func (s *Service) deliveryRefused(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) (bool, error) {
	if err := s.commandAdmission(ctx, tx, ws, c); errors.Is(err, store.SubscriptionRequired) {
		return true, nil
	} else if err != nil {
		return false, err
	}
	allowed, err := s.previewCommandAllowed(ctx, tx, nil, ws, c)
	return !allowed, err
}
func (s *Service) populatePipeline(ctx context.Context, tx pgx.Tx, git *gitAnswers, ws, env uuid.UUID, p *pipeline.Command) error {
	access, err := git.access(ctx, tx, p.Connection.ID, p.Action == "submit")
	if err != nil {
		return err
	}
	if access.Err != nil {
		return access.Err
	}
	p.Access = &pipeline.Access{}
	if p.Action == "submit" {
		p.Access.API = access.Value.API
	} else {
		p.Access.Clone = access.Value.Clone
	}
	if p.Build != nil {
		p.BuildSecrets, err = s.buildInputs(ctx, tx, ws, env, *p.Build)
		if err != nil {
			return err
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
	raw, err := stablejson.Marshal(p)
	if err != nil || len(raw) > 250000 {
		return domain.Invalid("pipeline.payloadBudget")
	}
	return nil
}

func (s *Service) writePipelineRevision(ctx context.Context, tx pgx.Tx, ws, env, base, actor uuid.UUID, spec domain.Specification) (uuid.UUID, error) {
	raw, _ := stablejson.Marshal(spec)
	parsed, canonical, err := domain.ParseSpec(raw)
	if err != nil {
		return uuid.Nil, err
	}
	if err := s.Store.ValidateApplicationRoutes(ctx, tx, ws, env, parsed, nil); err != nil {
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
func (s *Service) pipelineReport(ctx context.Context, tx pgx.Tx, git *gitAnswers, received time.Time, c api.ExecutionCommand, r *api.AgentReport) error {
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash {
		return domain.Forbidden
	}
	var ws uuid.UUID
	var cancelled bool
	var note string
	if err := tx.QueryRow(ctx, "SELECT workspace_id,cancel_requested,note FROM execution_operations WHERE id=$1", c.Id).Scan(&ws, &cancelled, &note); err != nil {
		return err
	}
	if r.State == "succeeded" && (cancelled || received.After(c.Deadline)) {
		r.State = "cancelled"
		r.Note = "Result arrived after cancellation or expiry."
		r.Pipeline = nil
	}
	if strings.HasPrefix(note, stoppedSubmissionWarning(p.SubmissionID)) {
		stopped := stoppedSubmissionNote(p.SubmissionID)
		// A cleanup instruction still needs the operator; other agent wording adds nothing.
		if r.State == "needs_attention" && r.Note != "" {
			r.Note = stopped + " " + r.Note
		} else {
			r.Note = stopped
		}
	}
	if r.Pipeline == nil {
		if r.State == "succeeded" {
			return domain.Invalid("pipeline")
		}
		return nil
	}
	// A failed import can carry more source than a result stores, and only a success uses it.
	if r.State == "succeeded" && len(*r.Pipeline) > 150000 {
		return domain.Invalid("pipeline")
	}
	var result pipeline.Result
	if json.Unmarshal(*r.Pipeline, &result) != nil || len(result.Logs) > pipeline.LogLimit || len(result.Cache) > 256 {
		return domain.Invalid("pipeline")
	}
	if result.Railpack != nil && (p.Railpack == nil || !result.Railpack.Valid(*p.Railpack)) {
		return domain.Invalid("build.railpack")
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
			var refused *domain.Error
			if errors.As(domain.ValidateNewCompose(spec), &refused) {
				r.State = "failed"
				// Compose reports what it was given, so this can hold a secret's placeholder.
				r.Note = boundedNote("Imported source needs a change at " + strings.Join(refused.Fields, ", ") + ": " + refused.Message)
				break
			}
			id, err := s.writePipelineRevision(ctx, tx, ws, c.EnvironmentId, current, actor, spec)
			if err != nil {
				r.State = "failed"
				r.Note = "Imported source is invalid for this environment."
				break
			}
			paths, _ := stablejson.Marshal(p.Source.Paths)
			entries, _ := stablejson.Marshal(p.Source.Entrypoints)
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
		case "build", "build-preview":
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
			if p.Build.UsesRailpack() && result.Railpack == nil {
				return domain.Invalid("build.railpack")
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
			raw, _ := stablejson.Marshal(artifact)
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
				head, err := git.head(ctx, tx, p.Connection.ID, p.Branch)
				if err != nil {
					return err
				}
				if head.Err != nil {
					return head.Err
				}
				if v.Revision == p.RevisionID && head.Value == p.Commit {
					if err = promoteSpecification(ctx, &v.Spec, p.AppID, artifact); err != nil {
						return err
					}
					revision, err := s.writePipelineRevision(ctx, tx, ws, c.EnvironmentId, v.Revision, actor, v.Spec)
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
	raw, _ := stablejson.Marshal(result)
	if len(raw) > 150000 {
		return domain.Invalid("pipeline")
	}
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
