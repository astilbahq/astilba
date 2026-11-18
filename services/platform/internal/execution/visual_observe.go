package execution

import (
	"context"
	"errors"

	db "github.com/astilbahq/astilba/services/platform/db/generated"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) ObserveVisual(ctx context.Context, p store.Principal, ws, pid, env uuid.UUID, volume *uuid.UUID) (api.Operation, error) {
	var out api.Operation
	err := s.scope(ctx, p, ws, workspaceAccess{Write: true, Owner: volume != nil, Recent: volume != nil}, "deployment.observe", env, func(tx pgx.Tx) error {
		v, err := loadVisualEnvironment(ctx, tx, ws, pid, env)
		if err != nil {
			return err
		}
		out, err = s.observeVisual(ctx, tx, ws, env, v, volume)
		return err
	})
	return operationFor(p, out), err
}

// observeVisual asks v's server to report its runtime, or with volume, to delete that volume of
// the environment's, which no plan or draft may mount.
func (s *Service) observeVisual(ctx context.Context, tx pgx.Tx, ws, env uuid.UUID, v visualEnvironment, volume *uuid.UUID) (api.Operation, error) {
	if v.Server == nil {
		return api.Operation{}, domain.Invalid("server")
	}
	if _, err := s.visualServer(ctx, tx, ws, *v.Server); err != nil {
		return api.Operation{}, err
	}
	if volume != nil {
		// The draft and the plan the server runs are read under the lock that a new draft or plan
		// using the volume takes, so the delete and it can't both go ahead.
		var err error
		if v.Spec, err = store.LockedDraft(ctx, db.New(tx), ws, env); err != nil {
			return api.Operation{}, err
		}
	}
	var id uuid.UUID
	err := tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id IS NOT NULL", ws, env).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = tx.QueryRow(ctx, "SELECT visual_plan_id FROM execution_operations WHERE workspace_id=$1 AND environment_id=$2 AND visual_plan_id IS NOT NULL ORDER BY created_at DESC LIMIT 1", ws, env).Scan(&id)
	}
	if err != nil {
		return api.Operation{}, err
	}
	plan, err := readPlan(ctx, tx, ws, env, id)
	if err != nil {
		return api.Operation{}, err
	}
	if plan.Distributed != nil {
		if volume == nil {
			return s.insertCoordination(ctx, tx, ws, uuid.New(), "observe", plan)
		}
		plan = plan.PrimaryPlan()
	}
	action := "observe"
	if volume != nil {
		action = "delete-volume"
		var protected bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM supported_upgrades WHERE workspace_id=$1 AND environment_id=$2 AND (plan->>'targetVolumeId'=$3 OR EXISTS(SELECT 1 FROM jsonb_array_elements(plan->'source'->'specification'->'applications') a, jsonb_array_elements(a->'mounts') m WHERE a->>'id'=plan->>'resourceId' AND m->>'volumeId'=$3)) AND (status->>'phase' NOT IN ('planned','rejected','complete','returned') OR (status->>'keepUntil')::timestamptz>now()))`, ws, env, volume.String()).Scan(&protected); err != nil {
			return api.Operation{}, err
		}
		if protected {
			return api.Operation{}, domain.VolumeInUse
		}
		deletions, err := store.VolumeDeletions(ctx, db.New(tx), ws, env, []uuid.UUID{*volume})
		if err != nil {
			return api.Operation{}, err
		}
		switch deletions[*volume] {
		case store.VolumeDeleting:
			return api.Operation{}, domain.VolumeDeleting
		case store.VolumeDeleted:
			return api.Operation{}, domain.VolumeDeleted
		}
		var ok bool
		if err := tx.QueryRow(ctx, "SELECT true FROM managed_volumes WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3 AND id=$4 AND NOT deleted", ws, env, v.Unit, *volume).Scan(&ok); err != nil {
			return api.Operation{}, err
		}
		// Neither the draft nor the plan the server runs may mount it.
		for _, spec := range []domain.Specification{v.Spec, plan.Specification} {
			for _, a := range spec.Applications {
				for _, m := range a.Mounts {
					if m.VolumeID == *volume {
						return api.Operation{}, domain.VolumeInUse
					}
				}
			}
		}
	}
	return s.insertVisual(ctx, tx, ws, uuid.New(), deployment.Payload{Action: action, Plan: plan, VolumeID: volume})
}
