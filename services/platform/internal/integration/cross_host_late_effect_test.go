package integration

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// recoveryRuns returns a verified restored run and one whose PostgreSQL version differs.
func recoveryRuns(recovery backup.Policy, environment, resource uuid.UUID) (restored, historical backup.Run) {
	at := time.Now().UTC()
	manifest := backup.Manifest{Recipe: backup.Recipe, PolicyID: recovery.ID, RunID: uuid.New(), EnvironmentID: environment, ResourceID: resource, Method: "postgres-logical", Image: domain.PostgresImage, Database: "app", Username: "app", CreatedAt: at, Roles: []backup.Role{}, Extensions: []string{}, SHA256: strings.Repeat("f", 64), Bytes: 100}
	restored = backup.Run{ID: uuid.New(), PolicyID: recovery.ID, Generation: recovery.Generation, ScheduledAt: at, StartedAt: at, FinishedAt: &at, State: "succeeded", Snapshot: strings.Repeat("e", 64), Verified: true, TargetID: uuid.New(), Manifest: &manifest, ManifestHash: backup.Hash(manifest)}
	older := manifest
	older.Image = domain.Postgres17Image
	historical = restored
	historical.ID, historical.TargetID = uuid.New(), uuid.New()
	historical.Manifest, historical.ManifestHash = &older, backup.Hash(older)
	return restored, historical
}

// A cutover whose command reached its host only after the deadline changed nothing, and
// nothing can issue that command again, so the owner has to be able to approve a new one.
func lateCoordinatedCutover(t *testing.T, f *fixture, b *browser, refresh func(), wait func(func() bool), expire *atomic.Value, actions string, parent, plan uuid.UUID) {
	t.Helper()
	expire.Store("effect")
	defer expire.Store("")
	refresh()
	b.call("POST", actions, api.UpgradeActionRequest{Action: "cutover", ExpectedPhase: "ready", RequestKey: uuid.New()}, 202, nil)
	wait(func() bool {
		var settled bool
		if err := f.st.Pool.QueryRow(context.Background(), `SELECT h.phase='maintenance-owner' AND u.status->>'phase'='ready' AND o.state='running' FROM cross_host_operations h JOIN execution_operations o ON o.id=h.id, supported_upgrades u WHERE h.id=$1 AND u.id=$2`, parent, plan).Scan(&settled); err != nil {
			t.Fatal(err)
		}
		return settled
	})
}

// A restore that misses its deadline did not start, whether its command reached the host too
// late or the parent's own deadline passed first with every child having succeeded. Every
// writer resumes on the source, the reservations go, and the owner can review it again.
func lateCoordinatedRestores(t *testing.T, f *fixture, b *browser, refresh, networkReady func(), wait func(func() bool), expire *atomic.Value, holdPause *atomic.Bool, path string, draft, server uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	apply := func() uuid.UUID {
		var review deployment.Status
		networkReady()
		refresh()
		b.call("POST", path+"/deployment-plans", api.VisualPlanRequest{BaseRevisionId: draft, ServerId: server, AllowHostChanges: true}, 201, &review)
		wait(func() bool {
			b.call("GET", path+"/deployment-plans/"+review.ID.String(), nil, 200, &review)
			return review.Plan != nil
		})
		var operation api.Operation
		refresh()
		b.call("POST", path+"/deployment-plans/"+review.ID.String()+"/apply", api.ApplyPlan{IdempotencyKey: uuid.New()}, 201, &operation)
		return operation.Id
	}
	settled := func(operation uuid.UUID) {
		t.Helper()
		var state, note string
		wait(func() bool {
			if err := f.st.Pool.QueryRow(ctx, `SELECT state,note FROM execution_operations WHERE id=$1`, operation).Scan(&state, &note); err != nil {
				t.Fatal(err)
			}
			return state == "failed" || state == "succeeded"
		})
		if state != "failed" || !strings.Contains(note, "did not complete") {
			t.Fatalf("late restore ended %s: %s", state, note)
		}
		var resumed, targets, reservations int
		if err := f.st.Pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE command->'crosshost'->>'phase'='resume-source' AND state='succeeded'),count(*) FILTER (WHERE command->'crosshost'->>'phase'='resume-target'),(SELECT count(*) FROM cross_host_reservations WHERE parent_id=$1) FROM execution_operations WHERE parent_id=$1`, operation).Scan(&resumed, &targets, &reservations); err != nil {
			t.Fatal(err)
		}
		if resumed != 2 || targets != 0 || reservations != 0 {
			t.Fatalf("late restore left %d hosts resumed, %d target resumptions and %d reservations", resumed, targets, reservations)
		}
	}

	expire.Store("restore")
	settled(apply())
	expire.Store("")

	holdPause.Store(true)
	operation := apply()
	wait(func() bool {
		var pausing bool
		if err := f.st.Pool.QueryRow(ctx, `SELECT phase='maintenance-pause' FROM cross_host_operations WHERE id=$1`, operation).Scan(&pausing); err != nil {
			t.Fatal(err)
		}
		return pausing
	})
	if _, err := f.st.Pool.Exec(ctx, `UPDATE execution_operations SET command=jsonb_set(command,'{deadline}','"2000-01-01T00:00:00Z"') WHERE id=$1`, operation); err != nil {
		t.Fatal(err)
	}
	holdPause.Store(false)
	settled(operation)
}
