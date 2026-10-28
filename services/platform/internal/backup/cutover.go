package backup

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// CutoverVolume validates a verified physical restore for a volume-only switch.
// A logical archive may verify under its original major while remaining
// incompatible with the currently deployed PostgreSQL data layout.
func CutoverVolume(p Policy, r Run) (uuid.UUID, error) {
	// Run history retains the verified archive's hash but omits its potentially
	// large role/extension lists. The compact manifest cannot reproduce that hash.
	if !r.Verified || r.State != "succeeded" || r.TargetID == uuid.Nil || r.PolicyID != p.ID || r.Manifest == nil || !SnapshotID(r.ManifestHash) || r.Manifest.PolicyID != p.ID || r.Manifest.EnvironmentID != p.EnvironmentID || r.Manifest.ResourceID != p.ResourceID || r.Manifest.Method != p.Method {
		return uuid.Nil, errors.New("the restored run does not match the source policy")
	}
	old := p.ResourceID
	switch p.Method {
	case "postgres-logical":
		found := false
		for _, a := range p.Plan.LocalApplications() {
			if a.ID != p.ResourceID || a.Postgres == nil {
				continue
			}
			if a.Source.Image == nil || r.Manifest.Image != *a.Source.Image || len(a.Mounts) != 1 || a.Mounts[0].Target != domain.PostgresMount(r.Manifest.Image) {
				return uuid.Nil, errors.New("the restored PostgreSQL image and physical layout differ from the frozen target")
			}
			old, found = a.Mounts[0].VolumeID, true
		}
		if !found {
			return uuid.Nil, errors.New("the restored database is not a primary source resource")
		}
	case QuiescedFiles:
	default:
		return uuid.Nil, errors.New("this policy has no supported volume cutover")
	}
	for _, v := range p.Plan.Specification.Volumes {
		if v.ID == r.TargetID {
			return uuid.Nil, errors.New("the restored target is already part of the source")
		}
	}
	return old, nil
}
