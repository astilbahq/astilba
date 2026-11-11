package execution

import (
	"context"
	"encoding/json/v2"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ServerBuildNetwork returns the private destinations builds on a runner may reach.
func (s *Service) ServerBuildNetwork(ctx context.Context, p store.Principal, ws, id uuid.UUID) (api.BuildNetwork, error) {
	var out api.BuildNetwork
	e := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		rules, err := serverBuildEgress(ctx, tx, ws, id)
		out = buildNetworkView(rules)
		return err
	})
	return out, e
}

// ConfigureServerBuildNetwork replaces a runner's build network allowlist. It widens what
// build code can reach on the owner's network, so it needs the owner, recently signed in.
func (s *Service) ConfigureServerBuildNetwork(ctx context.Context, p store.Principal, ws, id uuid.UUID, in api.BuildNetwork) (api.BuildNetwork, error) {
	var out api.BuildNetwork
	rules := []pipeline.EgressRule{}
	for _, d := range in.Allow {
		rule, err := pipeline.ParseEgressRule(d.Prefix, d.Port)
		if err != nil {
			return out, &domain.Error{Code: "invalid_input", Message: err.Error(), Fields: []string{"allow"}}
		}
		rules = append(rules, rule)
	}
	rules = pipeline.CanonicalEgress(rules)
	raw, err := stablejson.Marshal(rules)
	if err != nil {
		return out, err
	}
	e := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: true, Recent: true}, "server.build_network.configure", id, func(tx pgx.Tx) error {
		q := db.New(tx)
		n, err := q.SetServerBuildEgress(ctx, db.SetServerBuildEgressParams{WorkspaceID: ws, ID: id, BuildEgress: raw})
		if err != nil {
			return err
		}
		if n == 1 {
			return nil
		}
		row, err := q.ServerBuildEgress(ctx, db.ServerBuildEgressParams{WorkspaceID: ws, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NotFound
		}
		if err == nil && row.Role == "deployment" {
			return domain.Invalid("role")
		}
		if err == nil {
			// A revoked runner builds nothing again.
			err = domain.Conflict
		}
		return err
	})
	return buildNetworkView(rules), e
}

// serverBuildEgress reads a runner's stored allowlist, which only this package writes.
func serverBuildEgress(ctx context.Context, tx pgx.Tx, ws, id uuid.UUID) ([]pipeline.EgressRule, error) {
	row, err := db.New(tx).ServerBuildEgress(ctx, db.ServerBuildEgressParams{WorkspaceID: ws, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, domain.NotFound
	}
	if err != nil {
		return nil, err
	}
	var rules []pipeline.EgressRule
	if json.Unmarshal(row.BuildEgress, &rules, json.RejectUnknownMembers(true)) != nil || pipeline.ValidateEgress(rules) != nil {
		return nil, errors.New("stored build network allowlist is invalid")
	}
	if len(rules) == 0 {
		return nil, nil
	}
	return rules, nil
}

// settleBuildNetwork stops a build its runner must not run as it was queued: one queued
// before builds carried the build network, which an agent that predates it would run on its
// host's network, or one with a destination the owner removed since. It reports whether it
// settled the command, committing tx when it did. An undelivered build fails here; one the
// runner may hold is stopped and sent as cancelled. Returning an error instead would fail
// every poll and hold the runner's queue.
func (s *Service) settleBuildNetwork(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c *api.ExecutionCommand, p pipeline.Command, undelivered bool) (bool, error) {
	if !p.IsBuild() {
		return false, nil
	}
	q := db.New(tx)
	note := ""
	if p.BuildNetwork != pipeline.BuildNetworkV1 {
		note = "Build stopped: it was queued before builds ran on an isolated network. Start the build again."
	} else if len(p.BuildEgress) > 0 {
		current, err := serverBuildEgress(ctx, tx, ws, c.ServerId)
		if err != nil {
			return false, err
		}
		if !pipeline.EgressCovers(current, p.BuildEgress) {
			note = "Build stopped: the runner's build network allowlist no longer includes a private destination this build was queued with. Start the build again."
		}
	}
	if note == "" {
		return false, nil
	}
	var err error
	if undelivered {
		if err = q.FinishUndeliveredPipeline(ctx, db.FinishUndeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, State: "failed", Note: note}); err != nil {
			return false, err
		}
		if err = s.enqueuePreviewForEnvironment(ctx, tx, ws, c.EnvironmentId); err != nil {
			return false, err
		}
	} else {
		if err = q.StopPossiblyDeliveredPipeline(ctx, db.StopPossiblyDeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, Note: note}); err != nil {
			return false, err
		}
		c.CancelRequested = true
	}
	return true, tx.Commit(ctx)
}

func buildNetworkView(rules []pipeline.EgressRule) api.BuildNetwork {
	out := api.BuildNetwork{Allow: []api.BuildDestination{}}
	for _, r := range rules {
		out.Allow = append(out.Allow, api.BuildDestination{Prefix: r.Prefix, Port: r.Port})
	}
	return out
}
