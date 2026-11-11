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
	if len(in.Allow) > pipeline.MaxEgressRules {
		return out, domain.Invalid("allow")
	}
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

// narrowedBuildNote explains a build stopped because its runner's allowlist shrank.
const narrowedBuildNote = "Build stopped: the runner's build network allowlist no longer includes a private destination this build was queued with. Start the build again."

// settleNarrowedEgress stops a build that would reach a destination the owner removed after
// it was queued. It reports whether it settled the command, committing tx when it did. An
// undelivered build fails here; one a runner may hold is stopped and sent as cancelled.
func settleNarrowedEgress(ctx context.Context, tx pgx.Tx, ws uuid.UUID, c *api.ExecutionCommand, p pipeline.Command, undelivered bool) (bool, error) {
	if len(p.BuildEgress) == 0 {
		return false, nil
	}
	current, err := serverBuildEgress(ctx, tx, ws, c.ServerId)
	if err != nil || pipeline.EgressCovers(current, p.BuildEgress) {
		return false, err
	}
	q := db.New(tx)
	if undelivered {
		err = q.FinishUndeliveredPipeline(ctx, db.FinishUndeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, State: "failed", Note: narrowedBuildNote})
	} else {
		err = q.StopPossiblyDeliveredPipeline(ctx, db.StopPossiblyDeliveredPipelineParams{WorkspaceID: ws, ID: c.Id, Note: narrowedBuildNote})
		c.CancelRequested = true
	}
	if err != nil {
		return false, err
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
