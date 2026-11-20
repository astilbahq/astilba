package execution

import (
	"context"
	"encoding/json/v2"
	"slices"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// requireAgentSupport refuses a specification the server's agent cannot run: named service
// outputs it has not negotiated, or a PostgreSQL release it does not know.
func requireAgentSupport(ctx context.Context, q db.DBTX, ws, server uuid.UUID, spec domain.Specification) error {
	if err := requirePostgresReleases(ctx, q, ws, server, spec); err != nil {
		return err
	}
	if !domain.UsesServiceOutputs(spec) {
		return nil
	}
	var supported bool
	if err := q.QueryRow(ctx, "SELECT COALESCE((capabilities->>'serviceOutputs')::boolean,false) FROM servers WHERE workspace_id=$1 AND id=$2", ws, server).Scan(&supported); err != nil {
		return err
	}
	if !supported {
		return domain.Invalid("server.capabilities.serviceOutputs")
	}
	return nil
}

// postgresReleasesField marks a refusal an agent update clears.
const postgresReleasesField = "server.capabilities.postgresReleases"

// requirePostgresReleases refuses a managed database on a PostgreSQL release the server's agent
// does not know. An agent from before the release list knows only the first two pins, 17.10 and
// 18.4, and would refuse anything else as an invalid plan.
func requirePostgresReleases(ctx context.Context, q db.DBTX, ws, server uuid.UUID, spec domain.Specification) error {
	var known []string
	for _, a := range spec.Applications {
		if a.Postgres == nil || a.Source.Image == nil {
			continue
		}
		release, ok := domain.PostgresReleaseOf(*a.Source.Image)
		if !ok || release.Version == 170010 || release.Version == 180004 {
			continue
		}
		if known == nil {
			raw, err := db.New(q).ServerCapabilities(ctx, db.ServerCapabilitiesParams{WorkspaceID: ws, ID: server})
			if err != nil {
				return err
			}
			var capabilities api.AgentCapabilities
			if json.Unmarshal(raw, &capabilities) != nil || capabilities.PostgresReleases == nil {
				known = []string{}
			} else {
				known = *capabilities.PostgresReleases
			}
		}
		if !slices.Contains(known, release.Image) {
			name, err := db.New(q).ServerName(ctx, db.ServerNameParams{WorkspaceID: ws, ID: server})
			if err != nil {
				return err
			}
			return &domain.Error{Code: "invalid_input", Message: "Update the agent on " + name + " before running PostgreSQL " + release.Name() + " there: its agent does not know that release.", Fields: []string{postgresReleasesField}}
		}
	}
	return nil
}

// requireMemberSupport checks a cross-host member's agent, which validates the whole
// specification, not only what it runs. The primary's own check comes earlier.
func requireMemberSupport(ctx context.Context, q db.DBTX, ws, server, primary uuid.UUID, spec domain.Specification) error {
	if server == primary {
		return nil
	}
	return requireAgentSupport(ctx, q, ws, server, spec)
}
