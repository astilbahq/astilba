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
// however many databases a rebuild leaves unfinished, closed or invalid, or exclusion indexes it names.
// Whole after the longest note a report puts before it, the rebuild's also leads a maintenance
// report's, so clipping never loses it.
func TestReportNoteStaysWithinTheContract(t *testing.T) {
	shapes := map[string][]string{}
	for i := range 300 {
		shapes["accented"] = append(shapes["accented"], strings.Repeat("ä", 70)+strconv.Itoa(i))
		shapes["wide"] = append(shapes["wide"], strings.Repeat("a", 150)+strconv.Itoa(i))
		shapes["short"] = append(shapes["short"], "x"+strconv.Itoa(i))
	}
	before := "The selected local pool is recorded and retirement is confirmed. "
	after := "This host's maintenance reservation is released. 1000000 instances were not required to be ready: they were already stopped or failing, or an operator accepted them."
	for unfinishedShape, unfinished := range shapes {
		for closedShape, closed := range shapes {
			for invalidShape, invalid := range shapes {
				for exclusionShape, exclusions := range shapes {
					rebuild := reindexNote(unfinished, closed, invalid, exclusions)
					if note := before + rebuild; utf8.RuneCountInString(note) > 1024 || reportNote(note) != note || strings.Count(rebuild, " more") != 4 {
						t.Errorf("%s, %s, %s and %s names: a note of %d characters, or no count of the rest", unfinishedShape, closedShape, invalidShape, exclusionShape, utf8.RuneCountInString(note))
					}
					if !strings.HasPrefix(reportNote(rebuild+" "+after), rebuild) {
						t.Errorf("%s, %s, %s and %s names: a maintenance report clipped the rebuild's note", unfinishedShape, closedShape, invalidShape, exclusionShape)
					}
				}
			}
		}
	}
	if reportNote("short") != "short" {
		t.Fatal("a short note changed")
	}
}

// An agent that stops during the rebuild leaves the command to resume: it reports no outcome,
// and each volume keeps recording the earlier release. Storage with no record, whose rebuild had
// not started yet, records the one assumed of it before the first starts, so a later plan the
// unit saves does not record the serving release of the plan it leaves instead.
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
	unrecorded := uuid.New()
	p := base
	p.Specification.Applications = []domain.Application{
		{ID: id, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}},
		{ID: uuid.New(), Name: "Other", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: unrecorded, Target: domain.PostgresMount(image)}}},
	}
	if err = journal.savePostgresData(volume, postgresData{Image: retired.Image}); err != nil {
		t.Fatal(err)
	}
	p = p.WithHash()
	if err = journal.saveUnit(unitState{Plan: p, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if note, err := e.reindexPostgres(ctx, p, nil); !errors.Is(err, context.Canceled) || note != "" {
		t.Fatalf("a stopping agent settled the rebuild: %q, %v", note, err)
	}
	next := p
	next.ID = uuid.New()
	if err = journal.saveUnit(unitState{Plan: next.WithHash(), Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if record, _, _ := journal.postgresData(volume); record.Image != retired.Image || record.Served != image {
		t.Fatalf("a stopped rebuild advanced the record: %+v", record)
	}
	if record, _, _ := journal.postgresData(unrecorded); record.Image != oldestPostgres(18).Image || record.Served != image {
		t.Fatalf("storage with no record lost its outstanding rebuild: %+v", record)
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

// While a rebuild is outstanding, the record holds both the release whose indexes the data holds,
// for storage with no record the oldest of its major, and the later one serving it. A plan the unit
// leaves does not record its own release over that, so the rebuild is not lost, and the data
// still refuses what cannot open what the serving release wrote: an earlier release, or another
// line, whatever the assumed one's line.
func TestOutstandingRebuildKeepsTheServingReleasesRefusals(t *testing.T) {
	journal, identity := testVisualJournal(t)
	serving, _ := domain.CurrentPostgres(18)
	otherLine := serving
	otherLine.Version++
	otherLine.Image, otherLine.Line = "postgres:"+otherLine.Name()+"-alpine3.24@sha256:"+strings.Repeat("a", 64), "alpine3.24"
	postgresReleaseOf = func(image string) (domain.PostgresRelease, bool) {
		if image == otherLine.Image {
			return otherLine, true
		}
		return domain.PostgresReleaseOf(image)
	}
	t.Cleanup(func() { postgresReleaseOf = domain.PostgresReleaseOf })
	volume := uuid.New()
	plan := func(r domain.PostgresRelease) deployment.Plan {
		p := identity
		p.ID = uuid.New()
		image := r.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}},
			Applications: []domain.Application{{ID: uuid.New(), Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: r.Mount}}}}}
		return p.WithHash()
	}
	pending := postgresData{Image: oldestPostgres(18).Image, Served: serving.Image}
	if err := journal.savePostgresData(volume, pending); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := journal.saveUnit(unitState{Plan: plan(serving), Expected: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if record, found, err := journal.postgresData(volume); err != nil || !found || record != pending {
		t.Fatalf("a plan the unit left replaced the outstanding rebuild's record: %+v, %v", record, err)
	}
	if err := journal.postgresDataOpens(plan(serving), volume); err != nil {
		t.Fatalf("the serving release was refused: %v", err)
	}
	for _, refused := range []domain.PostgresRelease{oldestPostgres(18), otherLine} {
		if err := journal.postgresDataOpens(plan(refused), volume); !errors.Is(err, errPostgresVersionChange) {
			t.Fatalf("%s opened data %s served: %v", refused.Image, serving.Image, err)
		}
	}
}

// Saving the applied plan again, as pausing or resuming maintenance and a rerun after a restart
// do, leaves no plan behind, so it records no release for storage with no record: the plan's own
// release has not rebuilt its indexes yet, and the rebuild would never be due. Leaving the plan
// for another still records its release.
func TestSavingTheAppliedPlanAgainRecordsNoRelease(t *testing.T) {
	journal, identity := testVisualJournal(t)
	serving, _ := domain.CurrentPostgres(18)
	volume := uuid.New()
	plan := func() deployment.Plan {
		p := identity
		p.ID = uuid.New()
		image := serving.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}},
			Applications: []domain.Application{{ID: uuid.New(), Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: serving.Mount}}}}}
		return p.WithHash()
	}
	applied := plan()
	for range 2 {
		if err := journal.saveUnit(unitState{Plan: applied, Expected: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
	}
	if record, found, err := journal.postgresData(volume); err != nil || found {
		t.Fatalf("saving the applied plan again recorded its release: %+v, %v", record, err)
	}
	if err := journal.saveUnit(unitState{Plan: plan(), Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if record, found, err := journal.postgresData(volume); err != nil || !found || record != (postgresData{Image: serving.Image}) {
		t.Fatalf("leaving the plan did not record its release: %+v, %v", record, err)
	}
}
