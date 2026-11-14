package agent

import (
	"context"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// maxPurgeVolumes bounds what one purge inspects and deletes.
const maxPurgeVolumes = 1000

// volumeState is what deleting one volume meets on this host.
type volumeState int

const (
	volumeDeletable volumeState = iota
	volumeAbsent
	// volumeRetained is protected by an upgrade's retention period.
	volumeRetained
	// volumeUnverified could not be inspected, or its labels do not name the unit and the volume.
	volumeUnverified
)

// deletableVolume checks one volume before it is deleted, and returns the Docker name of one that
// may be.
func (e *Engine) deletableVolume(ctx context.Context, id uuid.UUID, p deployment.Plan, upgrades []upgradeState) (string, volumeState) {
	for _, u := range upgrades {
		if (id == u.Command.Plan.OldVolume() || id == u.Command.Plan.TargetVolumeID) && time.Now().Before(u.Status.KeepUntil) {
			return "", volumeRetained
		}
	}
	v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(id), client.VolumeInspectOptions{})
	switch {
	case errdefs.IsNotFound(err):
		return "", volumeAbsent
	case err != nil || !visualOwned(v.Volume.Labels, p) || v.Volume.Labels[labelResource] != id.String():
		return "", volumeUnverified
	}
	return v.Volume.Name, volumeDeletable
}

// executePurge deletes the retained volumes of a retired environment's unit, exactly the ones its
// owner confirmed. Every volume is checked before any is deleted: one that upgrade retention
// protects, or whose ownership labels do not name this unit and volume, stops the purge with
// nothing deleted. A volume that is already gone counts as deleted, so a purge can run again.
func (e *Engine) executePurge(ctx context.Context, c api.ExecutionCommand, p deployment.Payload) error {
	if p.Plan.Preview != nil || len(p.VolumeIDs) == 0 || len(p.VolumeIDs) > maxPurgeVolumes {
		return e.finish(c.Id, "failed", "A purge deletes the retained volumes of one retired environment.")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Purge cancelled before execution. Nothing was deleted.")
	}
	upgrades, err := e.Journal.upgrades()
	if err != nil {
		return err
	}
	present := []string{}
	for _, id := range p.VolumeIDs {
		switch name, state := e.deletableVolume(ctx, id, p.Plan, upgrades); state {
		case volumeRetained:
			return e.finish(c.Id, "failed", "Upgrade retention protects a retained volume. Nothing was deleted.")
		case volumeUnverified:
			return e.finish(c.Id, "needs_attention", "Ownership of a retained volume could not be verified. Nothing was deleted.")
		case volumeDeletable:
			present = append(present, name)
		}
	}
	if err = e.checkpoint(c.Id, "purge"); err != nil {
		return err
	}
	for _, name := range present {
		if _, err = e.Docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{Force: false}); err != nil && !errdefs.IsNotFound(err) {
			return e.finish(c.Id, "needs_attention", "A retained volume could not be deleted. Confirm no container uses it, then retry the operation on the host.")
		}
	}
	return e.finish(c.Id, "succeeded", "The retained volumes were deleted.")
}
