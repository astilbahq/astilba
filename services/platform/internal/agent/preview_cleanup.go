package agent

import (
	"context"
	"errors"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

func (j *Journal) previewProofCapacity(id uuid.UUID) error {
	return j.db.View(func(tx *bolt.Tx) error {
		b := tx.Bucket([]byte("preview-cleanups"))
		if b.Get([]byte(id.String())) == nil && b.Stats().KeyN >= 100 {
			return errors.New("pending preview cleanup receipts require acknowledgement")
		}
		return nil
	})
}
func (j *Journal) retirePreview(c api.ExecutionCommand) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		var u unitState
		if err := j.get(tx, "units", c.ResourceId.String(), &u); err != nil {
			// Absent units are normal when a build failed before its first deployment.
			if !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if u.Plan.Hash != "" && (u.Plan.ServerID != c.ServerId || u.Plan.EnvironmentID != c.EnvironmentId || u.Plan.UnitID != c.ResourceId) {
			return errors.New("preview journal ownership mismatch")
		}
		if err := tx.Bucket([]byte("units")).Delete([]byte(c.ResourceId.String())); err != nil {
			return err
		}
		if err := tx.Bucket([]byte("releases")).Delete([]byte(c.ResourceId.String())); err != nil {
			return err
		}
		return j.put(tx, "preview-cleanups", c.Id.String(), api.PreviewCleanupReceipt{OperationId: c.Id, Digest: c.PlanHash})
	})
}

// Validate every deterministic name before any destructive effect. The final
// pass also catches resources that appeared during cleanup rather than guessing.
func (e *Engine) previewResources(ctx context.Context, p deployment.Plan) ([]container.Summary, error) {
	allowed := map[string]bool{}
	for _, a := range p.Specification.Applications {
		allowed[a.ID.String()] = true
		v, err := e.Docker.ContainerInspect(ctx, p.ContainerName(a.ID), client.ContainerInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			return nil, err
		}
		if err == nil && (!visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != a.ID.String()) {
			return nil, errors.New("preview container ownership mismatch")
		}
	}
	list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: 101, Filters: client.Filters{}.Add("label", "com.astilba.unit="+p.UnitID.String())})
	if err != nil {
		return nil, err
	}
	if len(list.Items) > 100 {
		return nil, errors.New("preview resource limit exceeded")
	}
	for _, v := range list.Items {
		if !visualOwned(v.Labels, p) || !allowed[v.Labels[labelResource]] {
			return nil, errors.New("preview resource ownership mismatch")
		}
	}
	for _, vol := range p.Specification.Volumes {
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !visualOwned(v.Volume.Labels, p) || v.Volume.Labels[labelResource] != vol.ID.String() || v.Volume.Driver != "local" || len(v.Volume.Options) > 0 {
			return nil, errors.New("preview volume ownership mismatch")
		}
	}
	n, err := e.Docker.NetworkInspect(ctx, deployment.NetworkName(p.UnitID), client.NetworkInspectOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return nil, err
	}
	if err == nil && (!visualOwned(n.Network.Labels, p) || n.Network.Labels[labelResource] != p.UnitID.String() || n.Network.Driver != "bridge") {
		return nil, errors.New("preview network ownership mismatch")
	}
	return list.Items, nil
}
func (e *Engine) executePreviewCleanup(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, r Record) error {
	if p.Plan.Preview == nil || p.Plan.Specification.Compose != nil {
		return e.finish(c.Id, "failed", "A preview cleanup requires its frozen isolated identity.")
	}
	fail := func() error {
		return e.finish(c.Id, "needs_attention", "Preview cleanup could not verify ownership or removal. Its resource slot and data records remain reserved.")
	}
	if err := e.Journal.previewProofCapacity(c.Id); err != nil {
		return fail()
	}
	if _, err := e.previewResources(ctx, p.Plan); err != nil {
		return fail()
	}
	routing, err := e.Journal.routing()
	if err != nil {
		return err
	}
	if set, ok := routing.Current.Sets[p.Plan.UnitID.String()]; ok && set.EnvironmentID != p.Plan.EnvironmentID {
		return fail()
	}
	if routing.Pending != nil && r.Checkpoint == "accepted" {
		return fail()
	}
	if err = e.checkpoint(c.Id, "preview-routes"); err != nil {
		return err
	}
	withdrawn := p.Plan
	withdrawn.Specification.Routes = nil
	// The cleanup grant covers only withdrawing this unit; it never adopts drift.
	if err = e.publishReleaseRoutes(ctx, withdrawn); err != nil {
		return fail()
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Cleanup stopped after withdrawing preview traffic; resources are retained.")
	}
	if err = e.checkpoint(c.Id, "preview-services"); err != nil {
		return err
	}
	containers, err := e.previewResources(ctx, p.Plan)
	if err != nil {
		return fail()
	}
	for _, v := range containers {
		// Reinspect by immutable container ID immediately before removal.
		current, err := e.Docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil || !visualOwned(current.Container.Config.Labels, p.Plan) {
			return fail()
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
			return fail()
		}
		e.point("preview-container-removed")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Cleanup stopped after removing preview services; remaining data is retained.")
	}
	if err = e.checkpoint(c.Id, "preview-storage"); err != nil {
		return err
	}
	for _, vol := range p.Plan.Specification.Volumes {
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil || !visualOwned(v.Volume.Labels, p.Plan) || v.Volume.Labels[labelResource] != vol.ID.String() {
			return fail()
		}
		if _, err = e.Docker.VolumeRemove(ctx, v.Volume.Name, client.VolumeRemoveOptions{Force: false}); err != nil && !errdefs.IsNotFound(err) {
			return fail()
		}
		e.point("preview-volume-removed")
	}
	name := deployment.NetworkName(p.Plan.UnitID)
	n, err := e.Docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		if !visualOwned(n.Network.Labels, p.Plan) {
			return fail()
		}
		if _, err = e.Docker.NetworkRemove(ctx, n.Network.ID, client.NetworkRemoveOptions{}); err != nil && !errdefs.IsNotFound(err) {
			return fail()
		}
	} else if !errdefs.IsNotFound(err) {
		return fail()
	}
	containers, err = e.previewResources(ctx, p.Plan)
	if err != nil || len(containers) > 0 {
		return fail()
	}
	for _, vol := range p.Plan.Specification.Volumes {
		if _, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
			return fail()
		}
	}
	if _, err = e.Docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
		return fail()
	}
	if err = e.Journal.retirePreview(c); err != nil {
		return err
	}
	e.point("preview-cleanup-proof")
	return e.finish(c.Id, "succeeded", "Preview routes, services, network and owned data are absent.")
}
