package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/url"
	"slices"
	"strconv"
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
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
)

// Management submits a saved draft itself, through the provider's API. A runner builds code that
// may be hostile, so it never receives a credential that can write to the repository: a build
// escape would otherwise push to a tracked branch, and automatic deployment would ship it.

// submissionConfiguration is the frozen draft of one attempt, without any credential. It keeps the
// field names runner submissions used, so SubmittedReviews reads both alike.
type submissionConfiguration struct {
	SubmissionID uuid.UUID             `json:"submissionId"`
	Connection   pipeline.Connection   `json:"connection"`
	Commit       string                `json:"commit"`
	Branch       string                `json:"branch"`
	Bundle       composeproject.Bundle `json:"bundle"`
	// Paths are the source's selected files at Commit.
	Paths       []string  `json:"paths"`
	SubmittedAt time.Time `json:"submittedAt"`
}

type submissionOutcome struct {
	Commit       string `json:"commit"`
	ReviewNumber int    `json:"reviewNumber"`
	ReviewURL    string `json:"reviewUrl"`
}

const (
	// submissionAttempts bounds how often a provider that fails or cannot be reached is asked again.
	submissionAttempts = 5
	// submissionTimeout bounds one attempt, inside workspaceTurnStale. The provider's work ends
	// recordTimeout before it, so the attempt always has time to record what happened.
	submissionTimeout = time.Minute
	recordTimeout     = 10 * time.Second
	// submissionLifetime is far longer than every attempt with River's backoff between them, so an
	// attempt still queued after it has lost its job, and a new one may take its place. No attempt
	// starts within submissionTimeout of it, so none is still writing when another replaces it.
	submissionLifetime = time.Hour
)

func submissionView(id, revision uuid.UUID, state, note string, result []byte, created time.Time) api.SourceSubmission {
	out := api.SourceSubmission{Id: id, RevisionId: revision, State: api.SourceSubmissionState(state), Note: note, CreatedAt: created}
	var outcome submissionOutcome
	if len(result) > 0 && json.Unmarshal(result, &outcome) == nil && outcome.ReviewNumber > 0 {
		out.Commit, out.ReviewNumber, out.ReviewUrl = &outcome.Commit, &outcome.ReviewNumber, &outcome.ReviewURL
	}
	return out
}

// latestSubmission is the newest attempt for the environment, or nil when there is none.
func latestSubmission(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID) (*api.SourceSubmission, error) {
	row, err := db.New(tx).LatestSourceSubmission(ctx, db.LatestSourceSubmissionParams{WorkspaceID: ws, EnvironmentID: env})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	v := submissionView(row.ID, row.RevisionID, row.State, row.Note, row.Result, row.CreatedAt)
	return &v, nil
}

// SubmitSource queues an attempt to propose the saved draft as one branch and review. An attempt
// that is queued or succeeded for the revision answers a new request; a failed one is tried again
// with the same branch, author date and review.
func (s *Service) SubmitSource(ctx context.Context, p store.Principal, ws, pid, env, base, key uuid.UUID) (api.SourceSubmission, error) {
	var out api.SourceSubmission
	err := s.gitScope(ctx, p, ws, workspaceAccess{Write: true}, "git.source.submit", base, func(tx pgx.Tx, git *gitAnswers) error {
		q := db.New(tx)
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		if old, err := q.SourceSubmissionByRequestKey(ctx, db.SourceSubmissionByRequestKeyParams{WorkspaceID: ws, RequestKey: key}); err == nil {
			if old.EnvironmentID != env || old.RevisionID != base {
				return domain.Conflict
			}
			out = submissionView(old.ID, old.RevisionID, old.State, old.Note, old.Result, old.CreatedAt)
			return nil
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if v.Revision != base || v.Spec.Compose == nil {
			return domain.Conflict
		}
		var source pipeline.Source
		var raw []byte
		var imported *uuid.UUID
		var commit string
		if err = tx.QueryRow(ctx, "SELECT configuration,imported_revision_id,imported_commit FROM git_sources WHERE workspace_id=$1 AND environment_id=$2 FOR UPDATE", ws, env).Scan(&raw, &imported, &commit); err != nil {
			return err
		}
		if err = json.Unmarshal(raw, &source); err != nil {
			return err
		}
		baseline, err := sourceBaseline(ctx, tx, ws, env, imported)
		if err != nil {
			return err
		}
		if imported == nil || sameSourceFiles(v.Spec.Compose, baseline) || v.Spec.Compose.Origin == nil || v.Spec.Compose.Origin.Commit != commit {
			return domain.Invalid("source.draft")
		}
		submittedAt := time.Now().UTC()
		prior, err := q.LatestSourceSubmission(ctx, db.LatestSourceSubmissionParams{WorkspaceID: ws, EnvironmentID: env, RevisionID: &base})
		if err == nil {
			abandoned := prior.State == "queued" && time.Since(prior.CreatedAt) > submissionLifetime
			if prior.State != "failed" && !abandoned {
				out = submissionView(prior.ID, prior.RevisionID, prior.State, prior.Note, prior.Result, prior.CreatedAt)
				return nil
			}
			if abandoned {
				if err = q.FinishSourceSubmission(ctx, db.FinishSourceSubmissionParams{WorkspaceID: ws, ID: prior.ID, State: "failed", Note: "No attempt finished within an hour, so another took its place."}); err != nil {
					return err
				}
			}
			var frozen submissionConfiguration
			if err = json.Unmarshal(prior.Configuration, &frozen); err != nil {
				return err
			}
			submittedAt = frozen.SubmittedAt
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		head, err := git.head(ctx, tx, source.ConnectionID, source.Branch)
		if err != nil {
			return err
		}
		if head.Err != nil {
			return head.failure(domain.Invalid("source.branch"))
		}
		if head.Value != commit {
			return domain.Conflict
		}
		// A baseline file's name is already in the repository, and a draft must stay able to
		// rename it away, so only the names being submitted are checked.
		files := slices.Clone(v.Spec.Compose.Files)
		for _, file := range baseline.Files {
			files = append(files, composeproject.File{Content: file.Content})
		}
		if err = s.rejectRuntimeSecrets(ctx, tx, ws, env, composeproject.Bundle{Files: files}); err != nil {
			return err
		}
		frozen := submissionConfiguration{SubmissionID: uuid.NewSHA1(env, []byte("source/"+base.String())), Connection: head.Connection, Commit: commit, Branch: source.Branch, Bundle: *v.Spec.Compose, Paths: source.Paths, SubmittedAt: submittedAt}
		raw, err = stablejson.Marshal(frozen)
		if err != nil || len(raw) > 150000 {
			return domain.Invalid("source.draft")
		}
		id := uuid.New()
		created, err := q.InsertSourceSubmission(ctx, db.InsertSourceSubmissionParams{ID: id, WorkspaceID: ws, EnvironmentID: env, RevisionID: base, RequestKey: key, ActorID: p.Account.ID, Configuration: raw})
		if err != nil {
			return err
		}
		if err = s.insertGitJob(ctx, tx, workspaceMetadata{Workspace: ws}, submissionArgs{Workspace: ws, ID: id}, river.InsertOpts{MaxAttempts: submissionAttempts}, false); err != nil {
			return err
		}
		out = api.SourceSubmission{Id: id, RevisionId: base, State: api.SourceSubmissionState("queued"), Note: "", CreatedAt: created}
		return nil
	})
	return out, err
}

type submissionArgs struct {
	Workspace uuid.UUID `json:"workspace"`
	ID        uuid.UUID `json:"id"`
}

func (submissionArgs) Kind() string                 { return "platform_source_submission_v1" }
func (submissionArgs) InsertOpts() river.InsertOpts { return river.InsertOpts{Queue: gitQueue} }

type submissionWorker struct {
	river.WorkerDefaults[submissionArgs]
	service *Service
}

func (*submissionWorker) Timeout(*river.Job[submissionArgs]) time.Duration { return submissionTimeout }

// submissionWork is what an attempt writes with, decided under the workspace lock and used with
// nothing held.
type submissionWork struct {
	provider   *pipeline.Provider
	submission pipeline.Submission
}

func (w *submissionWorker) Work(ctx context.Context, job *river.Job[submissionArgs]) error {
	s := w.service
	if err := s.workspaceTurn(ctx, job.JobRow, job.Args.Workspace); err != nil {
		return err
	}
	err := s.attemptSubmission(ctx, job)
	// River discards the job after its last attempt, so that attempt settles its submission
	// whatever stopped it, rather than leaving it waiting with nothing to run it.
	if err != nil && job.Attempt >= submissionAttempts {
		settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		defer cancel()
		return errors.Join(err, s.settleSubmission(settle, job.Args.Workspace, job.Args.ID))
	}
	return err
}

func (s *Service) attemptSubmission(ctx context.Context, job *river.Job[submissionArgs]) error {
	var work *submissionWork
	err := s.withGit(ctx, job.Args.Workspace, func(git *gitAnswers) error {
		var err error
		work, err = s.prepareSubmission(ctx, git, job)
		return err
	})
	if err != nil || work == nil {
		return err
	}
	// River gives every attempt submissionTimeout, so the context always has a deadline.
	deadline, _ := ctx.Deadline()
	submitCtx, cancel := context.WithDeadline(ctx, deadline.Add(-recordTimeout))
	result, err := work.provider.Submit(submitCtx, work.submission)
	cancel()
	record, cancel := context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
	defer cancel()
	return s.finishSubmission(record, job, result, err)
}

// settleSubmission fails a submission still queued once its job can run no more attempts.
func (s *Service) settleSubmission(ctx context.Context, ws, id uuid.UUID) error {
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
	if err = db.New(tx).FinishSourceSubmission(ctx, db.FinishSourceSubmissionParams{WorkspaceID: ws, ID: id, State: "failed", Note: "Not submitted: the last attempt could not finish. Submit the draft again."}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// submissionFailure says why an attempt failed and whether trying again could change it. A
// refusal names what to do; other errors only say what failed, since a provider's own message is
// not shown.
func submissionFailure(err error) (string, bool) {
	var refused *pipeline.SubmissionRefused
	if errors.As(err, &refused) {
		return refused.Reason, true
	}
	var invalid *domain.Error
	if errors.As(err, &invalid) {
		return "The connection's credential cannot be used: " + invalid.Message, true
	}
	var status *pipeline.ProviderError
	if errors.As(err, &status) {
		return "The Git provider refused a request with HTTP " + strconv.Itoa(status.Status) + ". Check that the connection's API credential can create branches, commits and reviews.", !pipeline.SubmissionRetryable(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "The Git provider did not finish answering in time.", false
	}
	return "The Git provider could not be reached or gave an answer that could not be used.", false
}

// prepareSubmission makes every decision again under the workspace lock: the attempt is still
// queued, the workspace may write, no runtime secret is in the files and the connection is the
// one the draft was submitted through. It returns nil once the attempt is settled.
func (s *Service) prepareSubmission(ctx context.Context, git *gitAnswers, job *river.Job[submissionArgs]) (*submissionWork, error) {
	ws, id := job.Args.Workspace, job.Args.ID
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return nil, err
	}
	defer done()
	q := db.New(tx)
	row, err := q.LockSourceSubmission(ctx, db.LockSourceSubmissionParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if row.State != "queued" {
		return nil, nil
	}
	fail := func(note string) (*submissionWork, error) {
		if err := q.FinishSourceSubmission(ctx, db.FinishSourceSubmissionParams{WorkspaceID: ws, ID: id, State: "failed", Note: boundedNote("Not submitted: " + note)}); err != nil {
			return nil, err
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return nil, err
		}
		return nil, tx.Commit(ctx)
	}
	if time.Since(row.CreatedAt) > submissionLifetime-submissionTimeout {
		return fail("no attempt finished within an hour. Submit the draft again.")
	}
	var c submissionConfiguration
	if err = json.Unmarshal(row.Configuration, &c); err != nil {
		return nil, err
	}
	if err = s.Store.ManagementAdmission(ctx, tx, ws); err != nil {
		if errors.Is(err, store.SubscriptionRequired) {
			return fail("this workspace needs an active subscription.")
		}
		return nil, err
	}
	// A value can become a runtime secret while the attempt waits.
	if err = s.rejectRuntimeSecrets(ctx, tx, ws, row.EnvironmentID, c.Bundle); err != nil {
		var invalid *domain.Error
		if !errors.As(err, &invalid) {
			return nil, err
		}
		if slices.Contains(invalid.Fields, "source.secretBudget") {
			return fail("runtime secrets exceed the limit for checking these files. Nothing was written to the repository.")
		}
		return fail("the names or contents of these files include a known runtime secret value. Nothing was written to the repository.")
	}
	current, _, err := git.connection(ctx, tx, c.Connection.ID)
	if err != nil {
		return nil, err
	}
	current.Name = c.Connection.Name
	if current != c.Connection {
		return fail("the Git connection changed after the draft was submitted. Submit it again.")
	}
	access, err := git.access(ctx, tx, c.Connection.ID, true)
	if err != nil {
		return nil, err
	}
	if access.Err != nil {
		note, terminal := submissionFailure(access.Err)
		if terminal || job.Attempt >= submissionAttempts {
			return fail(note)
		}
		if err = q.NoteSourceSubmission(ctx, db.NoteSourceSubmissionParams{WorkspaceID: ws, ID: id, Note: boundedNote(retryNote(job.Attempt, note))}); err != nil {
			return nil, err
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, err
		}
		return nil, access.Err
	}
	if access.Value.API.Password == "" {
		return fail("the connection has no API credential, which submitting needs to create the branch and review.")
	}
	provider, err := pipeline.NewProvider(current, access.Value.API, s.Policy)
	if err != nil {
		return fail("the connection's provider is not allowed by this installation's policy.")
	}
	work := &submissionWork{provider: provider, submission: pipeline.Submission{ID: c.SubmissionID, Base: c.Commit, Branch: c.Branch, Files: c.Bundle.Files, Previous: c.Paths, At: c.SubmittedAt}}
	return work, tx.Commit(ctx)
}

func retryNote(attempt int, note string) string {
	return "Attempt " + strconv.Itoa(attempt) + " of " + strconv.Itoa(submissionAttempts) + " failed and will be retried: " + note
}

// finishSubmission records what the provider answered, if the attempt is still queued. A failure
// that another attempt could change leaves it queued for River to retry.
func (s *Service) finishSubmission(ctx context.Context, job *river.Job[submissionArgs], result pipeline.SubmissionResult, submitErr error) error {
	ws, id := job.Args.Workspace, job.Args.ID
	tx, done, err := s.Store.BeginWorkspaceWrite(ctx, ws)
	if err != nil {
		return err
	}
	defer done()
	q := db.New(tx)
	row, err := q.LockSourceSubmission(ctx, db.LockSourceSubmissionParams{WorkspaceID: ws, ID: id})
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	finish := func(state, note string, outcome []byte) error {
		if err == nil && row.State == "queued" {
			if err := q.FinishSourceSubmission(ctx, db.FinishSourceSubmissionParams{WorkspaceID: ws, ID: id, State: state, Note: boundedNote(note), Result: outcome}); err != nil {
				return err
			}
			if state == "succeeded" {
				if err := q.AwaitSubmittedMerge(ctx, db.AwaitSubmittedMergeParams{WorkspaceID: ws, EnvironmentID: row.EnvironmentID}); err != nil {
					return err
				}
			}
		}
		if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if err != nil || row.State != "queued" {
		return finish("", "", nil)
	}
	if submitErr == nil {
		var c submissionConfiguration
		if err = json.Unmarshal(row.Configuration, &c); err != nil {
			return err
		}
		if !reviewURLMatches(c.Connection, result.Review.URL) {
			return finish("failed", "Not recorded: the Git provider named a review outside the connection's own site. Check branch astilba/"+c.SubmissionID.String()+" and its review at the provider.", nil)
		}
		outcome, err := stablejson.Marshal(submissionOutcome{Commit: result.Commit, ReviewNumber: result.Review.Number, ReviewURL: result.Review.URL})
		if err != nil {
			return err
		}
		return finish("succeeded", "", outcome)
	}
	note, terminal := submissionFailure(submitErr)
	if terminal || job.Attempt >= submissionAttempts {
		return finish("failed", "Not submitted: "+note, nil)
	}
	if err = q.NoteSourceSubmission(ctx, db.NoteSourceSubmissionParams{WorkspaceID: ws, ID: id, Note: boundedNote(retryNote(job.Attempt, note))}); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	return submitErr
}

// reviewURLMatches accepts a review link only on the connection's own site, so a link the
// dashboard shows can't lead anywhere else.
func reviewURLMatches(c pipeline.Connection, review string) bool {
	u, err := url.Parse(review)
	expected, _ := url.Parse(c.BaseURL)
	host := expected.Host
	if c.Provider == "github" {
		host = "github.com"
	}
	return err == nil && u.Host == host && u.User == nil && u.Scheme == expected.Scheme
}
