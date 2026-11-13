package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// A backup from another major is refused with its reason before the host is touched: no
// repository opened, no target volume made. The engine here has no Docker or journal at all.
func TestRestoreFromAnotherMajorIsRefusedBeforeTouchingTheHost(t *testing.T) {
	for _, c := range []struct{ archive, database, reason string }{
		{domain.Postgres17Image, domain.PostgresImage, "taken on PostgreSQL 17.12, and the database now runs PostgreSQL 18.7. A backup restores only into the major version that made it; restore it into a PostgreSQL 17 database instead"},
		{domain.PostgresImage, domain.Postgres17Image, "restore it into a PostgreSQL 18 database instead"},
	} {
		database, volume := uuid.New(), uuid.New()
		image := c.database
		policy := backup.Policy{ID: uuid.New(), EnvironmentID: uuid.New(), ResourceID: database, Method: "postgres-logical", Plan: deployment.Plan{Specification: domain.Specification{Applications: []domain.Application{{ID: database, Source: domain.ApplicationSource{Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}}}}
		snapshot := strings.Repeat("a", 64)
		m := backup.Manifest{Recipe: backup.Recipe, PolicyID: policy.ID, ResourceID: database, Method: "postgres-logical", Image: c.archive, Database: "app", Username: "app", Bytes: 1, SHA256: strings.Repeat("b", 64)}
		s := scheduleState{Installation: backup.Installation{Policy: policy}, Runs: []backup.Run{{Snapshot: snapshot, State: "succeeded", Manifest: &m}}}
		e := &Engine{}
		err := e.restoreSnapshot(context.Background(), &s, &backup.Run{ID: uuid.New(), Snapshot: snapshot, TargetID: uuid.New()})
		var refused restoreRefusal
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("restore of a %s backup into %s: %v", c.archive, c.database, err)
		}
	}
}
