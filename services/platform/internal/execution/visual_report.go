package execution

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) visualReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil && p.Action == "preview-cleanup" {
			if r.State != "succeeded" {
				return nil
			}
			var ws uuid.UUID
			if err := tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
				return err
			}
			return s.finalizePreviewCleanup(ctx, tx, ws, c)
		}
	}
	if c.Visual == nil || r.Observation == nil {
		if r.State != "succeeded" {
			return nil
		}
		return domain.Invalid("observation")
	}
	var payload deployment.Payload
	var observed deployment.Observation
	if json.Unmarshal(*c.Visual, &payload) != nil || json.Unmarshal(*r.Observation, &observed) != nil || len(*r.Observation) > deployment.ObservationLimit {
		return domain.Invalid("observation")
	}
	if err := observed.Validate(time.Now()); err != nil {
		return err
	}
	plan := payload.Plan
	if plan.ServerID != c.ServerId || plan.EnvironmentID != c.EnvironmentId || payload.Digest() != c.PlanHash {
		return domain.Forbidden
	}
	var ws uuid.UUID
	if err := tx.QueryRow(ctx, "SELECT workspace_id FROM servers WHERE id=$1", c.ServerId).Scan(&ws); err != nil {
		return err
	}
	allowed := map[uuid.UUID]bool{}
	for _, a := range plan.Specification.Applications {
		allowed[a.ID] = true
	}
	rows, err := tx.Query(ctx, "SELECT id FROM managed_resources WHERE workspace_id=$1 AND environment_id=$2 AND unit_id=$3", ws, c.EnvironmentId, plan.UnitID)
	if err != nil {
		return err
	}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		allowed[id] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	seen := map[uuid.UUID]bool{}
	for _, resource := range observed.Resources {
		if !allowed[resource.ID] {
			return domain.Invalid("observation.resources")
		}
		seen[resource.ID] = true
	}
	if payload.Action == "resolve" && r.State == "succeeded" {
		plan.Images = observed.Images
		plan.ExpectedState = observed.StateHash
		if observed.InfrastructureDrift && !plan.AcceptDrift {
			r.State = "failed"
			r.Note = "Ingress drift requires an explicit reconciliation plan."
			return nil
		}
		if observed.ResourceDrift && !plan.AcceptDrift {
			r.State = "failed"
			r.Note = "Host drift was detected. Review it and explicitly request a reconciliation plan."
			return nil
		}
		if err := configureRelease(ctx, tx, ws, &plan); err != nil {
			r.State = "failed"
			r.Note = "Protected release is unavailable for this resolved change. Review its compatibility and agent capabilities."
			return nil
		}
		plan = plan.WithHash()
		if plan.Validate(true) != nil {
			return domain.Invalid("images")
		}
		raw, _ := stablejson.Marshal(plan)
		_, err := tx.Exec(ctx, "INSERT INTO visual_plans(id,workspace_id,environment_id,plan) VALUES($1,$2,$3,$4) ON CONFLICT(id) DO NOTHING", plan.ID, ws, c.EnvironmentId, raw)
		return err
	}

	var previous *uuid.UUID
	err = tx.QueryRow(ctx, "SELECT plan_id FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2", ws, c.EnvironmentId).Scan(&previous)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if payload.Action == "deploy" && observed.Applied && r.State == "succeeded" {
		if observed.OmittedResources != 0 || observed.ResourceDrift || len(observed.Resources) != len(plan.Specification.Applications) {
			return domain.Invalid("observation.resources")
		}
		for _, a := range plan.Specification.Applications {
			if !seen[a.ID] {
				return domain.Invalid("observation.resources")
			}
		}
		for _, resource := range observed.Resources {
			if resource.Drift || (resource.State != "running" && resource.State != "healthy") || resource.Image != plan.Images[resource.ID.String()] {
				return domain.Invalid("observation.resources")
			}
			if plan.Recipe == deployment.ReplicaRecipe {
				wanted := plan.InstanceIDs(resource.ID)
				if len(resource.Instances) != len(wanted) {
					return domain.Invalid("observation.instances")
				}
				members := map[uuid.UUID]bool{}
				for _, id := range wanted {
					members[id] = true
				}
				for _, instance := range resource.Instances {
					if !members[instance.InstanceID] || instance.State != "running" && instance.State != "healthy" {
						return domain.Invalid("observation.instances")
					}
				}
			}
		}
		if observed.InfrastructureDrift {
			return domain.Invalid("observation.infrastructureDrift")
		}
		if plan.Distributed == nil && plan.Local == nil {
			if _, err := tx.Exec(ctx, `DELETE FROM cross_host_runtime WHERE workspace_id=$1 AND environment_id=$2`, ws, c.EnvironmentId); err != nil {
				return err
			}
		}
		if previous == nil || *previous != plan.ID {
			if err := recordRuntimeOutcome(ctx, tx, ws, c.EnvironmentId, c.Id, portability.RuntimeOutcome{Kind: "applied", Plan: plan.ID}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2`, ws, c.EnvironmentId, plan.RevisionID); err != nil {
			return err
		}
		previous = &plan.ID
		claims, err := deployment.Claims(plan)
		if err != nil {
			return domain.Invalid("routes")
		}
		if _, err := tx.Exec(ctx, "DELETE FROM route_claims WHERE server_id=$1 AND workspace_id=$2 AND environment_id=$3 AND NOT (claim=ANY($4::text[]))", c.ServerId, ws, c.EnvironmentId, claims); err != nil {
			return err
		}
		ports := requiredPorts(plan.Specification)
		var raw []byte
		if err := tx.QueryRow(ctx, "SELECT ingress_ports FROM servers WHERE id=$1", c.ServerId).Scan(&raw); err != nil {
			return err
		}
		var old []int
		if json.Unmarshal(raw, &old) != nil {
			return errors.New("invalid server ports")
		}
		knownPorts := map[int]bool{}
		for _, p := range old {
			knownPorts[p] = true
		}
		for _, p := range ports {
			knownPorts[p] = true
		}
		old = []int{}
		for p := range knownPorts {
			old = append(old, p)
		}
		sort.Ints(old)
		raw, _ = stablejson.Marshal(old)
		if _, err := tx.Exec(ctx, "UPDATE servers SET ingress_ports=$2 WHERE id=$1", c.ServerId, raw); err != nil {
			return err
		}
	}
	if payload.Action == "delete-volume" && r.State == "succeeded" && payload.VolumeID != nil {
		if _, err := tx.Exec(ctx, "UPDATE managed_volumes SET deleted=true WHERE workspace_id=$1 AND environment_id=$2 AND id=$3", ws, c.EnvironmentId, *payload.VolumeID); err != nil {
			return err
		}
	}
	if plan.Local != nil && payload.Action == "delete-volume" {
		// Removing an unused primary volume must not replace the group snapshot
		// with a primary-only observation.
		return nil
	}
	observed.Images = nil
	raw, _ := stablejson.Marshal(observed)
	_, err = tx.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,environment_id) DO UPDATE SET observation=EXCLUDED.observation,observed_at=now(),plan_id=EXCLUDED.plan_id`, ws, c.EnvironmentId, plan.UnitID, c.ServerId, previous, raw)
	return err
}
