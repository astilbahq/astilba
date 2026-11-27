package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
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
		_, err := e.restoreSnapshot(context.Background(), &s, &backup.Run{ID: uuid.New(), Snapshot: snapshot, TargetID: uuid.New()})
		var refused restoreRefusal
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), c.reason) {
			t.Fatalf("restore of a %s backup into %s: %v", c.archive, c.database, err)
		}
	}
}

// A report's note is refused past 1024 characters on every retry, so no note may grow past it,
// however many databases a rebuild leaves unfinished or closed or exclusion indexes it names. The
// longest note a maintenance report puts before the rebuild's leaves it room to fit whole.
func TestReportNoteStaysWithinTheContract(t *testing.T) {
	var many, wide []string
	for i := range 60 {
		many = append(many, strings.Repeat("ä", 70)+strconv.Itoa(i))
		wide = append(wide, strings.Repeat("a", 118)+strconv.Itoa(i))
	}
	for _, names := range [][]string{many, wide} {
		rebuild := reindexNote(names, names, names)
		note := "This host's maintenance reservation is released. 12 instances were not required to be ready: they were already stopped or failing, or an operator accepted them. " + rebuild
		if got := utf8.RuneCountInString(note); got > 1024 || reportNote(note) != note || strings.Count(rebuild, " more") != 3 {
			t.Fatalf("a note of %d characters, or no count of the rest: %s", got, note)
		}
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

// A recovery result names the release its restore ran in only to a controller that reads it. Until
// the agent has heard which, a report naming it waits, since an accepted report is not sent again.
func TestRecoveryReportNamesItsReleaseOnlyWhereItIsRead(t *testing.T) {
	raw, err := stablejson.Marshal(backup.RecoveryResult{Snapshot: strings.Repeat("a", 64), TargetID: uuid.New(), RestoreImage: domain.PostgresImage})
	if err != nil {
		t.Fatal(err)
	}
	restore := api.BackupRecoveryResult(raw)
	named := func(v api.AgentReport) string {
		var result backup.RecoveryResult
		if json.Unmarshal(*v.Restore, &result) != nil {
			t.Fatal("the report's result no longer parses")
		}
		return result.RestoreImage
	}
	c := &Client{}
	if _, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); ready {
		t.Fatal("a report naming its release went out before negotiation")
	}
	if _, ready := c.advertisedReport(api.AgentReport{State: "succeeded"}); !ready {
		t.Fatal("a report without a restore waited for negotiation")
	}
	c.markNegotiated()
	if v, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); !ready || named(v) != "" {
		t.Fatal("a controller from before PostgreSQL releases was sent the release")
	}
	c.postgresReleases.Store(true)
	if v, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); !ready || named(v) != domain.PostgresImage {
		t.Fatal("a controller that reads the release was not sent it")
	}
}

// PLAT-450. A recovery result names the role model its restore created only to a controller that
// reads it, and keeps the release for one that reads only that.
func TestRecoveryReportNamesItsRoleModelOnlyWhereItIsRead(t *testing.T) {
	raw, err := stablejson.Marshal(backup.RecoveryResult{Snapshot: strings.Repeat("a", 64), TargetID: uuid.New(), RestoreImage: domain.PostgresImage, RestoreRoles: domain.PostgresOwner})
	if err != nil {
		t.Fatal(err)
	}
	restore := api.BackupRecoveryResult(raw)
	read := func(v api.AgentReport) backup.RecoveryResult {
		var result backup.RecoveryResult
		if json.Unmarshal(*v.Restore, &result) != nil {
			t.Fatal("the report's result no longer parses")
		}
		return result
	}
	c := &Client{}
	c.postgresReleases.Store(true)
	if _, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); ready {
		t.Fatal("a report naming its role model went out before negotiation")
	}
	c.markNegotiated()
	if v, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); !ready || read(v).RestoreRoles != "" || read(v).RestoreImage != domain.PostgresImage {
		t.Fatal("a controller from before the owner model was sent the role model, or lost the release")
	}
	c.postgresOwnerRole.Store(true)
	if v, ready := c.advertisedReport(api.AgentReport{Restore: &restore}); !ready || read(v).RestoreRoles != domain.PostgresOwner {
		t.Fatal("a controller that reads the role model was not sent it")
	}
	if got := c.advertisedCapabilities(api.AgentCapabilities{PostgresOwnerRole: ptr(true)}); got.PostgresOwnerRole == nil {
		t.Fatal("a controller that reads the capability was not sent it")
	}
	c.postgresOwnerRole.Store(false)
	if got := c.advertisedCapabilities(api.AgentCapabilities{PostgresOwnerRole: ptr(true)}); got.PostgresOwnerRole != nil {
		t.Fatal("a controller from before the owner model was sent the capability")
	}
}
