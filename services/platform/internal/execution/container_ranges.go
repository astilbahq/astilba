package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/catalog"
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

// stagedRoutes works out the routes catalog stagings created in the environment, each with the
// allowlist it was staged with: the routes in the specification a staging saved that target a
// service it created. Those services were new then, so nothing else could route to them in that
// revision. A clone records its source's stagings against its own first revision, so routes to
// a staged service the source added later count there too. Unresolved is whether a staging
// could not be read, in which case which routes it created is unknown.
func stagedRoutes(ctx context.Context, q *db.Queries, ws, env uuid.UUID) (staged map[uuid.UUID][]string, unresolved bool, err error) {
	routeless := []string{}
	for _, r := range catalog.Recipes() {
		if r.Route == nil {
			routeless = append(routeless, r.ID)
		}
	}
	rows, err := q.ListCatalogStagedRevisions(ctx, db.ListCatalogStagedRevisionsParams{WorkspaceID: ws, EnvironmentID: env, Routeless: routeless})
	if err != nil {
		return nil, false, err
	}
	staged = map[uuid.UUID][]string{}
	for _, row := range rows {
		var services map[uuid.UUID]uuid.UUID
		spec, _, parseErr := domain.ParseSpec(row.Specification)
		if row.Specification == nil || parseErr != nil || json.Unmarshal(row.Services, &services) != nil {
			unresolved = true
			continue
		}
		created := map[uuid.UUID]bool{}
		for _, id := range services {
			created[id] = true
		}
		for _, r := range spec.Routes {
			if created[r.TargetID] {
				staged[r.ID] = r.AllowCIDRs
			}
		}
	}
	return staged, unresolved, nil
}

// stagedAllowlists checks each route a catalog staging created in the environment, while it
// keeps the allowlist it was staged with, against the ranges the plan's server's containers send
// traffic from, and refuses the plan for an entry that overlaps one, as staging does. It returns
// what an owner must review instead when it can't check: the server's ranges are unknown, or a
// staging could not be read. So a staged allowlist never passes unchecked.
func stagedAllowlists(ctx context.Context, q *db.Queries, ws, env, server uuid.UUID, spec domain.Specification) ([]string, error) {
	staged, unresolved, err := stagedRoutes(ctx, q, ws, env)
	if err != nil {
		return nil, err
	}
	review := []string{}
	// Only a route can admit anyone, so a plan without one needs no review for it.
	if unresolved && len(spec.Routes) > 0 {
		review = append(review, deployment.UnresolvedStagingEffect)
	}
	routes := []domain.Route{}
	for _, r := range spec.Routes {
		if allow, ok := staged[r.ID]; ok && slices.Equal(allow, r.AllowCIDRs) {
			routes = append(routes, r)
		}
	}
	if len(routes) == 0 {
		return review, nil
	}
	host, err := containerRangesOf(ctx, q, ws, server)
	if err != nil {
		return nil, err
	}
	for _, r := range routes {
		if err := domain.ValidateOwnerAllowlist(r.AllowCIDRs, host.ranges); err != nil {
			if invalid := (*domain.Error)(nil); errors.As(err, &invalid) {
				return nil, &domain.Error{Code: invalid.Code, Message: invalid.Message, Fields: []string{"routes.allowCidrs"}}
			}
			return nil, err
		}
	}
	switch {
	case host.known:
	case host.supported:
		review = append(review, deployment.UnreadContainerRangesEffect)
	default:
		review = append(review, deployment.UnreportedContainerRangesEffect)
	}
	return review, nil
}
