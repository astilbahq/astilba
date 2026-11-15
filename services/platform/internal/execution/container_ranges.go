package execution

import (
	"context"
	"encoding/json/v2"
	"errors"
	"maps"
	"net/netip"
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

// unconfirmedStaging is what a clone records as the staged routes of a staging of its source
// whose routes could not be read, so planning in the clone still asks for review.
const unconfirmedStaging = `"unconfirmed"`

// maxStagedRoutes bounds the routes one staging may record. An entry creates at most one.
const maxStagedRoutes = 32

// stagingRoutes is what one staging says about the routes it created.
type stagingRoutes struct {
	// staged maps each route the staging created to the allowlist it was staged with.
	staged map[uuid.UUID][]string
	// unresolved is a staging that could not be read; unconfirmed, a clone's copy of one.
	unresolved, unconfirmed bool
}

// readStagingRoutes reads the routes a staging recorded, strictly: any value but route IDs to
// allowlists of masked prefixes, within the bounds a route has, makes it unresolved. A staging from
// before the record is worked out from the specification it saved instead: the routes there that
// target a service it created, which were new then, so nothing else could route to them.
func readStagingRoutes(recorded, services, specification []byte) stagingRoutes {
	out := stagingRoutes{staged: map[uuid.UUID][]string{}}
	if recorded != nil {
		if string(recorded) == unconfirmedStaging {
			out.unconfirmed = true
			return out
		}
		var routes map[string][]string
		if json.Unmarshal(recorded, &routes) != nil || len(routes) > maxStagedRoutes {
			out.unresolved = true
			return out
		}
		for key, allow := range routes {
			id, err := uuid.Parse(key)
			if err != nil || id.String() != key || len(allow) == 0 || len(allow) > 32 {
				return stagingRoutes{unresolved: true}
			}
			for _, raw := range allow {
				if p, err := netip.ParsePrefix(raw); err != nil || p != p.Masked() || p.Addr().Is4In6() {
					return stagingRoutes{unresolved: true}
				}
			}
			out.staged[id] = allow
		}
		return out
	}
	var created map[uuid.UUID]uuid.UUID
	spec, _, err := domain.ParseSpec(specification)
	if specification == nil || err != nil || json.Unmarshal(services, &created) != nil {
		return stagingRoutes{unresolved: true}
	}
	targets := map[uuid.UUID]bool{}
	for _, id := range created {
		targets[id] = true
	}
	for _, r := range spec.Routes {
		if targets[r.TargetID] {
			out.staged[r.ID] = r.AllowCIDRs
		}
	}
	return out
}

// stagedRoutes gathers what every staging in the environment says about the routes it created.
func stagedRoutes(ctx context.Context, q *db.Queries, ws, env uuid.UUID) (stagingRoutes, error) {
	rows, err := q.ListCatalogStagingProvenance(ctx, db.ListCatalogStagingProvenanceParams{WorkspaceID: ws, EnvironmentID: env})
	if err != nil {
		return stagingRoutes{}, err
	}
	out := stagingRoutes{staged: map[uuid.UUID][]string{}}
	for _, row := range rows {
		one := readStagingRoutes(row.StagedRoutes, row.Services, row.Specification)
		out.unresolved = out.unresolved || one.unresolved
		out.unconfirmed = out.unconfirmed || one.unconfirmed
		maps.Copy(out.staged, one.staged)
	}
	return out, nil
}

// stagedAllowlists checks each route a catalog staging created in the environment, while it
// keeps the allowlist it was staged with, against the ranges the plan's server's containers send
// traffic from, and refuses the plan for an entry that overlaps one, as staging does. It returns
// what an owner must review instead when it can't check: the server's ranges are unknown, or a
// staging could not be read. So a staged allowlist never passes unchecked.
func stagedAllowlists(ctx context.Context, q *db.Queries, ws, env, server uuid.UUID, spec domain.Specification) ([]string, error) {
	stagings, err := stagedRoutes(ctx, q, ws, env)
	if err != nil {
		return nil, err
	}
	review := []string{}
	// Only a route can admit anyone, so a plan without one needs no review for it.
	if stagings.unresolved && len(spec.Routes) > 0 {
		review = append(review, deployment.UnresolvedStagingEffect)
	}
	if stagings.unconfirmed && len(spec.Routes) > 0 {
		review = append(review, deployment.UnconfirmedStagingEffect)
	}
	routes := []domain.Route{}
	for _, r := range spec.Routes {
		if allow, ok := stagings.staged[r.ID]; ok && slices.Equal(allow, r.AllowCIDRs) {
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
