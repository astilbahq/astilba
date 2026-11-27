package agent

import (
	"context"
	"encoding/json/v2"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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
	bolt "go.etcd.io/bbolt"
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
	qualified := map[string][]listed{}
	for shape, names := range shapes {
		for _, name := range names {
			qualified[shape] = append(qualified[shape], listed{name, name})
		}
	}
	before := "The selected local pool is recorded and retirement is confirmed. "
	after := "This host's maintenance reservation is released. 1000000 instances were not required to be ready: they were already stopped or failing, or an operator accepted them."
	for unfinishedShape, unfinished := range shapes {
		for closedShape, closed := range qualified {
			for invalidShape, invalid := range qualified {
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

// PLAT-548. A name too long for its list is cut at a whole character, and a database's name
// before its application's, so the note stays valid UTF-8 and still says whose it is.
func TestLongNamesKeepWholeCharactersAndTheirApplication(t *testing.T) {
	long := "x" + strings.Repeat("ä", 200)
	for _, note := range []string{
		reindexNote(nil, []listed{{long, "Billing"}}, nil, nil),
		reindexNote(nil, nil, []listed{{long, "Billing"}}, nil),
		reindexNote([]string{long}, nil, nil, []string{long + "." + long}),
		reindexNote(nil, []listed{{long, long}}, nil, nil),
	} {
		if !utf8.ValidString(note) {
			t.Fatalf("a cut name split a character: %q", note)
		}
	}
	for _, note := range []string{reindexNote(nil, []listed{{long, "Billing"}}, nil, nil), reindexNote(nil, nil, []listed{{long, "Billing"}}, nil)} {
		if !strings.Contains(note, "… (Billing)") {
			t.Fatalf("a cut database name lost its application: %q", note)
		}
	}
}

// An agent that stops during the rebuild leaves the command to resume: it reports no outcome,
// and the volume keeps recording the earlier release beside the serving one, whatever plan the
// unit saves next.
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
	p.Specification.Applications = []domain.Application{
		{ID: id, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}},
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

// Storage with no record, such as a restore target an earlier agent filled, holds indexes no
// release this agent saw rebuilt. An earlier agent's journal holds the unit's plan on the oldest
// release. Saving a later release's plan records the storage it serves as holding the oldest
// release's indexes, served by the later one, and the storage it no longer uses as the plan it
// leaves served it. Saving the applied plan again, or another on the same release, changes
// neither, so the rebuild stays due and the earlier release stays refused.
func TestStorageWithNoRecordKeepsItsRebuildWhateverPlansTheUnitSaves(t *testing.T) {
	journal, identity := testVisualJournal(t)
	serving, _ := domain.CurrentPostgres(18)
	oldest := oldestPostgres(18)
	if oldest.Image == serving.Image || len(serving.ReindexSince(oldest)) == 0 {
		t.Fatal("PostgreSQL 18 has no earlier release whose update rebuilds indexes")
	}
	served, kept := uuid.New(), uuid.New()
	plan := func(r domain.PostgresRelease, volumes ...uuid.UUID) deployment.Plan {
		p := identity
		p.ID = uuid.New()
		image := r.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{}}
		for _, volume := range volumes {
			p.Specification.Volumes = append(p.Specification.Volumes, domain.Volume{ID: volume, Name: volume.String()})
			p.Specification.Applications = append(p.Specification.Applications, domain.Application{ID: uuid.New(), Name: volume.String(), Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: r.Mount}}})
		}
		return p.WithHash()
	}
	earlier := plan(oldest, served, kept)
	if err := journal.db.Update(func(tx *bolt.Tx) error {
		return journal.put(tx, "units", earlier.UnitID.String(), unitState{Plan: earlier, Expected: map[string]string{}})
	}); err != nil {
		t.Fatal(err)
	}
	applied := plan(serving, served)
	for _, p := range []deployment.Plan{applied, applied, plan(serving, served)} {
		if err := journal.saveUnit(unitState{Plan: p, Expected: map[string]string{}}); err != nil {
			t.Fatal(err)
		}
	}
	for volume, want := range map[uuid.UUID]postgresData{served: {Image: oldest.Image, Served: serving.Image}, kept: {Image: oldest.Image}} {
		if record, found, err := journal.postgresData(volume); err != nil || !found || record != want {
			t.Fatalf("storage with no record was recorded as %+v, not %+v: %v", record, want, err)
		}
	}
	if err := journal.postgresDataOpens(plan(oldest, served), served); !errors.Is(err, errPostgresVersionChange) {
		t.Fatalf("an earlier release opened data the serving one wrote: %v", err)
	}
}

// PLAT-549. PostgreSQL writes a volume's data from its first start, so a run that starts a
// database records its release as serving the data first. A deployment whose run then fails, as
// one whose service starts but never turns healthy does, still refuses a redeploy of the earlier
// release. A run that only creates the container records nothing, one that names its services
// records only theirs, and storage with no record gets the assumed record, served by the release.
func TestFailedDeploymentStillRefusesTheEarlierRelease(t *testing.T) {
	journal, identity := testVisualJournal(t)
	docker, err := client.New(client.WithHost("unix:///nonexistent/astilba-test.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	cli := filepath.Join(t.TempDir(), "docker")
	if err = os.WriteFile(cli, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	e := &Engine{Journal: journal, Docker: docker, DockerCLI: cli}
	serving, _ := domain.CurrentPostgres(18)
	oldest := oldestPostgres(18)
	recorded, unrecorded := uuid.New(), uuid.New()
	ids := map[uuid.UUID]uuid.UUID{recorded: uuid.New(), unrecorded: uuid.New()}
	plan := func(r domain.PostgresRelease) deployment.Plan {
		p := identity
		image := r.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{}}
		for _, volume := range []uuid.UUID{recorded, unrecorded} {
			p.Specification.Volumes = append(p.Specification.Volumes, domain.Volume{ID: volume, Name: volume.String()})
			p.Specification.Applications = append(p.Specification.Applications, domain.Application{ID: ids[volume], Name: volume.String(), Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: r.Mount}}})
		}
		return p.WithHash()
	}
	if err = journal.savePostgresData(recorded, postgresData{Image: oldest.Image}); err != nil {
		t.Fatal(err)
	}
	update := plan(serving)
	record := func(volume uuid.UUID) postgresData {
		t.Helper()
		v, _, err := journal.postgresData(volume)
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if err = e.compose(t.Context(), update, []byte("{}"), nil, "up", "--force-recreate", "--no-start", "--no-deps"); err == nil || record(recorded) != (postgresData{Image: oldest.Image}) {
		t.Fatalf("creating the containers recorded %+v: %v", record(recorded), err)
	}
	if err = e.compose(t.Context(), update, []byte("{}"), nil, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(ids[recorded])); err == nil {
		t.Fatal("the failing run succeeded")
	}
	if got := record(recorded); got != (postgresData{Image: oldest.Image, Served: serving.Image}) {
		t.Fatalf("the database's start recorded %+v", got)
	}
	if _, found, _ := journal.postgresData(unrecorded); found {
		t.Fatal("a run recorded a database it does not start")
	}
	if err = e.compose(t.Context(), update, []byte("{}"), nil, "up", "--detach", "--wait"); err == nil {
		t.Fatal("the failing run succeeded")
	}
	if got := record(unrecorded); got != assumedPostgresData(serving) {
		t.Fatalf("storage with no record was recorded as %+v", got)
	}
	for _, volume := range []uuid.UUID{recorded, unrecorded} {
		if err = journal.postgresDataOpens(plan(oldest), volume); !errors.Is(err, errPostgresVersionChange) {
			t.Fatalf("a redeploy of the earlier release opened data the later one wrote: %v", err)
		}
		if err = journal.postgresDataOpens(update, volume); err != nil {
			t.Fatalf("a retry of the update was refused: %v", err)
		}
	}
}

// PLAT-549. A restore cutover starts its replacement database outside Compose. The record says
// the release serves the data before the start reaches Docker, and a release the record refuses
// never starts.
func TestStartingADatabaseOutsideComposeRecordsItsReleaseFirst(t *testing.T) {
	journal, identity := testVisualJournal(t)
	serving, _ := domain.CurrentPostgres(18)
	oldest := oldestPostgres(18)
	volume, app := uuid.New(), uuid.New()
	plan := func(r domain.PostgresRelease) deployment.Plan {
		p := identity
		image := r.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}},
			Applications: []domain.Application{{ID: app, Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: r.Mount}}}}}
		return p.WithHash()
	}
	var started []postgresData
	transport := scheduleTransport(func(r *http.Request) (*http.Response, error) {
		w := httptest.NewRecorder()
		if !strings.HasSuffix(r.URL.Path, "/containers/replacement/start") {
			w.WriteHeader(404)
			return w.Result(), nil
		}
		record, _, err := journal.postgresData(volume)
		if err != nil {
			t.Error(err)
		}
		started = append(started, record)
		w.WriteHeader(204)
		return w.Result(), nil
	})
	docker, err := client.New(client.WithHost("http://docker-fixture"), client.WithHTTPClient(&http.Client{Transport: transport}), client.WithAPIVersion("1.56"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	e := &Engine{Journal: journal, Docker: docker}
	if err = journal.savePostgresData(volume, postgresData{Image: oldest.Image}); err != nil {
		t.Fatal(err)
	}
	if err = e.startServingPostgres(t.Context(), plan(serving), app, "replacement"); err != nil {
		t.Fatal(err)
	}
	if len(started) != 1 || started[0] != (postgresData{Image: oldest.Image, Served: serving.Image}) {
		t.Fatalf("the database started with the record %+v", started)
	}
	if err = e.startServingPostgres(t.Context(), plan(oldest), app, "replacement"); !errors.Is(err, errPostgresVersionChange) || len(started) != 1 {
		t.Fatalf("an earlier release started on data the later one wrote: %v", err)
	}
}
