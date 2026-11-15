package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// serverRanges is what a server's agent last reported about the ranges its containers send
// traffic from. Known is false when it reports none: an agent from before the report, the
// enrollment command, which can't know the run process's network pool, or an agent that could
// not read them all.
type serverRanges struct {
	ranges    []domain.ContainerRange
	supported bool
	known     bool
}

// containerRangesOf reads them. A report management can't parse counts as unknown.
func containerRangesOf(ctx context.Context, q *db.Queries, ws, server uuid.UUID) (serverRanges, error) {
	row, err := q.GetServerContainerRanges(ctx, db.GetServerContainerRangesParams{WorkspaceID: ws, ID: server})
	if errors.Is(err, pgx.ErrNoRows) {
		return serverRanges{}, nil
	}
	if err != nil {
		return serverRanges{}, err
	}
	out := serverRanges{supported: row.Supported}
	if !row.Supported || len(row.Ranges) == 0 || string(row.Ranges) == "null" {
		return out, nil
	}
	var reported []api.ContainerRange
	if json.Unmarshal(row.Ranges, &reported) != nil {
		return out, nil
	}
	for _, r := range reported {
		c, ok := domain.ParseContainerRange(r.Prefix, string(r.Source))
		if !ok {
			return serverRanges{supported: true}, nil
		}
		out.ranges = append(out.ranges, c)
	}
	// A host has at least its network pool, so an empty list is no report either.
	out.known = len(out.ranges) > 0
	return out, nil
}

// stagedAllowlists checks each route a catalog staging created in the environment, while it
// keeps the allowlist it was staged with, against the ranges the plan's server's containers send
// traffic from, and refuses the plan for an entry that overlaps one, as staging does. When the
// server's ranges are unknown it returns the effect an owner must review instead, naming the
// ranges that could not be checked, so the allowlist never passes unchecked.
func stagedAllowlists(ctx context.Context, q *db.Queries, ws, env, server uuid.UUID, spec domain.Specification) (string, error) {
	staged, err := q.ListCatalogStagedRoutes(ctx, db.ListCatalogStagedRoutesParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil || len(staged) == 0 {
		return "", err
	}
	routes := []domain.Route{}
	for _, r := range spec.Routes {
		if slices.ContainsFunc(staged, func(s db.ListCatalogStagedRoutesRow) bool {
			return s.RouteID == r.ID && slices.Equal(s.RouteAllowCidrs, r.AllowCIDRs)
		}) {
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		return "", nil
	}
	host, err := containerRangesOf(ctx, q, ws, server)
	if err != nil {
		return "", err
	}
	for _, r := range routes {
		if err := domain.ValidateOwnerAllowlist(r.AllowCIDRs, host.ranges); err != nil {
			if invalid := (*domain.Error)(nil); errors.As(err, &invalid) {
				return "", &domain.Error{Code: invalid.Code, Message: "Route " + r.Domain + ": " + invalid.Message, Fields: []string{"routes.allowCidrs"}}
			}
			return "", err
		}
	}
	switch {
	case host.known:
		return "", nil
	case host.supported:
		return deployment.UnreadContainerRangesEffect, nil
	}
	return deployment.UnreportedContainerRangesEffect, nil
}
