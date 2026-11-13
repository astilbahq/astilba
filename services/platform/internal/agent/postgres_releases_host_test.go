package agent

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

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
	// Indexes the update must rebuild sit beside ones it must leave alone, in two databases.
	fixture := `CREATE TABLE records(code bpchar, sized char(4), value text);
INSERT INTO records VALUES ('a ', 'b', 'retained');
CREATE INDEX unbounded ON records(code);
CREATE INDEX sized ON records(sized);
CREATE INDEX plain ON records(value);`
	if _, err := e.pgQuery(ctx, old, a, fixture); err != nil {
		t.Fatal(err)
	}
	if _, err := e.pgQuery(ctx, old, a, "CREATE DATABASE other"); err != nil {
		t.Fatal(err)
	}
	other := a
	otherDatabase := *a.Postgres
	otherDatabase.Database = "other"
	other.Postgres = &otherDatabase
	if _, err := e.pgQuery(ctx, old, other, fixture); err != nil {
		t.Fatal(err)
	}
	files := func(container string, application domain.Application) string {
		t.Helper()
		raw, err := e.pgQuery(ctx, container, application, "SELECT string_agg(relname || '=' || relfilenode, ',' ORDER BY relname) FROM pg_class WHERE relname IN ('unbounded', 'sized', 'plain')")
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(raw))
	}
	initial := map[string]string{"app": files(old, a), "other": files(old, other)}
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
	if err := e.reindexPostgres(ctx, after); err != nil {
		t.Fatal(err)
	}
	for name, application := range map[string]domain.Application{"app": a, "other": other} {
		got := files(updated, application)
		was := map[string]string{}
		for _, entry := range strings.Split(initial[name], ",") {
			index, file, _ := strings.Cut(entry, "=")
			was[index] = file
		}
		for _, entry := range strings.Split(got, ",") {
			index, file, _ := strings.Cut(entry, "=")
			if rebuilt := file != was[index]; rebuilt != (index == "unbounded") {
				t.Errorf("%s: index %s rebuilt is %v (%s, was %s)", name, index, rebuilt, got, initial[name])
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
	if err = e.reindexPostgres(ctx, after); err != nil || files(updated, a) != rebuilt {
		t.Fatalf("a finished update rebuilt indexes again: %v", err)
	}
}
