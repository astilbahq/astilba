package backup

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func TestVerifiedHistoricalRestoreRequiresMatchingPhysicalPostgresLayout(t *testing.T) {
	policy, environment, database, volume, target := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	for _, image := range []string{domain.Postgres17Image, domain.PostgresImage} {
		p := Policy{ID: policy, EnvironmentID: environment, ResourceID: database, Method: "postgres-logical", Plan: deployment.Plan{Specification: domain.Specification{Applications: []domain.Application{{ID: database, Source: domain.ApplicationSource{Image: &image}, Postgres: &domain.Postgres{}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}, Volumes: []domain.Volume{{ID: volume}}}}}
		for _, restoredImage := range []string{domain.Postgres17Image, domain.PostgresImage} {
			manifest := Manifest{PolicyID: policy, EnvironmentID: environment, ResourceID: database, Method: "postgres-logical", Image: restoredImage, Extensions: []string{"plpgsql"}}
			r := Run{PolicyID: policy, TargetID: target, State: "succeeded", Verified: true, Manifest: &manifest, ManifestHash: Hash(manifest)}
			// Authenticated run history carries compact metadata after the full
			// archive has been verified on the host.
			manifest.Extensions = []string{}
			old, err := CutoverVolume(p, r)
			if image == restoredImage && (err != nil || old != volume) {
				t.Fatal("compatible restored database rejected", err)
			}
			if image != restoredImage && err == nil {
				t.Fatal("verified historical physical data accepted under a different PostgreSQL layout")
			}
		}
	}
}

// A backup taken on an earlier release restores in the database's own, later release, which
// then serves it; one from another major is refused with the reason. Runs recorded before
// restores named their release were filled by the archive's own image.
func TestRestoreFollowsTheDatabaseReleaseWithinItsMajor(t *testing.T) {
	var retired18, retired17 domain.PostgresRelease
	for _, r := range domain.PostgresReleases() {
		if r.Retired() && r.Major() == 18 {
			retired18 = r
		}
		if r.Retired() && r.Major() == 17 {
			retired17 = r
		}
	}
	if retired18.Image == "" || retired17.Image == "" {
		t.Fatal("no retired release to restore from")
	}
	policy, environment, database, volume, target := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	serving := func(image string) Policy {
		return Policy{ID: policy, EnvironmentID: environment, ResourceID: database, Method: "postgres-logical", Plan: deployment.Plan{Specification: domain.Specification{Applications: []domain.Application{{ID: database, Source: domain.ApplicationSource{Image: &image}, Postgres: &domain.Postgres{}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}, Volumes: []domain.Volume{{ID: volume}}}}}
	}
	run := func(archive, restored string) Run {
		m := Manifest{PolicyID: policy, EnvironmentID: environment, ResourceID: database, Method: "postgres-logical", Image: archive, Roles: []Role{}, Extensions: []string{}}
		return Run{PolicyID: policy, TargetID: target, State: "succeeded", Verified: true, Manifest: &m, ManifestHash: Hash(m), RestoreImage: restored}
	}
	for _, c := range []struct {
		name                       string
		archive, database, written string
		refused                    string
	}{
		{name: "old minor restored by the current one", archive: retired18.Image, database: domain.PostgresImage, written: domain.PostgresImage},
		{name: "legacy run filled by the archive's image, served by a later minor", archive: retired18.Image, database: domain.PostgresImage},
		{name: "legacy run of the same release", archive: retired17.Image, database: retired17.Image},
		{name: "17 backup under 18", archive: domain.Postgres17Image, database: domain.PostgresImage, refused: "taken on PostgreSQL 17"},
		{name: "18 backup under 17", archive: domain.PostgresImage, database: domain.Postgres17Image, refused: "taken on PostgreSQL 18"},
	} {
		p := serving(c.database)
		release, err := RestoreRelease(p, *run(c.archive, "").Manifest)
		if c.refused != "" {
			if err == nil || !strings.Contains(err.Error(), c.refused) {
				t.Errorf("%s: restore allowed or unclear: %v", c.name, err)
			}
			continue
		}
		if err != nil || release.Image != c.database {
			t.Errorf("%s: restores in %s, %v", c.name, release.Image, err)
		}
		old, err := CutoverVolume(p, run(c.archive, c.written))
		if err != nil || old != volume {
			t.Errorf("%s: cutover refused: %v", c.name, err)
		}
	}
	// Data a later release wrote never moves back to an earlier one.
	if _, err := CutoverVolume(serving(retired18.Image), run(retired18.Image, domain.PostgresImage)); err == nil {
		t.Error("data the current release wrote was served by a retired one")
	}
	if _, err := CutoverVolume(serving(domain.PostgresImage), run(retired18.Image, "postgres:18.4-alpine3.23")); err == nil {
		t.Error("a restore image Astilba never pinned was accepted")
	}
}
