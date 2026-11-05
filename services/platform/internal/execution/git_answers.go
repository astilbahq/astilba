package execution

import (
	"bytes"
	"context"
	json "encoding/json/v2"
	"errors"
	"fmt"

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
		return out, &providerCall{fetch: func(ctx context.Context) { known.values[question] = fetch(ctx, state) }}
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

// previewHead asks with the credential that can also read pull requests, as preview policies
// have always been checked, so a save finds out if a GitHub App lacks that permission.
func (g *gitAnswers) previewHead(ctx context.Context, tx pgx.Tx, id uuid.UUID, branch string) (gitAnswer[string], error) {
	return askProvider(ctx, tx, g, id, true, "head/"+branch, func(ctx context.Context, p *pipeline.Provider) (string, error) {
		return p.Resolve(ctx, branch)
	})
}

func (g *gitAnswers) pullRequest(ctx context.Context, tx pgx.Tx, id uuid.UUID, number int) (gitAnswer[pipeline.PullRequest], error) {
	return askProvider(ctx, tx, g, id, true, fmt.Sprintf("pull/%d", number), func(ctx context.Context, p *pipeline.Provider) (pipeline.PullRequest, error) {
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
