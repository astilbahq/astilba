package execution

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/portability"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (s *Service) crossHostReport(ctx context.Context, tx pgx.Tx, c api.ExecutionCommand, r *api.AgentReport) error {
	if c.Kind != "crosshost" || c.ParentId == nil {
		if r.Crosshost != nil {
			return domain.Invalid("crosshost")
		}
		return nil
	}
	var command deployment.CrossHostCommand
	if c.Crosshost == nil || hostnetwork.Decode(*c.Crosshost, &command) != nil || command.Digest() != c.PlanHash || command.ParentID != *c.ParentId {
		return domain.Invalid("crosshost")
	}
	if command.Phase == "checkpoint-closed" {
		if c.Id != deployment.RecoveryCloseID(command.ParentID, c.ResourceId) {
			return domain.Invalid("recovery.receipt")
		}
		if _, err := tx.Exec(ctx, `UPDATE execution_operations SET cancel_requested=true WHERE id=$1`, command.ParentID); err != nil {
			return err
		}
	}
	if r.State != "succeeded" {
		return nil
	}
	if r.Crosshost == nil {
		return domain.Invalid("crosshost.receipt")
	}
	var receipt deployment.CrossHostReceipt
	if hostnetwork.Decode(*r.Crosshost, &receipt) != nil || receipt.ParentID != command.ParentID || receipt.GroupHash != command.GroupHash || receipt.UnitID != c.ResourceId || receipt.PlanHash != command.Unit.Plan.Hash || receipt.Phase != command.Phase {
		return domain.Invalid("crosshost.receipt")
	}
	if err := receipt.Observation.Validate(time.Now()); err != nil {
		return err
	}
	if len(*r.Crosshost) > deployment.ObservationLimit+2048 {
		return domain.Invalid("crosshost.observation")
	}
	allowed := map[uuid.UUID]bool{}
	for _, app := range command.Unit.Plan.LocalApplications() {
		allowed[app.ID] = true
	}
	for _, app := range command.Unit.Previous.LocalApplications() {
		allowed[app.ID] = true
	}
	for _, resource := range receipt.Observation.Resources {
		if !allowed[resource.ID] {
			return domain.Invalid("crosshost.resources")
		}
	}
	if command.Phase == "complete" {
		plan := command.Unit.Plan
		if command.UseTarget {
			if command.Maintenance == nil || command.Maintenance.Target == nil {
				return domain.Invalid("crosshost.target")
			}
			plan = *command.Maintenance.Target
		}
		if !receipt.Observation.Applied || command.ValidateCompletion(plan, receipt.Observation) != nil {
			return domain.Invalid("crosshost.inventory")
		}
	}
	if command.Phase == "checkpoint" {
		var descriptor deployment.RecoveryDescriptor
		if c.RecoveryCheckpoint == nil || hostnetwork.Decode(*c.RecoveryCheckpoint, &descriptor) != nil || descriptor.Validate() != nil || descriptor.ParentID != command.ParentID || receipt.CheckpointHash != descriptor.Digest {
			return domain.Invalid("recovery.receipt")
		}
		updated, err := tx.Exec(ctx, `UPDATE cross_host_checkpoints SET acknowledged=true WHERE id=$1 AND parent_id=$2 AND descriptor=$3`, descriptor.CheckpointID, command.ParentID, []byte(*c.RecoveryCheckpoint))
		if err != nil {
			return err
		}
		if updated.RowsAffected() != 1 {
			return domain.Invalid("recovery.receipt")
		}
	}
	if command.Phase == "resolve" {
		if receipt.Observation.InfrastructureDrift || receipt.Observation.ResourceDrift {
			r.State = "failed"
			r.Note = "Host drift requires reconciliation before a cross-host review."
			return nil
		}
		if len(receipt.Observation.Images) != len(command.Unit.Plan.LocalApplications()) || len(receipt.Observation.StateHash) != 64 {
			return domain.Invalid("crosshost.images")
		}
		for _, app := range command.Unit.Plan.LocalApplications() {
			image, ok := receipt.Observation.Images[app.ID.String()]
			artifact, built := command.Unit.Plan.Artifacts[app.ID.String()]
			if !ok || built && artifact.ServerID != nil && image != artifact.Digest || (!built || artifact.ServerID == nil) && !deployment.ResolvedImage(*app.Source.Image, image) {
				return domain.Invalid("crosshost.images")
			}
			if _, replacing := command.Unit.Plan.Local.Requests[app.ID.String()]; !replacing && command.Unit.Previous.Images[app.ID.String()] != "" && image != command.Unit.Previous.Images[app.ID.String()] {
				return domain.Invalid("crosshost.sharedImage")
			}
		}
	}
	raw, _ := stablejson.Marshal(receipt)
	key := command.Phase + "/" + c.ResourceId.String()
	_, err := tx.Exec(ctx, `UPDATE cross_host_operations SET receipts=jsonb_set(receipts,ARRAY[$2]::text[],$3::jsonb,true) WHERE id=$1`, command.ParentID, key, raw)
	return err
}

func (s *Service) finalizeCrossHost(ctx context.Context, tx pgx.Tx, ws, operation uuid.UUID, p deployment.Plan, receipts map[string]deployment.CrossHostReceipt) error {
	return s.finalizeCrossHostUnits(ctx, tx, ws, operation, p, receipts, p.Distributed.Units)
}

func (s *Service) finalizeCrossHostUnits(ctx context.Context, tx pgx.Tx, ws, operation uuid.UUID, p deployment.Plan, receipts map[string]deployment.CrossHostReceipt, units []deployment.DistributedUnit) error {
	return s.recordCrossHostObservations(ctx, tx, ws, operation, p, receipts, units, "complete", true)
}

func (s *Service) recordCrossHostObservations(ctx context.Context, tx pgx.Tx, ws, operation uuid.UUID, p deployment.Plan, receipts map[string]deployment.CrossHostReceipt, units []deployment.DistributedUnit, phase string, apply bool) error {
	resources := map[uuid.UUID]deployment.Resource{}
	hosts := []deployment.HostObservation{}
	infrastructureDrift := false
	for _, u := range units {
		r, ok := receipts[phase+"/"+u.Plan.UnitID.String()]
		if !ok {
			return fmt.Errorf("missing final placement receipt")
		}
		hosts = append(hosts, deployment.HostObservation{ServerID: u.Plan.ServerID, UnitID: u.Plan.UnitID, State: "snapshot", ObservedAt: r.Observation.ObservedAt})
		infrastructureDrift = infrastructureDrift || r.Observation.InfrastructureDrift
		if !u.Plan.Local.Primary && len(u.Plan.LocalApplications()) == 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM cross_host_runtime WHERE workspace_id=$1 AND unit_id=$2`, ws, u.Plan.UnitID); err != nil {
				return err
			}
			continue
		}
		inventory, _ := stablejson.Marshal(struct {
			Plan        deployment.Plan        `json:"plan"`
			Observation deployment.Observation `json:"observation"`
		}{u.Plan, r.Observation})
		if _, err := tx.Exec(ctx, `INSERT INTO cross_host_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,inventory,cpu_millis,memory_mib) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(workspace_id,unit_id) DO UPDATE SET server_id=$4,plan_id=$5,inventory=$6,observed_at=now(),cpu_millis=$7,memory_mib=$8`, ws, p.EnvironmentID, u.Plan.UnitID, u.Plan.ServerID, p.ID, inventory, u.CPUMillis, u.MemoryMiB); err != nil {
			return err
		}
		for _, res := range r.Observation.Resources {
			current, ok := resources[res.ID]
			if !ok {
				current = res
				current.Instances = nil
				current.MemoryBytes = 0
				current.Logs = ""
				current.Note = ""
			}
			for i := range res.Instances {
				res.Instances[i].ServerID = &u.Plan.ServerID
				res.Instances[i].UnitID = &u.Plan.UnitID
				res.Instances[i].Image = u.Plan.Images[res.ID.String()]
			}
			if current.Image != res.Image {
				current.Image = "Multiple platform digests"
			}
			current.Instances = append(current.Instances, res.Instances...)
			current.MemoryBytes += res.MemoryBytes
			current.Drift = current.Drift || res.Drift
			if res.Logs != "" {
				current.Logs += crossHostText("Host "+u.Plan.ServerID.String()+":\n"+res.Logs+"\n", 2048/len(units))
			}
			if res.Note != "" && !strings.HasSuffix(res.Note, "instances running.") {
				current.Note = crossHostText(current.Note+" Host "+u.Plan.ServerID.String()+": "+res.Note, 180)
			}
			resources[res.ID] = current
		}
	}
	observation := deployment.Observation{Hosts: hosts, ObservedAt: time.Now().UTC(), Applied: true, Resources: []deployment.Resource{}, StateHash: p.Hash, InfrastructureDrift: infrastructureDrift}
	for _, app := range p.Specification.Applications {
		res, ok := resources[app.ID]
		if !ok {
			return errors.New("missing final resource inventory")
		}
		running, healthy := 0, 0
		for _, instance := range res.Instances {
			if instance.State == "running" || instance.State == "healthy" {
				running++
			}
			if instance.State == "healthy" {
				healthy++
			}
		}
		res.State = "degraded"
		if running == len(res.Instances) && running > 0 {
			res.State = "running"
			if healthy == running {
				res.State = "healthy"
			}
		}
		res.Note = fmt.Sprintf("%d of %d instances running.%s", running, len(res.Instances), res.Note)
		observation.ResourceDrift = observation.ResourceDrift || res.Drift
		observation.Resources = append(observation.Resources, res)
	}
	raw, _ := stablejson.Marshal(observation)
	if apply {
		var unchanged bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM visual_runtime WHERE workspace_id=$1 AND environment_id=$2 AND plan_id=$3)`, ws, p.EnvironmentID, p.ID).Scan(&unchanged); err != nil {
			return err
		}
		if !unchanged {
			if err := recordRuntimeOutcome(ctx, tx, ws, p.EnvironmentID, operation, portability.RuntimeOutcome{Kind: "applied", Plan: p.ID}); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE environments SET applied_revision_id=$3 WHERE workspace_id=$1 AND id=$2`, ws, p.EnvironmentID, p.RevisionID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO visual_runtime(workspace_id,environment_id,unit_id,server_id,plan_id,observation) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(workspace_id,environment_id) DO UPDATE SET plan_id=$5,observation=$6,observed_at=now()`, ws, p.EnvironmentID, p.UnitID, p.ServerID, p.ID, raw); err != nil {
		return err
	}
	if !apply {
		return nil
	}
	claims, err := deployment.Claims(p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `DELETE FROM route_claims WHERE workspace_id=$1 AND environment_id=$2 AND server_id=$3 AND NOT(claim=ANY($4::text[]))`, ws, p.EnvironmentID, p.ServerID, claims)
	return err
}

func crossHostText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	for !utf8.RuneStart(value[limit]) {
		limit--
	}
	return value[:limit]
}
