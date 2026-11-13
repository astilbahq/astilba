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
		p.Images = map[string]string{id.String(): image}
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
		created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: before.ContainerName(id), Config: &container.Config{Image: r.Image, Env: []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=update-fixture-password"}, Labels: visualLabels(before, id)}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: r.Mount}}}})
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
	// Indexes the update must rebuild sit beside ones it must leave alone, in two databases, one
	// of them named like a connection string. An exclusion constraint's index cannot be rebuilt
	// concurrently, and a leftover of an interrupted rebuild is dropped rather than rebuilt.
	fixture := `CREATE DOMAIN code AS bpchar;
CREATE TABLE records(code bpchar, sized char(4), value text, tag code, slot bpchar, EXCLUDE USING btree (slot WITH =));
INSERT INTO records VALUES ('a ', 'b', 'retained', 'c', 'd');
CREATE INDEX unbounded ON records(code);
CREATE INDEX sized ON records(sized);
CREATE INDEX plain ON records(value);
CREATE INDEX tagged ON records(tag);
CREATE INDEX unbounded_ccnew ON records(code);
UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'unbounded_ccnew'::regclass;`
	if _, err := e.pgQuery(ctx, old, a, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pgQuery(ctx, old, a, `CREATE ROLE tenant`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pgQuery(ctx, old, a, `CREATE DATABASE "a=b" OWNER tenant`); err != nil {
		t.Fatal(err)
	}
	other := a
	otherDatabase := *a.Postgres
	otherDatabase.Database = "dbname='a=b'"
	other.Postgres = &otherDatabase
	if _, err := e.pgQuery(ctx, old, other, fixture); err != nil {
		t.Fatal(err)
	}
	// The database's owner, not a superuser, plants closer matches for the functions the rebuild
	// calls. The superuser session must never run them.
	if _, err := e.pgQuery(ctx, old, other, `SET ROLE tenant;
CREATE FUNCTION public.format(text, regclass) RETURNS text LANGUAGE sql AS $$ SELECT 'ALTER ROLE tenant SUPERUSER' $$;
CREATE FUNCTION public.unnest(oidvector) RETURNS SETOF oid LANGUAGE sql AS $$ SELECT oid FROM pg_opclass $$;
RESET ROLE;`); err != nil {
		t.Fatal(err)
	}
	indexes := "('unbounded', 'sized', 'plain', 'tagged', 'records_slot_excl', 'unbounded_ccnew')"
	files := func(container string, application domain.Application) map[string]string {
		t.Helper()
		raw, err := e.pgQuery(ctx, container, application, "SELECT string_agg(relname || '=' || relfilenode, ',' ORDER BY relname) FROM pg_class WHERE relname IN "+indexes)
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
	initial := map[string]map[string]string{"app": files(old, a), "a=b": files(old, other)}
	// The unit applied the retired release; applying the update records what wrote the data.
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
		t.Fatalf("the release that wrote the data was not recorded: %+v, %v", record, err)
	}
	// A transaction that holds the table makes the concurrent rebuild wait; the lock timeout ends
	// the wait, the update stays, the record keeps the earlier release and the note says so.
	holder, err := e.Docker.ExecCreate(ctx, updated, client.ExecCreateOptions{Cmd: []string{"psql", "-XAt", "-U", "app", "-d", "app", "-c", "BEGIN; LOCK TABLE records IN ROW EXCLUSIVE MODE; SELECT pg_sleep(20); COMMIT;"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.Docker.ExecStart(ctx, holder.ID, client.ExecStartOptions{Detach: true}); err != nil {
		t.Fatal(err)
	}
	locked := func() bool {
		raw, _ := e.pgQuery(ctx, updated, a, "SELECT count(*) FROM pg_stat_activity WHERE query LIKE 'BEGIN; LOCK TABLE records%'")
		return strings.TrimSpace(string(raw)) != "0"
	}
	for i := 0; i < 50 && !locked(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	steps := 0
	begin := func() error { steps++; return nil }
	note, err := e.reindexPostgres(ctx, after, begin)
	if err != nil || !strings.Contains(note, "did not finish") || steps != 1 {
		t.Fatalf("a held rebuild was not reported: %q, %v, %d steps", note, err, steps)
	}
	if record, _, _ := e.Journal.postgresData(volume); record.Image != retired.Image {
		t.Fatalf("an unfinished rebuild advanced the record to %s", record.Image)
	}
	for i := 0; i < 300 && locked(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	note, err = e.reindexPostgres(ctx, after, begin)
	if err != nil || !strings.Contains(note, "records_slot_excl") || strings.Contains(note, "did not finish") || steps != 2 {
		t.Fatalf("the retried rebuild: %q, %v, %d steps", note, err, steps)
	}
	for name, application := range map[string]domain.Application{"app": a, "a=b": other} {
		got, was := files(updated, application), initial[name]
		if _, left := got["unbounded_ccnew"]; left {
			t.Errorf("%s: the leftover of an interrupted rebuild was kept", name)
		}
		for _, index := range []string{"unbounded", "tagged", "sized", "plain", "records_slot_excl"} {
			if rebuilt := got[index] != was[index]; rebuilt != (index == "unbounded" || index == "tagged") {
				t.Errorf("%s: index %s rebuilt is %v (%v, was %v)", name, index, rebuilt, got, was)
			}
		}
	}
	raw, err := e.pgQuery(ctx, updated, a, "SELECT current_setting('server_version_num') || '|' || (SELECT value FROM records WHERE code = 'a')")
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
	if raw, err := e.pgQuery(ctx, updated, a, "SELECT rolsuper FROM pg_roles WHERE rolname = 'tenant'"); err != nil || strings.TrimSpace(string(raw)) != "f" {
		t.Fatalf("a database owner's function ran as superuser: %q, %v", raw, err)
	}
}
