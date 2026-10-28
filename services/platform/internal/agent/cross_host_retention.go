package agent

import (
	"context"
	"errors"
	"os"
	"slices"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type poolContainer struct {
	ResourceID      uuid.UUID
	Name, ID, Image string
}
type retainedPool struct {
	PlanID     uuid.UUID
	Containers []poolContainer
}

func (e *Engine) crossHostPool(ctx context.Context, owner, remove, keep deployment.Plan) (retainedPool, error) {
	out := retainedPool{PlanID: remove.ID}
	for _, app := range remove.LocalApplications() {
		for _, id := range remove.InstanceIDs(app.ID) {
			if keep.ID != uuid.Nil && slices.Contains(keep.InstanceIDs(app.ID), id) {
				continue
			}
			name := deployment.UnitName(id)
			v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				continue
			}
			if err != nil {
				return out, err
			}
			if !visualOwned(v.Container.Config.Labels, owner) || v.Container.Config.Labels[labelResource] != app.ID.String() || v.Container.Config.Image != remove.Images[app.ID.String()] {
				return out, errors.New("frozen pool ownership mismatch")
			}
			out.Containers = append(out.Containers, poolContainer{ResourceID: app.ID, Name: name, ID: v.Container.ID, Image: v.Container.Config.Image})
		}
	}
	return out, nil
}

func (e *Engine) inspectPoolContainer(ctx context.Context, p deployment.Plan, item poolContainer) (*client.ContainerInspectResult, error) {
	v, err := e.Docker.ContainerInspect(ctx, item.Name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if v.Container.ID != item.ID || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != item.ResourceID.String() || v.Container.Config.Image != item.Image {
		return nil, errors.New("retained container identity changed")
	}
	return &v, nil
}

func (e *Engine) stopPoolContainer(ctx context.Context, p deployment.Plan, item poolContainer) error {
	v, err := e.inspectPoolContainer(ctx, p, item)
	if err != nil || v == nil {
		return err
	}
	if v.Container.State.Paused {
		return errors.New("a paused old container requires local inspection before retirement")
	}
	if v.Container.State.Running || v.Container.State.Restarting {
		seconds := 10
		if _, err := e.Docker.ContainerStop(ctx, item.ID, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) retainCrossHostPool(ctx context.Context, p deployment.Plan, state *crossHostState) error {
	if state.Retiring == nil {
		pool, err := e.crossHostPool(ctx, p, state.Unit.Previous, p)
		if err != nil {
			return err
		}
		state.Retiring = &pool
		if err := e.Journal.saveCrossHost(*state); err != nil {
			return err
		}
	}
	for _, item := range state.Retiring.Containers {
		if err := e.stopPoolContainer(ctx, p, item); err != nil {
			return err
		}
		e.point("cross-host-pool-stopped")
	}
	var pools []retainedPool
	err := e.Journal.db.View(func(tx *bolt.Tx) error { return e.Journal.get(tx, "retained-pools", p.UnitID.String(), &pools) })
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	found := false
	for _, pool := range pools {
		found = found || pool.PlanID == state.Retiring.PlanID
	}
	if !found && len(state.Retiring.Containers) > 0 {
		pools = append(pools, *state.Retiring)
	}
	for len(pools) > 2 {
		for _, item := range pools[0].Containers {
			v, err := e.inspectPoolContainer(ctx, p, item)
			if err != nil {
				return err
			}
			if v == nil {
				continue
			}
			if v.Container.State.Running || v.Container.State.Restarting || v.Container.State.Paused {
				return errors.New("an old retained pool was restarted; nothing running will be evicted")
			}
			if _, err := e.Docker.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
				return err
			}
		}
		pools = pools[1:]
	}
	return e.Journal.db.Update(func(tx *bolt.Tx) error { return e.Journal.put(tx, "retained-pools", p.UnitID.String(), pools) })
}

func (e *Engine) removeCrossHostCandidates(ctx context.Context, p, previous deployment.Plan) error {
	pool, err := e.crossHostPool(ctx, p, p, previous)
	if err != nil {
		return err
	}
	for _, item := range pool.Containers {
		if err := e.stopPoolContainer(ctx, p, item); err != nil {
			return err
		}
		if _, err := e.Docker.ContainerRemove(ctx, item.ID, client.ContainerRemoveOptions{RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		e.point("cross-host-pool-removed")
	}
	return nil
}
