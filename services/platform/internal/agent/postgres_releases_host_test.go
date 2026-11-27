package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// A pin bump leaves databases and backups made under the earlier release. With real PostgreSQL
// images, a backup an earlier minor took restores into the later one, and a database moves to the
// later one in place, which then rebuilds the indexes the releases in between name.
func TestNativePostgresReleases(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	journal, base := testVisualJournal(t)
	e := &Engine{Docker: docker, Journal: journal}
	if _, err := os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		e.DockerCLI = "/opt/astilba-plat4/bin/docker"
	}
	for _, r := range domain.PostgresReleases() {
		if err := e.ensureImage(ctx, r.Image); err != nil {
			t.Fatal(err)
		}
	}
	// A page of two makes every listing of the rebuild span pages, as a database with many
	// indexes does.
	page := reindexPage
	reindexPage = 2
	t.Cleanup(func() { reindexPage = page })
	t.Run("backups_from_earlier_releases", func(t *testing.T) { nativePostgresMetadataRestore(t, e, base) })
	for _, major := range []int{17, 18} {
		t.Run(fmt.Sprintf("postgres%d_in_place_update", major), func(t *testing.T) {
			nativePostgresInPlaceUpdate(t, e, base, major)
		})
	}
}

func nativePostgresInPlaceUpdate(t *testing.T, e *Engine, base deployment.Plan, major int) {
	ctx := context.Background()
	current, _ := domain.CurrentPostgres(major)
	var retired domain.PostgresRelease
	for _, r := range domain.PostgresReleases() {
		if r.Major() == major && r.Retired() {
			retired = r
		}
	}
	if retired.Image == "" || len(current.ReindexSince(retired)) == 0 {
		t.Fatalf("PostgreSQL %d has no retired release whose update rebuilds indexes", major)
	}
	id, volume := uuid.New(), uuid.New()
	plan := func(r domain.PostgresRelease) deployment.Plan {
		p := base
		p.ID = uuid.New()
		image := r.Image
		p.Specification.Volumes = []domain.Volume{{ID: volume, Name: "Data"}}
		p.Specification.Applications = []domain.Application{{ID: id, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: r.Mount}}}}
		// The agent resolves the pin to name@digest before anything runs, as production plans hold it.
		_, digest, _ := strings.Cut(r.Image, "@")
		resolved, err := deployment.DigestReference(r.Image, digest)
		if err != nil {
			t.Fatal(err)
		}
		p.Images = map[string]string{id.String(): resolved}
		return p.WithHash()
	}
	before, after := plan(retired), plan(current)
	a := after.Specification.Applications[0]
	if _, err := e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Labels: visualLabels(before, volume)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = e.Docker.VolumeRemove(ctx, deployment.VolumeName(volume), client.VolumeRemoveOptions{Force: true})
	})
	run := func(r domain.PostgresRelease) string {
		t.Helper()
		created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: before.ContainerName(id), Config: &container.Config{Image: plan(r).Images[id.String()], Env: []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=update-fixture-password"}, Labels: visualLabels(before, id)}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: r.Mount}}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}) })
		if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		if err = e.waitPostgresStartup(ctx, created.ID, "app", "app"); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	old := run(retired)
	// Indexes the update must rebuild sit beside ones it must leave alone, in the deployment's
	// database, in one its owner named like a connection string and marked as a template, and in
	// template1. An exclusion constraint's index cannot be rebuilt concurrently, and a leftover of
	// an interrupted rebuild is dropped rather than rebuilt.
	fixture := `CREATE DOMAIN code AS bpchar;
CREATE DOMAIN short_code AS code;
CREATE DOMAIN sized_code AS char(4);
CREATE TABLE records(code bpchar, sized char(4), value text, tag code, nested short_code, codes short_code[], fixed sized_code, slot bpchar, EXCLUDE USING btree (slot WITH =));
CREATE INDEX nested ON records(nested);
CREATE INDEX listed ON records(codes);
CREATE INDEX fixed ON records(fixed);
INSERT INTO records VALUES ('a ', 'b', 'retained', 'c', 'd');
CREATE INDEX unbounded ON records(code);
CREATE INDEX sized ON records(sized);
CREATE INDEX plain ON records(value);
CREATE INDEX tagged ON records(tag);
CREATE INDEX unbounded_ccnew ON records(code);
UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'unbounded_ccnew'::regclass;`
	if _, err := e.pgQuery(ctx, pgClient{container: old}, a, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pgQuery(ctx, pgClient{container: old}, a, `CREATE ROLE tenant`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pgQuery(ctx, pgClient{container: old}, a, `CREATE DATABASE "a=b" OWNER tenant`); err != nil {
		t.Fatal(err)
	}
	other := a
	otherDatabase := *a.Postgres
	otherDatabase.Database = "dbname='a=b'"
	other.Postgres = &otherDatabase
	if _, err := e.pgQuery(ctx, pgClient{container: old}, other, fixture); err != nil {
		t.Fatal(err)
	}
	// The database's owner, not a superuser, plants closer matches for the functions the rebuild
	// calls. The superuser session must never run them. The owner also marks the database as a
	// template, which leaves its indexes no less due.
	if _, err := e.pgQuery(ctx, pgClient{container: old}, other, `SET ROLE tenant;
CREATE FUNCTION public.format(text, regclass) RETURNS text LANGUAGE sql AS $$ SELECT 'ALTER ROLE tenant SUPERUSER' $$;
CREATE FUNCTION public.unnest(oidvector) RETURNS SETOF oid LANGUAGE sql AS $$ SELECT oid FROM pg_opclass $$;
ALTER DATABASE "a=b" IS_TEMPLATE true;
RESET ROLE;`); err != nil {
		t.Fatal(err)
	}
	if raw, err := e.pgQuery(ctx, pgClient{container: old}, a, `SELECT datistemplate FROM pg_database WHERE datname = 'a=b'`); err != nil || strings.TrimSpace(string(raw)) != "t" {
		t.Fatalf("the owner did not mark the database as a template: %q, %v", raw, err)
	}
	// Before the owner model the database user is a superuser, so template1, which each new
	// database copies, can hold its indexes too.
	template := a
	templateDatabase := *a.Postgres
	templateDatabase.Database = "template1"
	template.Postgres = &templateDatabase
	if _, err := e.pgQuery(ctx, pgClient{container: old}, template, fixture); err != nil {
		t.Fatal(err)
	}
	databases := map[string]domain.Application{"app": a, "a=b": other, "template1": template}
	indexes := "('unbounded', 'sized', 'plain', 'tagged', 'nested', 'listed', 'fixed', 'records_slot_excl', 'unbounded_ccnew')"
	files := func(container string, application domain.Application) map[string]string {
		t.Helper()
		raw, err := e.pgQuery(ctx, pgClient{container: container}, application, "SELECT string_agg(relname || '=' || relfilenode, ',' ORDER BY relname) FROM pg_class WHERE relname IN "+indexes)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]string{}
		for entry := range strings.SplitSeq(strings.TrimSpace(string(raw)), ",") {
			index, file, _ := strings.Cut(entry, "=")
			out[index] = file
		}
		return out
	}
	initial := map[string]map[string]string{}
	for name, application := range databases {
		initial[name] = files(old, application)
	}
	// The unit applied the retired release, the oldest of its major, on storage with no record.
	if err := e.Journal.saveUnit(unitState{Plan: before, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := deployment.PostgresVersionChange(before.Specification, after.Specification); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Docker.ContainerRemove(ctx, old, client.ContainerRemoveOptions{Force: true}); err != nil {
		t.Fatal(err)
	}
	updated := run(current)
	if err := e.Journal.saveUnit(unitState{Plan: after, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	if record, found, err := e.Journal.postgresData(volume); err != nil || !found || record.Image != retired.Image {
		t.Fatalf("the release assumed of the data was not recorded: %+v, %v", record, err)
	}
	// A transaction that holds the table makes the concurrent rebuild wait; the lock timeout ends
	// the wait, the update stays, the record keeps the earlier release and the note says so. It
	// holds past the other database's rebuild and the 10-second lock timeout, but ends within the
	// failed run's own cleanup, which waits for it in turn and so drops the leftover.
	holder, err := e.Docker.ExecCreate(ctx, updated, client.ExecCreateOptions{Cmd: []string{"psql", "-XAt", "-U", "app", "-d", "app", "-c", "BEGIN; LOCK TABLE records IN ROW EXCLUSIVE MODE; SELECT pg_sleep(16); COMMIT;"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Docker.ExecStart(ctx, holder.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	locked := func() bool {
		raw, _ := e.pgQuery(ctx, pgClient{container: updated}, a, "SELECT count(*) FROM pg_stat_activity WHERE query LIKE 'BEGIN; LOCK TABLE records%'")
		return strings.TrimSpace(string(raw)) != "0"
	}
	for i := 0; i < 50 && !locked(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	steps := 0
	begin := func() error { steps++; return nil }
	note, err := e.reindexPostgres(ctx, after, begin)
	if err != nil || !strings.Contains(note, "did not finish for Database") || steps != 1 {
		t.Fatalf("a held rebuild was not reported: %q, %v, %d steps", note, err, steps)
	}
	if record, _, _ := e.Journal.postgresData(volume); record.Image != retired.Image {
		t.Fatalf("an unfinished rebuild advanced the record to %s", record.Image)
	}
	if _, left := files(updated, a)["unbounded_ccnew"]; left {
		t.Error("a failed rebuild kept the leftover it could drop")
	}
	for i := 0; i < 300 && locked(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	// Before the owner model the database user is a superuser, so during the rebuild it can copy
	// template1 into a new database, and move a database's name to another just after the rebuild
	// read it. The copy and the moved database are both still rebuilt, the moved one under its new
	// name, and nothing meant for it runs in the database that took its name.
	query := func(statements ...string) {
		t.Helper()
		for _, statement := range statements {
			if _, err := e.pgQuery(ctx, pgClient{container: updated}, a, statement); err != nil {
				t.Fatal(statement, err)
			}
		}
	}
	copied := a
	copiedDatabase := *a.Postgres
	copiedDatabase.Database = "copied"
	copied.Postgres = &copiedDatabase
	// PLAT-574. CREATE DATABASE lets a role that may create databases choose the oid, so one
	// rebuilt early in the step can be dropped and made again under its oid from template1 before
	// template1's own rebuild. Databases are listed by oid as text, so this one's, chosen from 16384
	// to 19999, comes before template1's, 1.
	recreated := a
	recreatedDatabase := *a.Postgres
	recreatedDatabase.Database = "recreated"
	recreated.Postgres = &recreatedDatabase
	query(`CREATE DATABASE recreated OID 19999 TEMPLATE template1`)
	firstFiles := files(updated, recreated)
	var copiedFiles, movedFiles, recreatedFiles map[string]string
	moved := false
	e.Point = func(point string) {
		switch {
		case point == "postgres-reindex-listed" && copiedFiles == nil:
			movedFiles = files(updated, other)
			query(`CREATE DATABASE copied TEMPLATE template1`, `CREATE DATABASE decoy TEMPLATE template0`)
			copiedFiles = files(updated, copied)
		case point == "postgres-reindex-read a=b" && !moved:
			moved = true
			query(`ALTER DATABASE "a=b" RENAME TO "a=b moved"`, `ALTER DATABASE decoy RENAME TO "a=b"`)
		case point == "postgres-reindex-read template1" && recreatedFiles == nil:
			if files(updated, recreated)["unbounded"] == firstFiles["unbounded"] {
				t.Fatal("the database to make again was not rebuilt before template1")
			}
			query(`DROP DATABASE recreated`, `CREATE DATABASE recreated OID 19999 TEMPLATE template1`)
			recreatedFiles = files(updated, recreated)
		}
	}
	note, err = e.reindexPostgres(ctx, after, begin)
	e.Point = nil
	if err != nil || !strings.Contains(note, `"a=b moved".public.records_slot_excl`) || strings.Contains(note, "did not finish") || steps != 2 || !moved || recreatedFiles == nil {
		t.Fatalf("the retried rebuild: %q, %v, %d steps", note, err, steps)
	}
	if got := files(updated, recreated); got["unbounded"] == recreatedFiles["unbounded"] || got["plain"] != recreatedFiles["plain"] {
		t.Errorf("a database made again under its oid from template1 during the rebuild was not rebuilt: %v, was %v", got, recreatedFiles)
	}
	query(`ALTER DATABASE "a=b" RENAME TO decoy`, `ALTER DATABASE "a=b moved" RENAME TO "a=b"`)
	if got := files(updated, other); got["unbounded"] == movedFiles["unbounded"] || got["plain"] != movedFiles["plain"] {
		t.Errorf("a database renamed during the rebuild was not rebuilt: %v, was %v", got, movedFiles)
	}
	if got := files(updated, copied); got["unbounded"] == copiedFiles["unbounded"] || got["plain"] != copiedFiles["plain"] {
		t.Errorf("a database copied from template1 during the rebuild was not rebuilt: %v, was %v", got, copiedFiles)
	}
	query(`DROP DATABASE copied`, `DROP DATABASE decoy`, `DROP DATABASE recreated`)
	for name, application := range databases {
		got, was := files(updated, application), initial[name]
		if _, left := got["unbounded_ccnew"]; left {
			t.Errorf("%s: the leftover of an interrupted rebuild was kept", name)
		}
		for _, index := range []string{"unbounded", "tagged", "nested", "listed", "sized", "fixed", "plain", "records_slot_excl"} {
			if rebuilt := got[index] != was[index]; rebuilt != (index == "unbounded" || index == "tagged" || index == "nested" || index == "listed") {
				t.Errorf("%s: index %s rebuilt is %v (%v, was %v)", name, index, rebuilt, got, was)
			}
		}
	}
	raw, err := e.pgQuery(ctx, pgClient{container: updated}, a, "SELECT current_setting('server_version_num') || '|' || (SELECT value FROM records WHERE code = 'a')")
	if err != nil || strings.TrimSpace(string(raw)) != fmt.Sprint(current.Version)+"|retained" {
		t.Fatalf("the updated database lost its data: %q, %v", raw, err)
	}
	if record, _, _ := e.Journal.postgresData(volume); record.Image != current.Image {
		t.Fatalf("the rebuilt data still records %s", record.Image)
	}
	// Done once: a later deployment of the same release rebuilds nothing.
	rebuilt := files(updated, a)
	if note, err = e.reindexPostgres(ctx, after, begin); err != nil || note != "" || steps != 2 || fmt.Sprint(files(updated, a)) != fmt.Sprint(rebuilt) {
		t.Fatalf("a finished update rebuilt indexes again: %q, %v", note, err)
	}
	// Storage with no record, such as a restore target an earlier agent filled, is assumed to
	// hold the oldest release's indexes, so its rebuild is never skipped.
	if err = e.Journal.forgetPostgresData(volume); err != nil {
		t.Fatal(err)
	}
	if note, err = e.reindexPostgres(ctx, after, begin); err != nil || steps != 3 || files(updated, a)["unbounded"] == rebuilt["unbounded"] {
		t.Fatalf("storage without a record skipped its rebuild: %q, %v", note, err)
	}
	if record, found, _ := e.Journal.postgresData(volume); !found || record.Image != current.Image {
		t.Fatalf("the rebuilt storage records %+v", record)
	}
	// Nothing can connect to a database its owner closed, the superuser included. The rebuild
	// names it, and the record stays behind until a deployment after it opens again rebuilds it.
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `CREATE DATABASE closed TEMPLATE template0 OWNER tenant`); err != nil {
		t.Fatal(err)
	}
	shut := a
	shutDatabase := *a.Postgres
	shutDatabase.Database = "closed"
	shut.Postgres = &shutDatabase
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, shut, fixture); err != nil {
		t.Fatal(err)
	}
	was := files(updated, shut)
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `SET ROLE tenant; ALTER DATABASE closed ALLOW_CONNECTIONS false; RESET ROLE;`); err != nil {
		t.Fatal(err)
	}
	// PLAT-545. An interrupted DROP DATABASE leaves the database invalid, still marked as
	// accepting connections; it is named, and the other databases are still rebuilt. It is named
	// as PostgreSQL quotes it, so a name with a comma and parentheses still reads as one database.
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `CREATE DATABASE "broken (Billing), ledger" TEMPLATE template0`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `UPDATE pg_database SET datconnlimit = -2 WHERE datname = 'broken (Billing), ledger'`); err != nil {
		t.Fatal(err)
	}
	// PLAT-572. template0 is always oid 4: a closed database a superuser names template0, once the
	// real one has another name, is named like any other, and the real one isn't.
	query(`CREATE DATABASE stale TEMPLATE template0`, `ALTER DATABASE stale ALLOW_CONNECTIONS false`, `ALTER DATABASE template0 RENAME TO "template0 kept"`, `ALTER DATABASE stale RENAME TO template0`)
	// PLAT-585. A database's name is in the encoding of the one it was made from, so from a LATIN1
	// database "café" is no UTF-8. It is still rebuilt, and named with the byte escaped.
	query(`CREATE DATABASE latin TEMPLATE template0 ENCODING 'LATIN1' LC_COLLATE 'C' LC_CTYPE 'C'`)
	latin := a
	latinDatabase := *a.Postgres
	latinDatabase.Database = "latin"
	latin.Postgres = &latinDatabase
	inLatin := pgClient{container: updated, env: []string{"PGCLIENTENCODING=UTF8"}}
	if _, err = e.pgQuery(ctx, inLatin, latin, `CREATE DATABASE "café" TEMPLATE template1`); err != nil {
		t.Fatal(err)
	}
	cafe := a
	cafeDatabase := *a.Postgres
	cafeDatabase.Database = databaseURI("caf\xe9")
	cafe.Postgres = &cafeDatabase
	cafeFiles := files(updated, cafe)
	served := files(updated, a)
	if err = e.Journal.forgetPostgresData(volume); err != nil {
		t.Fatal(err)
	}
	if note, err = e.reindexPostgres(ctx, after, begin); err != nil || !strings.Contains(note, "do not accept connections") || !strings.Contains(note, "closed (") || !strings.Contains(note, "template0 (") || strings.Contains(note, "kept") || !strings.Contains(note, `"broken (Billing), ledger" (`+a.Name+") are invalid") || !strings.Contains(note, `"caf\xe9".public.records_slot_excl`) || strings.Contains(note, "did not finish") {
		t.Fatalf("a closed or invalid database was not named: %q, %v", note, err)
	}
	if files(updated, a)["unbounded"] == served["unbounded"] {
		t.Fatal("an invalid database kept the others from being rebuilt")
	}
	if got := files(updated, cafe); got["unbounded"] == cafeFiles["unbounded"] || got["plain"] != cafeFiles["plain"] {
		t.Errorf("a database whose name isn't UTF-8 was not rebuilt: %v, was %v", got, cafeFiles)
	}
	query(`ALTER DATABASE template0 RENAME TO stale`, `ALTER DATABASE "template0 kept" RENAME TO template0`, `DROP DATABASE stale`)
	if _, err = e.pgQuery(ctx, inLatin, latin, `DROP DATABASE "café"`); err != nil {
		t.Fatal(err)
	}
	query(`DROP DATABASE latin`)
	// Storage with no record keeps the release the rebuild assumed beside the one serving it.
	if record, found, _ := e.Journal.postgresData(volume); !found || record.Image != oldestPostgres(major).Image || record.Served != current.Image {
		t.Fatalf("a rebuild that left a closed and an invalid database recorded %+v", record)
	}
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `ALTER DATABASE closed ALLOW_CONNECTIONS true`); err != nil {
		t.Fatal(err)
	}
	if _, err = e.pgQuery(ctx, pgClient{container: updated}, a, `DROP DATABASE "broken (Billing), ledger"`); err != nil {
		t.Fatal(err)
	}
	if note, err = e.reindexPostgres(ctx, after, begin); err != nil || strings.Contains(note, "do not accept connections") || strings.Contains(note, "invalid") || files(updated, shut)["unbounded"] == was["unbounded"] {
		t.Fatalf("the reopened database was not rebuilt: %q, %v", note, err)
	}
	if record, found, _ := e.Journal.postgresData(volume); !found || record.Image != current.Image {
		t.Fatalf("the rebuilt storage records %+v", record)
	}
	if raw, err := e.pgQuery(ctx, pgClient{container: updated}, a, "SELECT rolsuper FROM pg_roles WHERE rolname = 'tenant'"); err != nil || strings.TrimSpace(string(raw)) != "f" {
		t.Fatalf("a database owner's function ran as superuser: %q, %v", raw, err)
	}
}
