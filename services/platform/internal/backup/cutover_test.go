package backup

import (
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
