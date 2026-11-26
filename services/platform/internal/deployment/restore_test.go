package deployment

import (
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A restored cluster's role model is the one change a restore makes besides the volume: the
// database on the restored volume moves onto it, and nothing else does.
func TestRestoreSpecificationMovesOnlyTheRestoredDatabaseOntoItsRoles(t *testing.T) {
	old, other, target := uuid.New(), uuid.New(), uuid.New()
	source := domain.Specification{Volumes: []domain.Volume{{ID: old}, {ID: other}}, Applications: []domain.Application{
		{Name: "Database", Postgres: &domain.Postgres{Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: old}}},
		{Name: "Other", Postgres: &domain.Postgres{Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: other}}},
	}}
	for _, roles := range []string{"", domain.PostgresOwner} {
		out, err := RestoreSpecification(source, old, target, roles)
		if err != nil {
			t.Fatal(err)
		}
		if a := out.Applications[0]; a.Mounts[0].VolumeID != target || a.Postgres.Roles != roles {
			t.Errorf("roles %q: restored database = %+v %+v", roles, a.Mounts, *a.Postgres)
		}
		if out.Applications[1].Postgres.Roles != "" || source.Applications[0].Postgres.Roles != "" {
			t.Errorf("roles %q: moved a database it didn't restore, or its source", roles)
		}
	}
	r := RestoreCutover{SourcePlanID: uuid.New(), SourceHash: strings.Repeat("a", 64), PolicyID: uuid.New(), RunID: uuid.New(), ManifestHash: strings.Repeat("b", 64), OldVolumeID: old, TargetVolumeID: target}
	for roles, valid := range map[string]bool{"": true, domain.PostgresOwner: true, "superuser": false} {
		r.Roles = roles
		if (r.Validate() == nil) != valid {
			t.Errorf("a cutover restored as %q: valid = %v", roles, !valid)
		}
	}
}
