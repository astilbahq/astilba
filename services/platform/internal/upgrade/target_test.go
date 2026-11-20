package upgrade

import (
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func sourcePlan(image string) Plan {
	database, volume := uuid.New(), uuid.New()
	spec := domain.Specification{Version: 1, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Applications: []domain.Application{{ID: database, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}}
	return Plan{ID: uuid.New(), Recipe: Recipe, ResourceID: database, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), Source: deployment.Plan{ID: uuid.New(), Specification: spec, Images: map[string]string{database.String(): image}}}
}

// A plan made before plans recorded their target restores into the 18.4 it was reviewed with,
// so a later pin does not change a plan under way or the target recovery recomputes.
func TestPlanTargetsTheReleaseItWasMadeWith(t *testing.T) {
	legacy, _ := domain.PostgresReleaseOf(legacyTargetImage)
	current, _ := domain.CurrentPostgres(18)
	if legacy.Name() != "18.4" || current.Version <= legacy.Version {
		t.Fatal("the legacy target is not the retired 18.4 release")
	}
	for _, c := range []struct {
		recorded string
		want     domain.PostgresRelease
	}{{"", legacy}, {current.Image, current}, {legacy.Image, legacy}} {
		p := sourcePlan(domain.Postgres17Image)
		p.TargetImage = c.recorded
		target, err := p.TargetRelease()
		if err != nil || target.Image != c.want.Image {
			t.Fatalf("recorded %q targets %v, %v", c.recorded, target.Image, err)
		}
		app := p.Target().Specification.Applications[0]
		if *app.Source.Image != c.want.Image || app.Mounts[0].Target != c.want.Mount || app.Mounts[0].VolumeID != p.TargetVolumeID {
			t.Fatalf("recorded %q builds a target running %s at %s", c.recorded, *app.Source.Image, app.Mounts[0].Target)
		}
	}
	p := sourcePlan(domain.Postgres17Image)
	p.TargetImage = "postgres:18.7-alpine3.23"
	if _, err := p.TargetRelease(); err == nil {
		t.Fatal("an unpinned target was accepted")
	}
}

// Any known PostgreSQL 17 release is a source, so a database on a retired 17 release can still
// upgrade, and nothing else is one.
func TestPlanSourceIsAnyPostgres17Release(t *testing.T) {
	current, _ := domain.CurrentPostgres(18)
	for _, r := range domain.PostgresReleases() {
		p := sourcePlan(r.Image)
		p.TargetImage = current.Image
		source, err := p.SourceRelease()
		if err != nil || source.Image != r.Image {
			t.Fatal(err)
		}
		target, _ := p.TargetRelease()
		if (source.UpgradesTo(target) == nil) != (r.Major() == 17) {
			t.Errorf("%s: upgrade allowed is %v", r.Name(), source.UpgradesTo(target) == nil)
		}
	}
}
