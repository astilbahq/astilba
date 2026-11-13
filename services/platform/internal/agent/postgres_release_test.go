package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
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

// A report's note is refused past 1024 characters on every retry, so no note may grow past it,
// however many databases a rebuild leaves unfinished or exclusion indexes it names.
func TestReportNoteStaysWithinTheContract(t *testing.T) {
	var many []string
	for i := range 60 {
		many = append(many, strings.Repeat("ä", 70)+strconv.Itoa(i))
	}
	note := "This host's maintenance reservation is released. 12 instances were not required to be ready: they were already stopped or failing, or an operator accepted them. " + reindexNote(many, many)
	if got := utf8.RuneCountInString(reportNote(note)); got > 1024 || !strings.Contains(reindexNote(many, many), "more") {
		t.Fatalf("a note of %d characters, or no count of the rest", got)
	}
	if reportNote("short") != "short" {
		t.Fatal("a short note changed")
	}
}

// An agent that stops during the rebuild leaves the command to resume: it reports no outcome,
// and the volume keeps recording the earlier release.
func TestStoppingDuringTheRebuildLeavesItToResume(t *testing.T) {
	journal, base := testVisualJournal(t)
	docker, err := client.New(client.WithHost("unix:///nonexistent/astilba-test.sock"))
	if err != nil {
		t.Fatal(err)
	}
	e := &Engine{Docker: docker, Journal: journal}
	var retired domain.PostgresRelease
	for _, r := range domain.PostgresReleases() {
		if r.Major() == 18 && r.Retired() {
			retired = r
		}
	}
	id, volume, image := uuid.New(), uuid.New(), domain.PostgresImage
	p := base
	p.Specification.Applications = []domain.Application{{ID: id, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}
	if err = journal.savePostgresData(volume, postgresData{Image: retired.Image}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if note, err := e.reindexPostgres(ctx, p, nil); !errors.Is(err, context.Canceled) || note != "" {
		t.Fatalf("a stopping agent settled the rebuild: %q, %v", note, err)
	}
	if record, _, _ := journal.postgresData(volume); record.Image != retired.Image {
		t.Fatalf("a stopped rebuild advanced the record to %s", record.Image)
	}
}
