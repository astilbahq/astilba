package domain

import "github.com/google/uuid"

// Refusals to delete a volume, discard a restore's volume, or use a volume in a draft or plan. Each
// carries one stable field, which clients may rely on to explain it; docs/backups.md and
// docs/visual-deployments.md list them with the endpoints that return them.
var (
	// VolumeInUse refuses to delete a volume the draft, the plan the server runs or a supported
	// upgrade still uses.
	VolumeInUse = &Error{Code: "conflict", Message: "The draft, the deployed plan or a database upgrade still uses this volume.", Fields: []string{"volume.inUse"}}
	// VolumeDeleted refuses to delete a volume, or discard a restore's volume, that is already
	// deleted.
	VolumeDeleted = &Error{Code: "conflict", Message: "This volume is already deleted.", Fields: []string{"volume.deleted"}}
	// VolumeDeleting refuses to delete a volume while an earlier delete of it hasn't ended.
	VolumeDeleting = &Error{Code: "conflict", Message: "This volume is already being deleted.", Fields: []string{"volume.deleting"}}
	// UsesVolumeBeingDeleted refuses a new draft or plan that uses a volume of the environment's
	// that a delete that hasn't ended names, and UsesDeletedVolume one that uses a deleted volume.
	// Their fields are the values a delete of the volume is refused with. Neither names the volume:
	// the message can reach a credential, or a job's note, that may not read the configuration.
	UsesVolumeBeingDeleted = &Error{Code: "conflict", Message: "A volume the configuration uses is being deleted, so the configuration can't use it. If the delete fails or is cancelled, the volume is kept and can be used again.", Fields: []string{"volume.deleting"}}
	UsesDeletedVolume      = &Error{Code: "conflict", Message: "A volume the configuration uses was deleted, so the configuration can't use it. Remove it from the configuration, or use a new volume.", Fields: []string{"volume.deleted"}}
	// DiscardActive refuses to discard, or cut over to, a restore's volume while a discard of it
	// is still running.
	DiscardActive = &Error{Code: "conflict", Message: "This restore's volume is already being discarded.", Fields: []string{"discard.active"}}
	// RestoreServerMoved refuses to discard a restore's volume once the environment no longer
	// runs on the server the restore was made on, which holds the volume.
	RestoreServerMoved = &Error{Code: "conflict", Message: "The environment no longer runs on the server this restore was made on, so its volume can't be discarded.", Fields: []string{"restore.server"}}
)

// MountedVolumes returns the volumes spec's applications mount, each once, in the order they
// first appear. A Compose specification's mounts are derived from its files by ParseSpec.
func MountedVolumes(spec Specification) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	for _, a := range spec.Applications {
		for _, m := range a.Mounts {
			if !seen[m.VolumeID] {
				seen[m.VolumeID] = true
				out = append(out, m.VolumeID)
			}
		}
	}
	return out
}
