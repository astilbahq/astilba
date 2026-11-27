package execution

import (
	"context"
	"errors"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// A preview step refused for good would be refused again at every pass, each five minutes, and each
// pass would repeat the step's work first: creating the environment copies every template secret
// through the broker, which records each access; admitting a build searches the environment's
// runtime secrets; and promoting loads the Compose files. So a refused step is recorded with a key
// over everything it reads that a later pass could find changed, and a pass that finds the same key
// skips the step and shows the reason again. Once the request's commit, the policy, the preview
// host's agent or the preview environment changes, the key differs and the step runs again. A
// refresh clears the key, so an owner can always ask for the step again.
//
// Only refusals are recorded. An error that may pass by itself, such as the operation limit or a
// provider call, still fails the pass and is retried.

// creationRetry and environmentRetry tell the owner what makes a refused step run again. A policy
// can't change its configuration while any of its previews holds an environment, so a step refused
// in an environment the preview keeps names the cleanup a policy change needs first.
const (
	creationRetry    = " It is tried again after a new commit, a change to the preview policy or an update of the preview host's agent, or when an owner refreshes the preview."
	environmentRetry = " It is tried again after a new commit or an update of the preview host's agent, or when an owner refreshes the preview. The preview policy can change once its previews are cleaned up."
)

// previewRefusalKey digests a step's name and inputs together with what the step reads of the
// preview host. Every key names the policy generation the step ran under, so a policy saved since
// never matches one.
func previewRefusalKey(ctx context.Context, tx pgx.Tx, p previewPolicy, step string, inputs any) (string, error) {
	host, err := db.New(tx).PreviewRefusalHost(ctx, db.PreviewRefusalHostParams{WorkspaceID: p.Workspace, ID: p.Server})
	if err != nil {
		return "", err
	}
	return Hash(struct {
		Step             string
		Policy           uuid.UUID
		PolicyGeneration int64
		Host             db.PreviewRefusalHostRow
		Inputs           any
	}{step, p.ID, p.Generation, host, inputs}), nil
}

// previewCreationKey covers creating a preview's environment and admitting its first build in the
// same pass: a build refused there rolls the environment back, so the next pass would create it
// again. The template revision, its secrets' versions and the build configuration are the policy
// generation's; the generated hostnames depend on the application domain.
func (s *Service) previewCreationKey(ctx context.Context, tx pgx.Tx, p previewPolicy, pr pipeline.PullRequest, connection pipeline.Connection) (string, error) {
	return previewRefusalKey(ctx, tx, p, "create", struct {
		Head              string
		HeadRepository    int64
		Build             string
		ApplicationDomain string
	}{pr.Head, pr.HeadRepository, buildInputHash(connection, pr.Head, p.Config.Build), s.Store.ApplicationDomain})
}

// previewBuildKey covers admitting a build into a preview's existing environment. The build input
// hash covers the commit, the build configuration and the builder management pins, and the
// environment's secrets are what its arguments are checked against.
func previewBuildKey(ctx context.Context, tx pgx.Tx, p previewPolicy, r *previewRecord, pr pipeline.PullRequest, connection pipeline.Connection) (string, error) {
	env, err := db.New(tx).PreviewRefusalEnvironment(ctx, db.PreviewRefusalEnvironmentParams{WorkspaceID: p.Workspace, ID: *r.View.EnvironmentId})
	if err != nil {
		return "", err
	}
	return previewRefusalKey(ctx, tx, p, "build", struct {
		Environment uuid.UUID
		Generation  int64
		Build       string
		State       db.PreviewRefusalEnvironmentRow
	}{*r.View.EnvironmentId, r.View.Generation, buildInputHash(connection, pr.Head, r.Config.Build), env})
}

// previewPromotionKey covers promoting a preview's build into its draft. The build names the
// artifact, and the environment's state names the draft it is written over.
func (s *Service) previewPromotionKey(ctx context.Context, tx pgx.Tx, p previewPolicy, r *previewRecord) (string, error) {
	env, err := db.New(tx).PreviewRefusalEnvironment(ctx, db.PreviewRefusalEnvironmentParams{WorkspaceID: p.Workspace, ID: *r.View.EnvironmentId})
	if err != nil {
		return "", err
	}
	return previewRefusalKey(ctx, tx, p, "promote", struct {
		Environment       uuid.UUID
		Generation        int64
		Build, App        uuid.UUID
		State             db.PreviewRefusalEnvironmentRow
		ApplicationDomain string
	}{*r.View.EnvironmentId, r.View.Generation, *r.Build, *r.App, env, s.Store.ApplicationDomain})
}

// refusePreview fails the request with note and records key, so later passes skip the step until
// its inputs change. kept says whether the preview still holds its environment. The note is kept to be shown at every later pass, so it is bounded as an
// agent's is. Every reason is a fixed sentence or names a field the API bounds, such as a host's
// name or a Railpack digest, and never quotes the configuration (refusedChange).
func refusePreview(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID, key, note string, kept bool) error {
	retry := creationRetry
	if kept {
		retry = environmentRetry
	}
	return db.New(tx).RefusePreviewStep(ctx, db.RefusePreviewStepParams{WorkspaceID: ws, ID: id, RefusedKey: key, Note: boundedNote(note) + retry})
}

// stillRefused is a pass that skips a step refused under the same key: the request stays failed
// with the reason it was refused for, which another pass, such as one blocked on the policy's
// authority, may have replaced meanwhile.
func stillRefused(ctx context.Context, tx pgx.Tx, ws uuid.UUID, r *previewRecord) error {
	return db.New(tx).RefusePreviewStep(ctx, db.RefusePreviewStepParams{WorkspaceID: ws, ID: r.View.Id, RefusedKey: r.RefusedKey, Note: r.RefusalNote})
}

// previewBuildRefusal is the reason a build was refused for good before it reached the runner, or
// "" for an error that may pass by itself. A build-input rejection is the policy's. A runner that
// must be updated is refused until its agent advertises what the build needs, which the host's part
// of the key covers.
func previewBuildRefusal(err error) string {
	var rejected *buildInputRejection
	if errors.As(err, &rejected) {
		return rejected.note()
	}
	var refused *domain.Error
	if !runnerUpgradeRequired(err) || !errors.As(err, &refused) {
		return ""
	}
	// A runner's Railpack pins are refused before admission, by runnerRailpackRefusal.
	switch {
	case slices.Contains(refused.Fields, "runner.buildNetworkUpgradeRequired"):
		return "Update the agent on the preview host: it would run build code on the host's network."
	case slices.Contains(refused.Fields, "runner.rootlessBuildsUpgradeRequired"):
		return "Update the agent on the preview host: it would run build code as root."
	}
	return "Update the agent on the preview host: it cannot run this build."
}
