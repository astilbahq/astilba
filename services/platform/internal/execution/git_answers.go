package execution

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// providerCall is returned by a transaction body that needs a network answer it does not
// have yet. Returning it rolls the transaction back, so its locks and connection are free
// while fetch runs. A tenant's own slow Git server can hold fetch for as long as its client
// allows, and nothing else may wait on that.
type providerCall struct{ fetch func(context.Context) }

func (*providerCall) Error() string { return "provider answer needed" }

// and returns a call making both calls, for a body that can ask several questions in one
// pass rather than spending a pass on each.
func (c *providerCall) and(other *providerCall) *providerCall {
	if c == nil {
		return other
	}
	return &providerCall{fetch: func(ctx context.Context) {
		c.fetch(ctx)
		other.fetch(ctx)
	}}
}

// providerPassLimit bounds the passes of one operation. A real operation asks only a few
// separate questions, so reaching this means the state is changing faster than it can
// settle: a pass after a change discards the answers and asks again.
const providerPassLimit = 8

// withProviderCalls runs body until it stops asking for a network answer, giving up with
// domain.Conflict at the limit. Each pass starts from fresh reads under fresh locks, so a
// decision that uses an answer is made from the state at commit, never from what a pass saw
// before the call. Rollback undoes only the database, so a body must have no other effect
// before asking, and it cannot ask after committing, when its transaction is closed.
func withProviderCalls(ctx context.Context, body func() error) error {
	for pass := 1; ; pass++ {
		err := body()
		var call *providerCall
		if !errors.As(err, &call) {
			return err
		}
		if pass == providerPassLimit {
			return domain.Conflict
		}
		call.fetch(ctx)
	}
}

// gitState is what a provider's answer was about: which repository, which credential and
// which GitHub App grant. Rotating, repointing or revoking any of them while a call was
// outstanding changes this value, and the answer is discarded rather than trusted.
type gitState struct {
	connection               pipeline.Connection
	configuration, encrypted []byte
	binding                  *db.ShareActiveGitHubAppConnectionRow
	// unusable records a connection that cannot authenticate, found without a network call.
	unusable error
}

func (a gitState) same(b gitState) bool {
	if !bytes.Equal(a.configuration, b.configuration) || !bytes.Equal(a.encrypted, b.encrypted) || (a.binding == nil) != (b.binding == nil) {
		return false
	}
	return a.binding == nil || *a.binding == *b.binding
}

// loadGitState shares the App binding until the transaction ends, so a revocation cannot
// commit between this check and whatever the transaction goes on to accept or deliver.
func (s *Service) loadGitState(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) (gitState, error) {
	q := db.New(tx)
	row, err := q.GetGitConnection(ctx, db.GetGitConnectionParams{WorkspaceID: ws, ID: id})
	if err != nil {
		return gitState{}, err
	}
	st := gitState{configuration: row.Configuration, encrypted: row.EncryptedAccess}
	if err = json.Unmarshal(row.Configuration, &st.connection); err != nil {
		return st, err
	}
	if !appConfiguration(row.Configuration) {
		return st, nil
	}
	if s.GitHub == nil {
		st.unusable = domain.Invalid("githubApp.reauthorizationRequired")
		return st, nil
	}
	binding, err := q.ShareActiveGitHubAppConnection(ctx, db.ShareActiveGitHubAppConnectionParams{WorkspaceID: ws, ConnectionID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		st.unusable = domain.Invalid("githubApp.reauthorizationRequired")
		return st, nil
	}
	if err != nil {
		return st, err
	}
	st.binding = &binding
	c := st.connection
	if c.Provider != "github" || c.BaseURL != "https://api.github.com" || c.RepositoryID != binding.RepositoryID || binding.AppID != s.GitHub.Config.ID {
		st.unusable = domain.Forbidden
	}
	return st, nil
}

func (s *Service) openStoredAccess(ws, id uuid.UUID, st gitState) (pipeline.Access, error) {
	var access pipeline.Access
	err := s.openIntegration(ws, id, "git", st.encrypted, &access)
	return access, err
}

// openGitAccess opens a stored credential or, for a GitHub App, validates the grant and
// mints a token. Minting contacts GitHub, so this belongs inside a providerCall.
func (s *Service) openGitAccess(ctx context.Context, ws, id uuid.UUID, st gitState, write bool) (pipeline.Access, error) {
	if st.binding == nil {
		return s.openStoredAccess(ws, id, st)
	}
	b := st.binding
	if err := s.GitHub.Validate(ctx, b.InstallationID, b.RepositoryID, b.Generation, st.connection.Repository, st.connection.Private); err != nil {
		return pipeline.Access{}, err
	}
	token, err := s.GitHub.Mint(ctx, b.InstallationID, b.RepositoryID, write)
	if err != nil {
		return pipeline.Access{}, err
	}
	credential := pipeline.Credential{Username: "x-access-token", Password: token}
	return pipeline.Access{API: credential, Clone: credential}, nil
}

func (s *Service) openProvider(ctx context.Context, ws, id uuid.UUID, st gitState, preview bool) (*pipeline.Provider, error) {
	access, err := s.openGitAccess(ctx, ws, id, st, false)
	if err != nil {
		return nil, err
	}
	if preview && st.binding != nil {
		token, err := s.GitHub.MintPreview(ctx, st.binding.InstallationID, st.binding.RepositoryID)
		if err != nil {
			return nil, err
		}
		access.API = pipeline.Credential{Username: "x-access-token", Password: token}
	}
	return pipeline.NewProvider(st.connection, access.API, s.Policy)
}

// sourceBlobsMargin is what a background job keeps to record its answers and admit other work.
const sourceBlobsMargin = 3 * time.Second

// errSourceBlobsNotAsked says a job's time ran out before its turn, which is no fault of the
// source or its provider.
var errSourceBlobsNotAsked = errors.New("no time left to ask about this source's files")

// gitAnswer separates what a provider said from whether the transaction can go on: a
// question's error return means a failed read or an answer still to fetch, and goes back up
// unchanged, while Err is the provider's failure for the caller to judge.
type gitAnswer[T any] struct {
	Value T
	Err   error
	// Setup marks an Err from establishing the credential, rather than from the question.
	Setup      bool
	Connection pipeline.Connection
}

// failure maps a failed question to the caller's own refusal, leaving a credential failure
// to speak for itself.
func (a gitAnswer[T]) failure(question error) error {
	if a.Setup {
		return a.Err
	}
	return question
}

type connectionAnswers struct {
	state  gitState
	values map[string]any
}

// gitAnswers holds what providers said during one operation, across its passes through
// withGit, each answer valid only while its connection's state is unchanged.
type gitAnswers struct {
	service     *Service
	ws          uuid.UUID
	connections map[uuid.UUID]*connectionAnswers
}

// withGit runs a transaction body that can ask Git providers questions through its
// gitAnswers. Body must open its transaction itself, and must not commit before asking.
func (s *Service) withGit(ctx context.Context, ws uuid.UUID, body func(*gitAnswers) error) error {
	git := &gitAnswers{service: s, ws: ws, connections: map[uuid.UUID]*connectionAnswers{}}
	return withProviderCalls(ctx, func() error { return body(git) })
}

// gitScope is scope for an operation that asks Git providers questions. Only the finishing
// pass writes its audit record.
func (s *Service) gitScope(ctx context.Context, p store.Principal, ws uuid.UUID, access workspaceAccess, action string, id uuid.UUID, fn func(pgx.Tx, *gitAnswers) error) error {
	return s.withGit(ctx, ws, func(git *gitAnswers) error {
		return s.scope(ctx, p, ws, access, action, id, func(tx pgx.Tx) error { return fn(tx, git) })
	})
}

// current re-reads the connection under the caller's locks, dropping every answer about it
// if it changed.
func (g *gitAnswers) current(ctx context.Context, tx pgx.Tx, id uuid.UUID) (*connectionAnswers, error) {
	st, err := g.service.loadGitState(ctx, tx, g.ws, id)
	if err != nil {
		return nil, err
	}
	known := g.connections[id]
	if known != nil && known.state.same(st) {
		return known, nil
	}
	known = &connectionAnswers{state: st, values: map[string]any{}}
	g.connections[id] = known
	return known, nil
}

// connection reports the connection as it stands, and whether it uses a GitHub App.
func (g *gitAnswers) connection(ctx context.Context, tx pgx.Tx, id uuid.UUID) (pipeline.Connection, bool, error) {
	known, err := g.current(ctx, tx, id)
	if err != nil {
		return pipeline.Connection{}, false, err
	}
	return known.state.connection, appConfiguration(known.state.configuration), nil
}

func askGit[T any](ctx context.Context, tx pgx.Tx, g *gitAnswers, id uuid.UUID, question string, fetch func(context.Context, gitState) gitAnswer[T]) (gitAnswer[T], error) {
	known, err := g.current(ctx, tx, id)
	if err != nil {
		return gitAnswer[T]{}, err
	}
	var out gitAnswer[T]
	if known.state.unusable != nil {
		out = gitAnswer[T]{Err: known.state.unusable, Setup: true}
	} else if stored, ok := known.values[question]; ok {
		out = stored.(gitAnswer[T])
	} else {
		state := known.state
		// Questions joined by and can repeat, such as sources selecting the same files.
		return out, &providerCall{fetch: func(ctx context.Context) {
			if _, answered := known.values[question]; !answered {
				known.values[question] = fetch(ctx, state)
			}
		}}
	}
	out.Connection = known.state.connection
	return out, nil
}

func askProvider[T any](ctx context.Context, tx pgx.Tx, g *gitAnswers, id uuid.UUID, preview bool, question string, call func(context.Context, *pipeline.Provider) (T, error)) (gitAnswer[T], error) {
	return askGit(ctx, tx, g, id, fmt.Sprintf("provider/%t/%s", preview, question), func(ctx context.Context, st gitState) gitAnswer[T] {
		provider, err := g.service.openProvider(ctx, g.ws, id, st, preview)
		if err != nil {
			return gitAnswer[T]{Err: err, Setup: true}
		}
		value, err := call(ctx, provider)
		return gitAnswer[T]{Value: value, Err: err}
	})
}

// access asks for the credential a command would carry. A stored one needs no call and costs
// no pass; a GitHub App's is minted.
func (g *gitAnswers) access(ctx context.Context, tx pgx.Tx, id uuid.UUID, write bool) (gitAnswer[pipeline.Access], error) {
	known, err := g.current(ctx, tx, id)
	if err != nil {
		return gitAnswer[pipeline.Access]{}, err
	}
	if known.state.binding == nil && known.state.unusable == nil {
		access, err := g.service.openStoredAccess(g.ws, id, known.state)
		return gitAnswer[pipeline.Access]{Value: access, Err: err, Setup: err != nil}, nil
	}
	return askGit(ctx, tx, g, id, fmt.Sprintf("access/%t", write), func(ctx context.Context, st gitState) gitAnswer[pipeline.Access] {
		access, err := g.service.openGitAccess(ctx, g.ws, id, st, write)
		return gitAnswer[pipeline.Access]{Value: access, Err: err, Setup: err != nil}
	})
}

// errCloneRejected refuses a stored clone credential the Git server did not accept, since what
// it could do is then unknown.
var errCloneRejected = &domain.Error{Code: "invalid_input", Message: "The Git server did not accept the connection's clone credential, so whether it can push is unknown. Check its username and token.", Fields: []string{"clone.rejected"}}

// cloneRefusal reports whether err refuses the clone credential itself, which no retry changes.
func cloneRefusal(err error) (*domain.Error, bool) {
	for _, refusal := range []*domain.Error{errCloneCanPush, errCloneRejected} {
		if errors.Is(err, refusal) {
			return refusal, true
		}
	}
	return nil, false
}

// errCloneCanPush refuses a stored clone credential that the Git server would let push.
var errCloneCanPush = &domain.Error{Code: "invalid_input", Message: "The connection's clone credential can push to the repository. Runners build code that may be hostile, so give the connection a read-only clone credential, such as a deploy token with only read_repository or a token with only read access to repository contents.", Fields: []string{"clone.canPush"}}

// cloneAccess asks for the only credential a runner receives. A GitHub App's is minted with read
// access to the one repository's contents. A stored one is first put to the Git server, which
// answers whether it may push without anything being pushed, and is refused if it may.
func (g *gitAnswers) cloneAccess(ctx context.Context, tx pgx.Tx, id uuid.UUID) (gitAnswer[pipeline.Credential], error) {
	access, err := g.access(ctx, tx, id, false)
	if err != nil || access.Err != nil {
		return gitAnswer[pipeline.Credential]{Err: access.Err, Setup: access.Setup, Connection: access.Connection}, err
	}
	known, err := g.current(ctx, tx, id)
	if err != nil {
		return gitAnswer[pipeline.Credential]{}, err
	}
	answer := gitAnswer[pipeline.Credential]{Value: access.Value.Clone, Connection: known.state.connection}
	if known.state.binding != nil {
		return answer, nil
	}
	// A runner fetches anonymously without a password, so nothing else of the credential is sent.
	if access.Value.Clone.Password == "" {
		answer.Value = pipeline.Credential{}
		return answer, nil
	}
	push, err := askGit(ctx, tx, g, id, "clone/push", func(ctx context.Context, st gitState) gitAnswer[bool] {
		stored, err := g.service.openStoredAccess(g.ws, id, st)
		if err != nil {
			return gitAnswer[bool]{Err: err, Setup: true}
		}
		provider, err := pipeline.NewProvider(st.connection, stored.Clone, g.service.Policy)
		if err != nil {
			return gitAnswer[bool]{Err: err, Setup: true}
		}
		can, err := provider.CanPush(ctx)
		return gitAnswer[bool]{Value: can, Err: err}
	})
	if err != nil {
		return gitAnswer[pipeline.Credential]{}, err
	}
	if errors.Is(push.Err, pipeline.ErrCredentialRejected) {
		return gitAnswer[pipeline.Credential]{Err: errCloneRejected, Setup: true, Connection: answer.Connection}, nil
	}
	if push.Err != nil {
		return gitAnswer[pipeline.Credential]{Err: push.Err, Setup: push.Setup, Connection: answer.Connection}, nil
	}
	if push.Value {
		return gitAnswer[pipeline.Credential]{Err: errCloneCanPush, Setup: true, Connection: answer.Connection}, nil
	}
	return answer, nil
}

type repositoryIdentity struct {
	ID      int64
	Private bool
}

func (g *gitAnswers) repository(ctx context.Context, tx pgx.Tx, id uuid.UUID) (gitAnswer[repositoryIdentity], error) {
	return askProvider(ctx, tx, g, id, false, "repository", func(ctx context.Context, p *pipeline.Provider) (repositoryIdentity, error) {
		number, private, err := p.Repository(ctx)
		return repositoryIdentity{number, private}, err
	})
}

// head is asked again after a push delivery for the branch. The workspace lock no longer
// orders an admission's head against the refresh that delivery starts: that refresh could
// finish first, and the admission would then record a commit older than one the controller
// had already been told about, with nothing left to notice.
func (g *gitAnswers) head(ctx context.Context, tx pgx.Tx, id uuid.UUID, branch string) (gitAnswer[string], error) {
	pushes, err := db.New(tx).GitBranchPushes(ctx, db.GitBranchPushesParams{WorkspaceID: g.ws, ConnectionID: id, Branch: branch})
	if err != nil {
		return gitAnswer[string]{}, err
	}
	return askProvider(ctx, tx, g, id, false, fmt.Sprintf("head/%d/%s", pushes, branch), func(ctx context.Context, p *pipeline.Provider) (string, error) {
		return p.Resolve(ctx, branch)
	})
}

// sourceBlobs reads what the provider's tree holds at a commit, which is true for good, so an
// import freezes the answer when it is admitted and its runner's report needs no network. A
// background job keeps some of its time, so that running out becomes an answer it can record
// instead of the job's own failure.
func (g *gitAnswers) sourceBlobs(ctx context.Context, tx pgx.Tx, id uuid.UUID, commit string, paths []string) (gitAnswer[map[string]string], error) {
	return askProvider(ctx, tx, g, id, false, fmt.Sprintf("blobs/%s/%q", commit, paths), func(ctx context.Context, p *pipeline.Provider) (map[string]string, error) {
		if deadline, bounded := ctx.Deadline(); bounded {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, deadline.Add(-sourceBlobsMargin))
			defer cancel()
			if ctx.Err() != nil {
				return nil, errSourceBlobsNotAsked
			}
		}
		return p.SourceBlobs(ctx, commit, paths)
	})
}

// previewHead asks with the credential that can also read pull requests, as preview policies
// have always been checked, so a save finds out if a GitHub App lacks that permission.
func (g *gitAnswers) previewHead(ctx context.Context, tx pgx.Tx, id uuid.UUID, branch string) (gitAnswer[string], error) {
	return askProvider(ctx, tx, g, id, true, "head/"+branch, func(ctx context.Context, p *pipeline.Provider) (string, error) {
		return p.Resolve(ctx, branch)
	})
}

// pullRequest is asked again when recorded changes, for a caller whose answer must not be
// older than what another job has already recorded about the request.
func (g *gitAnswers) pullRequest(ctx context.Context, tx pgx.Tx, id uuid.UUID, number int, recorded string) (gitAnswer[pipeline.PullRequest], error) {
	return askProvider(ctx, tx, g, id, true, fmt.Sprintf("pull/%d/%s", number, recorded), func(ctx context.Context, p *pipeline.Provider) (pipeline.PullRequest, error) {
		return p.PullRequest(ctx, number)
	})
}

// mergedAt asks whether any of these reviews merged as exactly this commit.
func (g *gitAnswers) mergedAt(ctx context.Context, tx pgx.Tx, id uuid.UUID, reviews []int, head string) (gitAnswer[bool], error) {
	return askProvider(ctx, tx, g, id, false, fmt.Sprintf("merged/%s/%v", head, reviews), func(ctx context.Context, p *pipeline.Provider) (bool, error) {
		for _, number := range reviews {
			merged, err := p.MergedCommit(ctx, number)
			if err != nil {
				return false, err
			}
			if merged == head {
				return true, nil
			}
		}
		return false, nil
	})
}

func (g *gitAnswers) openPullRequests(ctx context.Context, tx pgx.Tx, id uuid.UUID, branch string, page int) (gitAnswer[[]int], error) {
	return askProvider(ctx, tx, g, id, true, fmt.Sprintf("open/%d/%s", page, branch), func(ctx context.Context, p *pipeline.Provider) ([]int, error) {
		return p.OpenPullRequests(ctx, branch, page)
	})
}
