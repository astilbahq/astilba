package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Why a command waited for management's own configuration. Each is added to the operation's note
// once, and kept if the command then expires undelivered.
const (
	noGitHubAppNote    = "Delivery waited because management is running without the GitHub App this connection uses. An operator can configure it again."
	otherGitHubAppNote = "Delivery waited because management is running with another GitHub App than the one this connection was authorized with. An operator can configure that App again, or the repository can be connected again with the current one."
	providerPolicyNote = "Delivery waited because management's configuration does not allow this connection's Git provider, as once an operator removes its origin. An operator can allow it again."
)

// waitNotes say why a pipeline's delivery waited, in the order waitedNotes gives them.
var waitNotes = []string{gitHubBusyNote, noGitHubAppNote, otherGitHubAppNote, providerPolicyNote}

// waitedNotes is what of an operation's note says why its delivery waited, for the note it ends
// with if it expires undelivered. The rest, such as the run's reason, which the note starts with
// and its end puts in front again, is left out.
func waitedNotes(note string) string {
	var waited []string
	for _, n := range waitNotes {
		if strings.Contains(note, n) {
			waited = append(waited, n)
		}
	}
	return strings.Join(waited, " ")
}

// operatorWait is a command that management's own configuration refuses for now, as once an
// operator removes a provider's origin or management starts without its GitHub App. Only the
// operator's change undoes it, which may come at any time, so the command waits rather than
// settling, and failing the poll instead would hold back every other command of its host until
// then. The poll holds it (holdForOperator) and looks past it.
type operatorWait struct{ note string }

func (e *operatorWait) Error() string { return e.note }

// operatorRefusal says why management's configuration refuses a connection in state st, or ""
// if it doesn't. It needs no network call, so the dispatcher can ask it too.
func (s *Service) operatorRefusal(st gitState) string {
	var unusable *appUnusable
	if errors.As(st.unusable, &unusable) {
		return unusable.operator
	}
	if errors.Is(s.Policy.Check(st.connection), pipeline.ErrNotAllowed) {
		return providerPolicyNote
	}
	return ""
}

// refusedByOperator is operatorRefusal for a connection as this pass reads it, as an operatorWait to
// return, or nil.
func (g *gitAnswers) refusedByOperator(ctx context.Context, tx pgx.Tx, id uuid.UUID) error {
	known, err := g.current(ctx, tx, id)
	if err != nil {
		return err
	}
	if why := g.service.operatorRefusal(known.state); why != "" {
		return &operatorWait{note: why}
	}
	return nil
}

// queuedOperatorRefusal is operatorRefusal for a queued pipeline's connection, which the
// dispatcher asks so as not to give a pipeline its runner's turn while delivery would only hand
// it back.
func (s *Service) queuedOperatorRefusal(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c api.ExecutionCommand) (string, error) {
	var p pipeline.Command
	// Delivery refuses an invalid payload for itself.
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil {
		return "", nil
	}
	st, err := s.loadGitState(ctx, tx, ws, p.Connection.ID)
	if err != nil {
		return "", err
	}
	return s.operatorRefusal(st), nil
}

// holdForOperator records why a command waits for management's configuration. A pipeline that no
// runner holds goes back to the queue, so it no longer takes its runner's turn: the runner's
// other work, its update and later pipelines included, can be dispatched and delivered, and the
// dispatcher keeps it queued until the configuration allows it or its deadline passes. A command
// its host may hold keeps its place, and only goes without its values meanwhile.
func (s *Service) holdForOperator(ctx context.Context, a Identity, c api.ExecutionCommand, wait *operatorWait) error {
	tx, err := s.Store.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	queries := db.New(tx)
	state, err := queries.LockServerOperationState(ctx, db.LockServerOperationStateParams{WorkspaceID: a.Workspace, ID: c.Id, ServerID: a.ID})
	if err != nil {
		return err
	}
	if Terminal(api.ExecutionState(state)) {
		return nil
	}
	if c.Kind == "pipeline" {
		if err = queries.RequeueUndeliveredPipeline(ctx, db.RequeueUndeliveredPipelineParams{WorkspaceID: a.Workspace, ID: c.Id}); err != nil {
			return err
		}
	}
	if err = queries.AddOperationNote(ctx, db.AddOperationNoteParams{WorkspaceID: a.Workspace, ID: c.Id, Note: wait.note}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// heldForOperator holds c for management's configuration when err is an operatorWait, and
// reports whether it did, for the poll to look past c to its host's other commands. Any other err
// comes back as it is.
func (s *Service) heldForOperator(ctx context.Context, a Identity, c api.ExecutionCommand, err error) (bool, error) {
	var wait *operatorWait
	if !errors.As(err, &wait) {
		return false, err
	}
	return true, s.holdForOperator(ctx, a, c, wait)
}
