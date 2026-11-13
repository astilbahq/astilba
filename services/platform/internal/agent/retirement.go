package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// Retirement removes what one environment's unit runs on this host and keeps its named volumes,
// so restoring the environment can deploy onto the same data. Resources are found by their
// ownership labels rather than by the plan's services, so containers and networks left by older
// revisions, release commands or an interrupted first deployment go too. Every step tolerates
// what is already gone, so the command can run again after a crash or a failed step.

const retireResourceLimit = 200

// unitOwned is the identity every container and network a unit creates carries, whatever its kind.
func unitOwned(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels["com.astilba.unit"] == p.UnitID.String()
}

// retirementResources lists the unit's containers and networks, and refuses if anything labelled
// as the unit's names another server or environment.
func (e *Engine) retirementResources(ctx context.Context, p deployment.Plan) ([]container.Summary, []network.Summary, error) {
	unit := client.Filters{}.Add("label", "com.astilba.unit="+p.UnitID.String())
	containers, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: retireResourceLimit + 1, Filters: unit})
	if err != nil {
		return nil, nil, err
	}
	if len(containers.Items) > retireResourceLimit {
		return nil, nil, errors.New("retirement resource limit exceeded")
	}
	for _, v := range containers.Items {
		if !unitOwned(v.Labels, p) {
			return nil, nil, errors.New("container ownership mismatch")
		}
	}
	networks, err := e.Docker.NetworkList(ctx, client.NetworkListOptions{Filters: unit})
	if err != nil {
		return nil, nil, err
	}
	for _, n := range networks.Items {
		if !visualOwned(n.Labels, p) || n.Driver != "bridge" {
			return nil, nil, errors.New("network ownership mismatch")
		}
	}
	return containers.Items, networks.Items, nil
}

func (e *Engine) executeRetirement(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, r Record) error {
	if p.Plan.Preview != nil || p.Plan.Local != nil || p.Plan.Distributed != nil {
		return e.finish(c.Id, "failed", "Retirement removes one local unit; previews and cross-host placements have their own cleanup.")
	}
	attention := func(note string) error { return e.finish(c.Id, "needs_attention", note) }
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil {
		return err
	}
	if unit.Plan.Hash != "" && (unit.Plan.ServerID != c.ServerId || unit.Plan.EnvironmentID != c.EnvironmentId || unit.Plan.UnitID != c.ResourceId) {
		return attention("This host's journal records the unit for another environment. Nothing was removed.")
	}
	// Check every resource before the first effect.
	if _, _, err = e.retirementResources(ctx, p.Plan); err != nil {
		return attention("Ownership of this environment's containers or networks could not be verified. Nothing was removed.")
	}
	routing, err := e.Journal.routing()
	if err != nil {
		return err
	}
	if set, ok := routing.Current.Sets[p.Plan.UnitID.String()]; ok && set.EnvironmentID != p.Plan.EnvironmentID {
		return attention("The host's routes record this unit for another environment. Nothing was removed.")
	}
	if routing.Pending && r.Checkpoint == "accepted" {
		return attention("A route publication is unfinished on this host. Inspect ingress, then retry. Nothing was removed.")
	}
	if err = e.checkpoint(c.Id, "retire-routes"); err != nil {
		return err
	}
	if err = e.publishReleaseRoutes(ctx, withdrawnRoutes(p.Plan)); err != nil {
		return attention("This environment's routes could not be withdrawn, for example because ingress changed outside Astilba. Nothing else was removed.")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Retirement stopped after withdrawing its routes. Its services, networks and volumes are kept.")
	}
	// Schedules go before services, so that a job never runs against a half-removed unit.
	if err = e.checkpoint(c.Id, "retire-schedules"); err != nil {
		return err
	}
	if err = e.Journal.retireSchedules(c.ServerId, c.EnvironmentId); err != nil {
		return err
	}
	if err = e.checkpoint(c.Id, "retire-services"); err != nil {
		return err
	}
	containers, _, err := e.retirementResources(ctx, p.Plan)
	if err != nil {
		return attention("Ownership of this environment's containers changed during retirement. Its remaining services, networks and volumes are kept.")
	}
	for _, v := range containers {
		// Reinspect by immutable ID immediately before removal.
		current, err := e.Docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil || !unitOwned(current.Container.Config.Labels, p.Plan) {
			return attention("A container's ownership could not be verified before removal. Its remaining services, networks and volumes are kept.")
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
			return attention("A service could not be removed. Its remaining services, networks and volumes are kept.")
		}
		e.point("retire-container-removed")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Retirement stopped after removing its services. Its networks and volumes are kept.")
	}
	if err = e.checkpoint(c.Id, "retire-networks"); err != nil {
		return err
	}
	_, networks, err := e.retirementResources(ctx, p.Plan)
	if err != nil {
		return attention("Ownership of this environment's networks changed during retirement. Its networks and volumes are kept.")
	}
	for _, n := range networks {
		current, err := e.Docker.NetworkInspect(ctx, n.ID, client.NetworkInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil || !visualOwned(current.Network.Labels, p.Plan) {
			return attention("A network's ownership could not be verified before removal. Its networks and volumes are kept.")
		}
		if _, err = e.Docker.NetworkRemove(ctx, n.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return attention("A network could not be removed, for example because something outside Astilba still uses it. Its volumes are kept.")
		}
	}
	// Confirm, rather than assume, that nothing appeared while removing.
	containers, networks, err = e.retirementResources(ctx, p.Plan)
	if err != nil || len(containers) > 0 || len(networks) > 0 {
		return attention("Some of this environment's containers or networks are still present. Inspect the host and retry. Its volumes are kept.")
	}
	if err = e.Journal.retireUnit(c); err != nil {
		return err
	}
	return e.finish(c.Id, "succeeded", "Routes, services, networks and schedules were removed. Named volumes are kept.")
}

// retireSchedules removes the environment's backup and job schedules from both schedule buckets.
func (j *Journal) retireSchedules(server, env uuid.UUID) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		ids := []string{}
		if err := j.eachSchedule(tx, func(id string, s scheduleState) error {
			if s.Installation.Policy.ServerID == server && s.Installation.Policy.EnvironmentID == env {
				ids = append(ids, id)
			}
			return nil
		}); err != nil {
			return err
		}
		for _, id := range ids {
			for _, bucket := range []string{"schedules", "schedules-v2"} {
				if err := tx.Bucket([]byte(bucket)).Delete([]byte(id)); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// holdsUnit reports whether this journal holds a retirement's unit for its environment on this
// server: an applied plan, a release, a published route set, or a deployment command it ran, which
// a first deployment that failed after starting services or routes leaves without an applied plan.
func (j *Journal) holdsUnit(c api.ExecutionCommand) (bool, error) {
	held := false
	err := j.db.View(func(tx *bolt.Tx) error {
		var u unitState
		if err := j.get(tx, "units", c.ResourceId.String(), &u); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		var r releaseState
		if err := j.get(tx, "releases", c.ResourceId.String(), &r); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		for _, p := range []deployment.Plan{u.Plan, r.Plan} {
			if p.Hash != "" && p.EnvironmentID == c.EnvironmentId && p.ServerID == c.ServerId {
				held = true
				return nil
			}
		}
		routing, err := j.routingIn(tx)
		if err != nil {
			return err
		}
		if set, ok := routing.Current.Sets[c.ResourceId.String()]; ok && set.EnvironmentID == c.EnvironmentId {
			held = true
			return nil
		}
		return tx.Bucket([]byte("operations")).ForEach(func(k, _ []byte) error {
			if held || string(k) == c.Id.String() {
				return nil
			}
			var record Record
			if err := j.get(tx, "operations", string(k), &record); err != nil {
				return err
			}
			o := record.Command
			if o.Kind != "visual" || o.Visual == nil || o.ResourceId != c.ResourceId || o.EnvironmentId != c.EnvironmentId || o.ServerId != c.ServerId {
				return nil
			}
			var p deployment.Payload
			held = json.Unmarshal(*o.Visual, &p) == nil && p.Action == "deploy"
			return nil
		})
	})
	return held, err
}

// retireUnit deletes the unit's applied plan and release records. Its finished command records stay
// under the journal's bound like any others; their released values are already gone (see
// journal_redaction.go). Generation fencing still refuses any older command for the unit.
func (j *Journal) retireUnit(c api.ExecutionCommand) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var u unitState
		if err := j.get(tx, "units", c.ResourceId.String(), &u); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if u.Plan.Hash != "" && (u.Plan.ServerID != c.ServerId || u.Plan.EnvironmentID != c.EnvironmentId || u.Plan.UnitID != c.ResourceId) {
			return errors.New("unit journal ownership mismatch")
		}
		if err := tx.Bucket([]byte("units")).Delete([]byte(c.ResourceId.String())); err != nil {
			return err
		}
		return tx.Bucket([]byte("releases")).Delete([]byte(c.ResourceId.String()))
	})
}

// withdrawnRoutes is the plan without its routes, as retirement publishes it. Withdrawing never
// adopts ingress changed outside Astilba, and never makes a host change: the plan's HostChanges
// approved its deployment, not the host's current certificate email or managed egress, and adopting
// either could recreate the proxy every other environment on the host is served through. Removing a
// route set keeps the proxy's ports, so withdrawal needs no host change.
func withdrawnRoutes(p deployment.Plan) deployment.Plan {
	withdrawn := p
	withdrawn.Specification.Routes = nil
	withdrawn.AcceptDrift, withdrawn.HostChanges = false, false
	return withdrawn
}
