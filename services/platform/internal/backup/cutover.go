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
	return RestoredVolume(p, r.TargetID, *r.Manifest, r.RestoredImage(), r.RestoreRoles)
}

// RestoredVolume is the volume of p's plan that target, holding a verified restore of the archive
// m describes, switches in for. A logical restore's data was written by the release whose image
// is restored, which the frozen source must be able to open, in a cluster of the roles model
// restored. A database in the owner model never moves onto a cluster whose superuser is its user.
func RestoredVolume(p Policy, target uuid.UUID, m Manifest, restored, roles string) (uuid.UUID, error) {
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
			if err := OpensRestoredPostgres(a, restored); err != nil {
				return uuid.Nil, err
			}
			if a.Postgres.Owner() && roles != domain.PostgresOwner {
				return uuid.Nil, errors.New("the restore made the database user its cluster's superuser, which this database's role model doesn't allow")
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

// RestoredImage is the PostgreSQL image that filled a logical restore target. A run from an agent
// that predates the release rule restored in the archive's own image.
func (r Run) RestoredImage() string {
	if r.RestoreImage != "" {
		return r.RestoreImage
	}
	if r.Manifest == nil {
		return ""
	}
	return r.Manifest.Image
}

// RestoredImage is the PostgreSQL image that filled a recovery's target, as for a run.
func (r RecoveryResult) RestoredImage() string {
	if r.RestoreImage != "" {
		return r.RestoreImage
	}
	return r.Manifest.Image
}

// OpensRestoredPostgres says whether a managed database may serve the data a logical restore in
// the restored image wrote: its release must open data that release wrote, mounted where it
// keeps it.
func OpensRestoredPostgres(a domain.Application, restored string) error {
	written, ok := domain.PostgresReleaseOf(restored)
	if !ok {
		return errors.New("the restored PostgreSQL release is unknown")
	}
	if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
		return errors.New("the restored database is not a managed PostgreSQL service")
	}
	serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
	if !ok || a.Mounts[0].Target != serving.Mount {
		return errors.New("the restored PostgreSQL image and physical layout differ from the frozen target")
	}
	return serving.OpensDataFrom(written)
}

// RestoreRelease is the release a logical PostgreSQL restore of the archive runs for the policy:
// the database's own release, which then serves the data. It refuses an archive that release
// cannot restore, such as one from another major.
func RestoreRelease(p Policy, archive Manifest) (domain.PostgresRelease, error) {
	for _, a := range p.Plan.Specification.Applications {
		if a.ID != p.ResourceID || a.Postgres == nil || a.Source.Image == nil {
			continue
		}
		serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
		source, known := domain.PostgresReleaseOf(archive.Image)
		if !ok || !known {
			return domain.PostgresRelease{}, errors.New("this backup or database uses a PostgreSQL release Astilba does not know")
		}
		return serving, serving.RestoresArchiveFrom(source)
	}
	return domain.PostgresRelease{}, errors.New("the policy has no managed PostgreSQL source")
}
