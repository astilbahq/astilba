package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
)

// refreshedSource is a source on the pushed branch, as the refresh found it.
type refreshedSource struct {
	env, pid, base, actor uuid.UUID
	imported              *uuid.UUID
	raw                   []byte
	commit, pending       string
	// failed is the commit a refresh last failed to import, which it does not try again.
	failed string
	// importing is whether an import of a commit other than the head is running.
	importing bool
}

// sourceOutcome is what a refresh did with a source, when nothing failed.
type sourceOutcome int

const (
	// sourceSettled needs nothing more from this job, as when the branch conflicts with the draft.
	sourceSettled sourceOutcome = iota
	// sourceAdmitted started an import.
	sourceAdmitted
	// sourceUnreadable waits for the provider to answer about its files or reviews.
	sourceUnreadable
	// sourceHeld is shown as waiting already, and the job is past its limit.
	sourceHeld
)

// refreshSource starts an import of head for one source. A failure that is the source's own
// comes back as a *sourceFailure, for the caller to roll back and record; a question for a
// provider comes back as a *providerCall.
func (w *refreshWorker) refreshSource(ctx context.Context, tx pgx.Tx, git *gitAnswers, job *river.Job[refreshArgs], connection pipeline.Connection, head string, expired bool, v refreshedSource) (sourceOutcome, error) {
	a := job.Args
	var cfg pipeline.Source
	if err := json.Unmarshal(v.raw, &cfg); err != nil {
		return sourceSettled, err
	}
	env, err := loadVisualEnvironment(ctx, tx, a.Workspace, v.pid, v.env)
	if err != nil {
		return sourceSettled, err
	}
	baseline, err := sourceBaseline(ctx, tx, a.Workspace, v.env, v.imported)
	if err != nil {
		return sourceSettled, err
	}
	clean := v.imported == nil || sameSourceFiles(env.Spec.Compose, baseline)
	if clean && v.imported != nil {
		cfg = sourceSelection(cfg, env.Spec.Compose)
	}
	if !clean {
		cfg, clean, err = mergedSourceSelection(ctx, tx, git, a.Workspace, v.env, env.Spec.Compose, head, cfg)
		var unanswered *reviewUnanswered
		if errors.As(err, &unanswered) {
			if unanswered.gone() {
				return sourceSettled, failedSource(reviewGoneReason)
			}
			// The provider may answer next time, as for files it could not read.
			return sourceUnreadable, db.New(tx).SetGitSourcePendingCommit(ctx, db.SetGitSourcePendingCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env, Commit: head})
		}
		if err != nil {
			return sourceSettled, err
		}
	}
	if !clean {
		return sourceSettled, db.New(tx).MarkGitSourceConflict(ctx, db.MarkGitSourceConflictParams{Commit: head, WorkspaceID: a.Workspace, EnvironmentID: v.env})
	}
	if expired && v.pending == head {
		// A source that waited its day for its runner to be updated fails, saying so. One whose
		// files couldn't be read stays pending, so no file is read again.
		if _, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, "", false, nil, false); runnerUpgradeRequired(err) {
			return sourceSettled, failedSource(runnerNotUpdatedReason)
		} else if err != nil && !runnerUnavailable(err) {
			return sourceSettled, err
		}
		return sourceHeld, nil
	}
	blobs, err := git.sourceBlobs(ctx, tx, a.Connection, head, cfg.Paths)
	if err != nil {
		return sourceSettled, err
	}
	if blobs.Err != nil {
		if errors.Is(blobs.Err, errSourceBlobsNotAsked) && !expired {
			return sourceUnreadable, nil
		}
		return sourceUnreadable, db.New(tx).SetGitSourcePendingCommit(ctx, db.SetGitSourcePendingCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env, Commit: head})
	}
	runner, err := selectRunner(ctx, tx, a.Workspace, cfg.RunnerID, cfg.FallbackRunnerID, "", false, nil, false)
	wait, waitErr := runnerWaits(ctx, tx, a.Workspace, cfg.RunnerID, err)
	if waitErr != nil {
		return sourceSettled, waitErr
	}
	if wait {
		return sourceSettled, errRefreshWaits
	}
	if runnerUpgradeRequired(err) {
		return sourceSettled, runnerNotUpdated(expired)
	}
	if runnerUnavailable(err) {
		return sourceSettled, failedSource(runnerUnavailableReason)
	}
	if err != nil {
		return sourceSettled, err
	}
	op, err := w.service.insertPipeline(ctx, tx, git, a.Workspace, v.env, uuid.New(), runner, pipeline.Command{Action: "import", Connection: connection, Commit: head, Branch: a.Branch, RevisionID: v.base, UnitID: env.Unit, Source: &cfg, SubmittedAt: time.Now().UTC()})
	var refused *domain.Error
	if errors.As(err, &refused) {
		return sourceSettled, importRefusal(refused, expired)
	}
	if err != nil {
		return sourceSettled, err
	}
	if err = explainImportAgain(ctx, tx, a.Workspace, v.env, op.Id, v.commit, head); err != nil {
		return sourceSettled, err
	}
	return sourceAdmitted, db.New(tx).ClearGitSourcePendingCommit(ctx, db.ClearGitSourcePendingCommitParams{WorkspaceID: a.Workspace, EnvironmentID: v.env})
}
