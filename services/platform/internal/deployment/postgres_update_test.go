package deployment

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A later release of the same major and line replaces a database in place, and the plan says so
// with the reindex it brings. Moving back, or across majors, on the same volume is refused.
func TestPostgresVersionChangeAllowsOnlyAForwardUpdateInPlace(t *testing.T) {
	retired18, retired17 := releaseOf(t, "18.4"), releaseOf(t, "17.10")
	current18, _ := domain.CurrentPostgres(18)
	current17, _ := domain.CurrentPostgres(17)
	database, volume := uuid.New(), uuid.New()
	spec := func(image string, volume uuid.UUID) domain.Specification {
		return domain.Specification{Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Applications: []domain.Application{{ID: database, Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}}
	}
	for _, c := range []struct {
		from, to domain.PostgresRelease
		refused  string
	}{
		{from: retired18, to: retired18},
		{from: retired18, to: current18},
		{from: retired17, to: current17},
		{from: current18, to: retired18, refused: "never moves to an earlier release"},
		{from: current17, to: current18, refused: "needs the supported upgrade"},
		{from: current18, to: current17, refused: "needs the supported upgrade"},
	} {
		err := PostgresVersionChange(spec(c.from.Image, volume), spec(c.to.Image, volume))
		if c.refused == "" && err != nil || c.refused != "" && (err == nil || !strings.Contains(err.Error(), c.refused)) {
			t.Errorf("%s to %s: %v", c.from.Name(), c.to.Name(), err)
		}
		// New storage holds no data to reinterpret.
		if err := PostgresVersionChange(spec(c.from.Image, volume), spec(c.to.Image, uuid.New())); err != nil {
			t.Errorf("%s to %s on new storage: %v", c.from.Name(), c.to.Name(), err)
		}
	}
	effects := strings.Join(Effects(spec(current18.Image, volume), spec(retired18.Image, volume), false), "\n")
	if !strings.Contains(effects, "Update Database from PostgreSQL 18.4 to "+current18.Name()+" in place") || !strings.Contains(effects, "rebuild the indexes") {
		t.Errorf("the plan does not describe the in-place update: %s", effects)
	}
	if effects := strings.Join(Effects(spec(current18.Image, volume), spec(current18.Image, volume), false), "\n"); strings.Contains(effects, "in place") {
		t.Errorf("an unchanged database is described as updated: %s", effects)
	}
	if got := PostgresUpdates(spec(retired18.Image, volume), spec(current18.Image, volume)); got[database].Image != retired18.Image || len(got) != 1 {
		t.Errorf("updates: %v", got)
	}
}

func releaseOf(t *testing.T, name string) domain.PostgresRelease {
	t.Helper()
	for _, r := range domain.PostgresReleases() {
		if r.Name() == name {
			return r
		}
	}
	t.Fatalf("PostgreSQL %s is not a known release", name)
	return domain.PostgresRelease{}
}
