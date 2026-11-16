package backup

import (
	"errors"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// CutoverVolume validates a verified physical restore for a volume-only switch.
func CutoverVolume(p Policy, r Run) (uuid.UUID, error) {
	// Run history retains the verified archive's hash but omits its potentially
	// large role/extension lists. The compact manifest cannot reproduce that hash.
	if !r.Verified || r.State != "succeeded" || r.PolicyID != p.ID || r.Manifest == nil || !SnapshotID(r.ManifestHash) {
		return uuid.Nil, errors.New("the restored run does not match the source policy")
	}
	return RestoredVolume(p, r.TargetID, *r.Manifest)
}

// RestoredVolume is the volume of p's plan that target, holding a verified restore of the archive
// m describes, switches in for. A logical archive may verify under its original major while
// remaining incompatible with the currently deployed PostgreSQL data layout.
func RestoredVolume(p Policy, target uuid.UUID, m Manifest) (uuid.UUID, error) {
	if target == uuid.Nil || m.PolicyID != p.ID || m.EnvironmentID != p.EnvironmentID || m.ResourceID != p.ResourceID || m.Method != p.Method {
		return uuid.Nil, errors.New("the restored archive does not match the source policy")
	}
	old := p.ResourceID
	switch p.Method {
	case "postgres-logical":
		found := false
		for _, a := range p.Plan.LocalApplications() {
			if a.ID != p.ResourceID || a.Postgres == nil {
				continue
			}
			if !OpensRestoredPostgres(a, m) {
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
		if v.ID == target {
			return uuid.Nil, errors.New("the restored target is already part of the source")
		}
	}
	return old, nil
}

// OpensRestoredPostgres reports whether a PostgreSQL application runs the image a logical restore
// initialised its target with, mounted where that image keeps its data. A data directory one major
// initialised cannot be opened by another.
func OpensRestoredPostgres(a domain.Application, m Manifest) bool {
	return a.Postgres != nil && a.Source.Image != nil && m.Image == *a.Source.Image && len(a.Mounts) == 1 && a.Mounts[0].Target == domain.PostgresMount(m.Image)
}
