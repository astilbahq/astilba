package execution

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// revokedPrimary is the server the environment needs replacing, if any: the applied plan's primary,
// or the unit's server when nothing applied or an attempt to replace it didn't finish. A server an
// unfinished attempt chose holds what it started, so while it's healthy only it may continue.
func revokedPrimary(ctx context.Context, tx pgx.Tx, ws uuid.UUID, v visualEnvironment, previous deployment.Plan) (primary uuid.UUID, continueOn *uuid.UUID, err error) {
	candidates := []uuid.UUID{}
	if previous.ID != uuid.Nil {
		candidates = append(candidates, previous.ServerID)
	}
	if v.Server != nil {
		candidates = append(candidates, *v.Server)
	}
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: candidates})
	if err != nil {
		return uuid.Nil, nil, err
	}
	revoked := map[uuid.UUID]bool{}
	for _, row := range rows {
		revoked[row.ID] = true
	}
	for _, candidate := range candidates {
		if revoked[candidate] {
			primary = candidate
			break
		}
	}
	if primary != uuid.Nil && v.Server != nil && !revoked[*v.Server] {
		continueOn = v.Server
	}
	return primary, continueOn, nil
}

// PrimaryReplacement tells the dashboard, before it asks for a plan, whether the environment needs
// its revoked primary replaced and what that leaves behind.
func (s *Service) PrimaryReplacement(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID) (api.PrimaryReplacementView, error) {
	out := api.PrimaryReplacementView{AbandonedVolumeIds: []uuid.UUID{}}
	err := s.scope(ctx, p, ws, workspaceAccess{}, "", uuid.Nil, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		previous, err := appliedVisualPlan(ctx, tx, ws, env)
		if err != nil {
			return err
		}
		primary, continueOn, err := revokedPrimary(ctx, tx, ws, v, previous)
		if err != nil || primary == uuid.Nil {
			return err
		}
		out.Required, out.RevokedServerId, out.ContinueOnServerId = true, &primary, continueOn
		out.AbandonedVolumeIds, err = db.New(tx).EnvironmentVolumes(ctx, db.EnvironmentVolumesParams{WorkspaceID: ws, EnvironmentID: env, UnitID: v.Unit})
		return err
	})
	return out, err
}

// primaryReplacement decides whether a plan moves the environment off a revoked primary. The owner
// names every volume on it, so an acknowledgement made before the volumes changed doesn't fit.
func primaryReplacement(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, v visualEnvironment, previous deployment.Plan, in api.VisualPlanRequest) (*deployment.Replacement, error) {
	primary, continueOn, err := revokedPrimary(ctx, tx, ws, v, previous)
	if err != nil {
		return nil, err
	}
	if primary == uuid.Nil {
		if in.Replacement != nil {
			return nil, domain.Invalid("replacement")
		}
		return nil, nil
	}
	if in.Replacement == nil {
		return nil, domain.Invalid("replacement")
	}
	if continueOn != nil && *continueOn != in.ServerId {
		return nil, &domain.Error{Code: "invalid_input", Message: "An earlier replacement started on another server. Continue it there.", Fields: []string{"serverId"}}
	}
	volumes, err := db.New(tx).EnvironmentVolumes(ctx, db.EnvironmentVolumesParams{WorkspaceID: ws, EnvironmentID: env, UnitID: v.Unit})
	if err != nil {
		return nil, err
	}
	accepted := slices.Clone(in.Replacement.AbandonedVolumeIds)
	slices.SortFunc(accepted, compareUUID)
	if !slices.Equal(accepted, volumes) {
		return nil, domain.Invalid("replacement.abandonedVolumeIds")
	}
	return &deployment.Replacement{Server: primary, AbandonedVolumes: volumes}, nil
}

func compareUUID(a, b uuid.UUID) int { return strings.Compare(a.String(), b.String()) }

// replacementEffects says what the owner is approving, in the plan's review.
func replacementEffects(ctx context.Context, tx pgx.Tx, ws uuid.UUID, r deployment.Replacement, to uuid.UUID, spec domain.Specification) ([]string, error) {
	names := map[uuid.UUID]string{}
	rows, err := db.New(tx).RevokedServersAmong(ctx, db.RevokedServersAmongParams{WorkspaceID: ws, Ids: []uuid.UUID{r.Server}})
	if err != nil {
		return nil, err
	}
	for _, row := range rows {
		names[row.ID] = row.Name
	}
	target, err := db.New(tx).ServerName(ctx, db.ServerNameParams{WorkspaceID: ws, ID: to})
	if err != nil {
		return nil, err
	}
	out := []string{fmt.Sprintf("Replace revoked server %s as this environment's primary with %s. %s can't be told to stop, so it may keep serving its copy at its own address, and it keeps the secrets, registry access and TLS keys it held. Rotate them, and point custom domains at %s.", names[r.Server], target, names[r.Server], target)}
	if len(r.AbandonedVolumes) > 0 {
		volumeNames := map[uuid.UUID]string{}
		for _, volume := range spec.Volumes {
			volumeNames[volume.ID] = volume.Name
		}
		listed := []string{}
		for _, id := range r.AbandonedVolumes {
			name := volumeNames[id]
			if name == "" {
				name = "retained volume " + id.String()[:8]
			}
			listed = append(listed, name)
		}
		out = append(out, fmt.Sprintf("Start without the data in %s. It stays on %s and isn't copied: the volumes this revision declares start empty on %s, and the others are dropped. Restore what you need from a snapshot by hand.", strings.Join(listed, ", "), names[r.Server], target))
	}
	out = append(out, fmt.Sprintf("Backup policies for this environment stay with %s and can't run. Create new ones for %s.", names[r.Server], target))
	return out, nil
}

// leaveBehind records in a cross-host plan the revoked servers it leaves out. A replaced primary is
// described by the replacement's own effects rather than as an abandoned secondary.
func leaveBehind(ctx context.Context, tx pgx.Tx, ws uuid.UUID, parent *deployment.Plan, abandoned map[uuid.UUID]string, replacement *deployment.Replacement) error {
	if replacement != nil {
		delete(abandoned, replacement.Server)
	}
	parent.Effects = append(parent.Effects, abandonedEffects(abandoned)...)
	parent.Distributed.Abandoned = abandonedIDs(abandoned)
	if replacement == nil {
		return nil
	}
	parent.Distributed.Abandoned = append(parent.Distributed.Abandoned, replacement.Server)
	slices.SortFunc(parent.Distributed.Abandoned, compareUUID)
	effects, err := replacementEffects(ctx, tx, ws, *replacement, parent.ServerID, parent.Specification)
	if err != nil {
		return err
	}
	parent.Replacement = replacement
	parent.Effects = append(parent.Effects, effects...)
	return nil
}

// primaryUnitFor is the unit that takes the primary role on the new server. An attempt already
// heading there keeps its unit, so it finds what it started. Otherwise it's the environment's unit
// already on that server, so the host keeps one, or a new one. A unit never changes server, so a
// former primary's identity, and the volumes it labels, can't come back.
func primaryUnitFor(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, v visualEnvironment, server uuid.UUID) (uuid.UUID, error) {
	if v.Server != nil && *v.Server == server {
		return v.Unit, nil
	}
	q := db.New(tx)
	unit, err := q.SecondaryUnitOnServer(ctx, db.SecondaryUnitOnServerParams{WorkspaceID: ws, EnvironmentID: env, ServerID: &server})
	if !errors.Is(err, pgx.ErrNoRows) {
		return unit, err
	}
	unit = uuid.New()
	return unit, q.InsertSecondaryUnit(ctx, db.InsertSecondaryUnitParams{WorkspaceID: ws, ID: unit, EnvironmentID: env, ServerID: &server})
}

// adoptReplacement gives the new primary the environment's primary role and its volumes and
// resources, before the apply records them as usual.
func adoptReplacement(ctx context.Context, tx pgx.Tx, ws uuid.UUID, v visualEnvironment, plan deployment.Plan) error {
	r := plan.Replacement
	if err := requireStillRevoked(ctx, tx, ws, []uuid.UUID{r.Server}); err != nil {
		return err
	}
	previous, err := appliedVisualPlan(ctx, tx, ws, plan.EnvironmentID)
	if err != nil {
		return err
	}
	// The environment may have been replaced already since this plan was reviewed.
	if primary, continueOn, err := revokedPrimary(ctx, tx, ws, v, previous); err != nil {
		return err
	} else if primary != r.Server || continueOn != nil && *continueOn != plan.ServerID {
		return domain.Conflict
	}
	q := db.New(tx)
	if plan.UnitID != v.Unit {
		if unit, err := q.SecondaryUnitOnServer(ctx, db.SecondaryUnitOnServerParams{WorkspaceID: ws, EnvironmentID: plan.EnvironmentID, ServerID: &plan.ServerID}); err != nil || unit != plan.UnitID {
			if err == nil || errors.Is(err, pgx.ErrNoRows) {
				err = domain.Conflict
			}
			return err
		}
		if err := q.DemotePrimaryUnit(ctx, db.DemotePrimaryUnitParams{WorkspaceID: ws, ID: v.Unit}); err != nil {
			return err
		}
		if err := q.PromoteUnit(ctx, db.PromoteUnitParams{WorkspaceID: ws, ID: plan.UnitID}); err != nil {
			return err
		}
		if err := q.MoveEnvironmentVolumes(ctx, db.MoveEnvironmentVolumesParams{UnitID: plan.UnitID, WorkspaceID: ws, EnvironmentID: plan.EnvironmentID}); err != nil {
			return err
		}
		if err := q.MoveEnvironmentResources(ctx, db.MoveEnvironmentResourcesParams{UnitID: plan.UnitID, WorkspaceID: ws, EnvironmentID: plan.EnvironmentID}); err != nil {
			return err
		}
	}
	declared := []uuid.UUID{}
	for _, volume := range plan.Specification.Volumes {
		declared = append(declared, volume.ID)
	}
	if err := q.DropUnusedAbandonedVolumes(ctx, db.DropUnusedAbandonedVolumesParams{WorkspaceID: ws, EnvironmentID: plan.EnvironmentID, Abandoned: r.AbandonedVolumes, Declared: declared}); err != nil {
		return err
	}
	return q.ReleaseRevokedRouteClaims(ctx, db.ReleaseRevokedRouteClaimsParams{WorkspaceID: ws, EnvironmentID: plan.EnvironmentID})
}
