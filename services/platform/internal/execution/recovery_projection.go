package execution

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type recoveryProjection struct {
	environment portability.Row
	visual      portability.Row
	runtime     []portability.Row
	units       []portability.Row
	adopt       bool
	planID      uuid.UUID
}

func recoveryProjections(ctx context.Context, tx pgx.Tx, before, incoming portability.Archive, roots map[uuid.UUID]recoveringRoot) (map[uuid.UUID]recoveryProjection, error) {
	out := map[uuid.UUID]recoveryProjection{}
	own := map[uuid.UUID]portability.Archive{}
	for parent, root := range roots {
		var raw []byte
		if err := tx.QueryRow(ctx, `SELECT decoded FROM recovery_input_checkpoints WHERE parent_id=$1 AND sequence=0`, parent).Scan(&raw); err != nil {
			return nil, err
		}
		// The loader already authenticated and validated this transaction-local base.
		var a portability.Archive
		if json.Unmarshal(raw, &a) != nil {
			return nil, portability.ErrArchive
		}
		own[root.Command.EnvironmentId] = recoveryProjectionArchive(a, root.Command.EnvironmentId)
	}
	currentEnv := map[uuid.UUID]portability.Row{}
	for _, row := range before.Tables["environments"] {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		currentEnv[id] = row
	}
	rows := append([]portability.Row{}, incoming.Tables["environments"]...)
	seen := map[uuid.UUID]bool{}
	for _, row := range rows {
		id, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		seen[id] = true
	}
	for id, row := range currentEnv {
		if !seen[id] {
			rows = append(rows, row)
		}
	}
	for _, row := range rows {
		env, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return nil, err
		}
		candidate := incoming
		if !seen[env] {
			candidate = before
		}
		if base, exists := own[env]; exists {
			candidate = base
			row = nil
			for _, r := range base.Tables["environments"] {
				id, err := recoveryValue[uuid.UUID](r, "id")
				if err != nil {
					return nil, err
				}
				if id == env {
					row = r
					break
				}
			}
			if row == nil {
				return nil, portability.ErrArchive
			}
		}
		newApplied, err := recoveryValue[*uuid.UUID](row, "applied_revision_id")
		if err != nil {
			return nil, err
		}
		oldRow, known := currentEnv[env]
		adopt := !known
		if known {
			oldApplied, err := recoveryValue[*uuid.UUID](oldRow, "applied_revision_id")
			if err != nil {
				return nil, err
			}
			oldPlan, err := recoveryRuntimePlan(before, env)
			if err != nil {
				return nil, err
			}
			newPlan, err := recoveryRuntimePlan(candidate, env)
			if err != nil {
				return nil, err
			}
			if newApplied != nil && (oldApplied == nil || !sameID(oldApplied, newApplied) || oldPlan != newPlan) {
				oldGen, err := recoveryValue[int64](oldRow, "execution_generation")
				if err != nil {
					return nil, err
				}
				newGen, err := recoveryValue[int64](row, "execution_generation")
				if err != nil {
					return nil, err
				}
				if newGen <= oldGen {
					if _, selected := own[env]; selected {
						return nil, errors.New("known applied source conflicts with the recovered parent")
					}
				} else {
					if oldApplied != nil && *oldApplied != *newApplied {
						oldAt, err := recoveryRevisionTime(before, *oldApplied)
						if err != nil {
							return nil, err
						}
						newAt, err := recoveryRevisionTime(candidate, *newApplied)
						if err != nil {
							return nil, err
						}
						if !oldAt.Before(newAt) {
							return nil, errors.New("source runtime would rewind known applied history")
						}
					}
					adopt = true
				}
			}
		}
		if !adopt {
			candidate = before
			row = oldRow
		}
		projection, err := recoveryProjectionFrom(candidate, env, row)
		if err != nil {
			return nil, err
		}
		projection.adopt = adopt
		if _, selected := own[env]; selected && projection.planID == uuid.Nil {
			return nil, errors.New("recovered parent lacks its applied source projection")
		}
		out[env] = projection
	}
	return out, nil
}
func recoveryRuntimePlan(a portability.Archive, env uuid.UUID) (uuid.UUID, error) {
	for _, row := range a.Tables["visual_runtime"] {
		id, err := recoveryValue[uuid.UUID](row, "environment_id")
		if err != nil {
			return uuid.Nil, err
		}
		if id == env {
			return recoveryValue[uuid.UUID](row, "plan_id")
		}
	}
	return uuid.Nil, nil
}
func recoveryRevisionTime(a portability.Archive, id uuid.UUID) (time.Time, error) {
	for _, row := range a.Tables["specification_revisions"] {
		key, err := recoveryValue[uuid.UUID](row, "id")
		if err != nil {
			return time.Time{}, err
		}
		if key == id {
			return recoveryValue[time.Time](row, "created_at")
		}
	}
	return time.Time{}, errors.New("applied revision history is incomplete")
}
func recoveryProjectionFrom(a portability.Archive, env uuid.UUID, row portability.Row) (recoveryProjection, error) {
	out := recoveryProjection{environment: row, runtime: []portability.Row{}, units: []portability.Row{}}
	for _, r := range a.Tables["visual_runtime"] {
		id, err := recoveryValue[uuid.UUID](r, "environment_id")
		if err != nil {
			return out, err
		}
		if id == env {
			out.visual = r
			break
		}
	}
	if out.visual == nil {
		return out, nil
	}
	id, err := recoveryValue[*uuid.UUID](out.visual, "plan_id")
	if err != nil {
		return out, err
	}
	applied, err := recoveryValue[*uuid.UUID](row, "applied_revision_id")
	if err != nil {
		return out, err
	}
	if id == nil {
		if applied != nil {
			return out, errors.New("applied revision has no runtime plan")
		}
		return out, nil
	}
	out.planID = *id
	var plan deployment.Plan
	found := false
	for _, r := range a.Tables["visual_plans"] {
		key, err := recoveryValue[uuid.UUID](r, "id")
		if err != nil {
			return out, err
		}
		if key == *id {
			if json.Unmarshal(r["plan"], &plan) != nil {
				return out, portability.ErrArchive
			}
			found = true
			break
		}
	}
	if !found || plan.Validate(true) != nil || plan.ID != *id || plan.EnvironmentID != env || applied == nil || plan.RevisionID != *applied {
		return out, errors.New("applied runtime differs from its immutable plan")
	}
	active := map[uuid.UUID]deployment.Plan{}
	if plan.Distributed == nil {
		active[plan.UnitID] = plan
	} else {
		for _, unit := range plan.ActiveUnits() {
			active[unit.Plan.UnitID] = unit.Plan
		}
	}
	for _, r := range a.Tables["deployment_units"] {
		unit, err := recoveryValue[uuid.UUID](r, "id")
		if err != nil {
			return out, err
		}
		if expected, ok := active[unit]; ok {
			server, err := recoveryValue[*uuid.UUID](r, "server_id")
			if err != nil {
				return out, err
			}
			environment, err := recoveryValue[uuid.UUID](r, "environment_id")
			if err != nil {
				return out, err
			}
			if server == nil || *server != expected.ServerID || environment != env {
				return out, errors.New("applied runtime has incompatible unit ownership")
			}
			out.units = append(out.units, r)
		}
	}
	if len(out.units) != len(active) {
		return out, errors.New("applied runtime unit bindings are incomplete")
	}
	if plan.Distributed != nil {
		for _, r := range a.Tables["cross_host_runtime"] {
			group, err := recoveryValue[uuid.UUID](r, "plan_id")
			if err != nil {
				return out, err
			}
			unit, err := recoveryValue[uuid.UUID](r, "unit_id")
			if err != nil {
				return out, err
			}
			if expected, ok := active[unit]; ok && group == plan.ID {
				var inventory struct {
					Plan deployment.Plan `json:"plan"`
				}
				if json.Unmarshal(r["inventory"], &inventory) != nil || inventory.Plan.Hash != expected.Hash {
					return out, errors.New("distributed source inventory differs from its applied plan")
				}
				out.runtime = append(out.runtime, r)
			}
		}
		if len(out.runtime) != len(active) {
			return out, errors.New("distributed source inventory is incomplete")
		}
	}
	return out, nil
}
func applyRecoveryProjection(table string, row portability.Row, projections map[uuid.UUID]recoveryProjection) (bool, error) {
	if table == "visual_runtime" || table == "cross_host_runtime" {
		return false, nil
	}
	if table != "environments" {
		return true, nil
	}
	env, err := recoveryValue[uuid.UUID](row, "id")
	if err != nil {
		return false, err
	}
	if p, exists := projections[env]; exists {
		row["applied_revision_id"] = p.environment["applied_revision_id"]
	}
	return true, nil
}
func appendRecoveryProjectionRows(a *portability.Archive, projections map[uuid.UUID]recoveryProjection) error {
	for _, p := range projections {
		if p.visual != nil {
			a.Tables["visual_runtime"] = append(a.Tables["visual_runtime"], maps.Clone(p.visual))
		}
		for _, row := range p.runtime {
			a.Tables["cross_host_runtime"] = append(a.Tables["cross_host_runtime"], maps.Clone(row))
		}
		for _, binding := range p.units {
			id, err := recoveryValue[uuid.UUID](binding, "id")
			if err != nil {
				return err
			}
			for _, row := range a.Tables["deployment_units"] {
				key, err := recoveryValue[uuid.UUID](row, "id")
				if err != nil {
					return err
				}
				if key == id {
					old, err := recoveryValue[*uuid.UUID](row, "server_id")
					if err != nil {
						return err
					}
					target, err := recoveryValue[*uuid.UUID](binding, "server_id")
					if err != nil {
						return err
					}
					if old != nil && target != nil && *old != *target {
						return errors.New("source adoption would move a bound deployment unit")
					}
					row["server_id"] = binding["server_id"]
				}
			}
		}
	}
	return nil
}
func stageRecoveryProjections(ctx context.Context, tx pgx.Tx, ws uuid.UUID, projections map[uuid.UUID]recoveryProjection) error {
	for env, p := range projections {
		if p.adopt {
			if _, err := tx.Exec(ctx, `DELETE FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2`, ws, env); err != nil {
				return err
			}
		}
	}
	return nil
}

func restoreRecoveryGrants(ctx context.Context, tx pgx.Tx, ws uuid.UUID, projections map[uuid.UUID]recoveryProjection, roots map[uuid.UUID]recoveringRoot) error {
	selected := map[uuid.UUID]recoveringRoot{}
	for _, root := range roots {
		selected[root.Command.EnvironmentId] = root
	}
	for env, projection := range projections {
		plans := []uuid.UUID{}
		if projection.planID != uuid.Nil {
			plans = append(plans, projection.planID)
		}
		root, interrupted := selected[env]
		if interrupted && root.Body.Maintenance == nil {
			var phase string
			if err := tx.QueryRow(ctx, `SELECT phase FROM cross_host_operations WHERE workspace_id=$1 AND id=$2`, ws, root.Command.Id).Scan(&phase); err != nil {
				return err
			}
			switch phase {
			case "restrict", "complete":
				plans = []uuid.UUID{root.Body.Plan.ID}
			case "abort-restrict":
			default:
				// Until retirement is committed, both the applied source and the
				// admitted target need their frozen plan associations.
				plans = append(plans, root.Body.Plan.ID)
			}
		}
		if err := projectCrossHostGrants(ctx, tx, ws, env, plans); err != nil {
			return err
		}
	}
	return nil
}

func recoveryProjectionArchive(a portability.Archive, env uuid.UUID) portability.Archive {
	out := a
	out.Tables = map[string][]portability.Row{}
	for _, table := range []string{"environments", "visual_runtime", "cross_host_runtime", "deployment_units", "specification_revisions", "visual_plans"} {
		for _, row := range a.Tables[table] {
			field := "environment_id"
			if table == "environments" {
				field = "id"
			}
			var id uuid.UUID
			_ = json.Unmarshal(row[field], &id)
			if id == env {
				out.Tables[table] = append(out.Tables[table], row)
			}
		}
	}
	return out
}
