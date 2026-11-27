package agent

import (
	"context"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// assertOrdinaryUser checks, through run, which runs sql as user with password the way a
// deployment's app reaches its database, that the database user connects only with its password,
// that the cluster's superuser can't connect at all, and that the user can't do what only a
// superuser may, though it may create a trusted extension.
func assertOrdinaryUser(t *testing.T, run func(user, password, sql string) error, user, database, password string) {
	t.Helper()
	if err := run(user, password, "SELECT 1"); err != nil {
		t.Fatal("the database user can't connect with its password:", err)
	}
	if run(user, "", "SELECT 1") == nil || run(user, password+"-wrong", "SELECT 1") == nil {
		t.Fatal("the database user connects without its password")
	}
	for _, p := range []string{"", password, "postgres"} {
		if run("postgres", p, "SELECT 1") == nil {
			t.Fatal("the cluster's superuser accepts a connection from the network")
		}
	}
	for _, sql := range []string{
		"COPY (SELECT 1) TO PROGRAM 'id'",
		"COPY (SELECT 1) TO '/tmp/astilba-denied'",
		"CREATE EXTENSION file_fdw",
		"CREATE EXTENSION dblink",
		"CREATE EXTENSION postgres_fdw",
		"ALTER SYSTEM SET work_mem = '1MB'",
		"SET log_statement = 'all'",
		"SET session_replication_role = replica",
		"ALTER DATABASE " + quoteIdentifier(database) + " SET log_statement = 'all'",
		"CREATE ROLE intruder",
		"ALTER ROLE " + quoteIdentifier(user) + " SUPERUSER",
		"SELECT pg_catalog.pg_read_file('postgresql.conf')",
		"CREATE FUNCTION public.intruder() RETURNS int LANGUAGE c AS 'intruder', 'intruder'",
		"CREATE EVENT TRIGGER intruder ON ddl_command_start EXECUTE FUNCTION pg_catalog.pg_event_trigger_ddl_commands()",
	} {
		if run(user, password, sql) == nil {
			t.Errorf("the database user could run %s", sql)
		}
	}
	if err := run(user, password, "SELECT pg_catalog.count(*) FROM pg_catalog.pg_roles WHERE rolname = current_user AND NOT rolsuper AND NOT rolcreaterole AND NOT rolcreatedb AND NOT rolreplication AND NOT rolbypassrls HAVING pg_catalog.count(*) = 1"); err != nil {
		t.Error("the database user has a superuser's attributes:", err)
	}
	if err := run(user, password, "CREATE EXTENSION pgcrypto; DROP EXTENSION pgcrypto"); err != nil {
		t.Error("the database user can't create a trusted extension:", err)
	}
}

// ordinaryDatabaseUser runs assertOrdinaryUser from inside container db, over its own network
// address, which the cluster holds to password authentication as it does the app's connections.
func ordinaryDatabaseUser(t *testing.T, e *Engine, db, database, password string) {
	t.Helper()
	assertOrdinaryUser(t, func(user, password, sql string) error {
		// A statement that returns no row fails, so a check written as a query can fail too.
		_, err := e.dataOutput(context.Background(), pgClient{container: db, env: []string{"PGPASSWORD=" + password}}, "sh", "-c", `out="$(psql -XAtqw -v ON_ERROR_STOP=1 -h "$(hostname -i)" -U "$1" -d "$2" -c "$3")" || exit 1; case "$3" in SELECT*) [ -n "$out" ] ;; esac`, "sh", user, database, sql)
		return err
	}, "app", database, password)
}

// asDatabaseUserSQL makes sql, which a test runs as a's maintenance user, run as a's database user
// instead, as the deployment's app would, so what it creates is the user's, as in a real database.
func asDatabaseUserSQL(a domain.Application, sql string) string {
	return "SET ROLE " + quoteIdentifier(a.Postgres.Username) + "; " + sql
}

// An agent that stopped while it created a database's cluster left the journal recording that
// creation as unfinished, and its container holding the new volume, which Docker then won't remove.
// The next deployment removes both and creates the cluster again.
func TestNativeDeploymentRecreatesAnUnfinishedCluster(t *testing.T) {
	f := newNativeVisualFixture(t)
	ctx := context.Background()
	p, secrets := f.plan("unfinished", true)
	p = f.resolve(t, p.WithHash())
	a := p.Specification.Applications[1]
	volume := a.Mounts[0].VolumeID
	release, ok := domain.PostgresReleaseOf(*a.Source.Image)
	if !ok {
		t.Fatal("the fixture's database image is not a supported release")
	}
	if err := f.engine.Journal.recordNewPostgresStorage(p, volume, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Driver: "local", Labels: volumeLabels(p, volume)}); err != nil {
		t.Fatal(err)
	}
	if err := f.engine.ensureImage(ctx, release.Image); err != nil {
		t.Fatal(err)
	}
	labels := visualLabels(p, volume)
	labels[labelKind] = postgresInitKind
	if _, err := f.docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: initName(volume), Config: &container.Config{Image: release.Image, Cmd: []string{"true"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: release.Mount}}}}); err != nil {
		t.Fatal(err)
	}
	f.finish(t, f.command("deploy", p, secrets))
	if _, err := f.docker.ContainerInspect(ctx, initName(volume), client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatalf("the unfinished creation's container survived: %v", err)
	}
	if record, found, err := f.engine.Journal.postgresData(volume); err != nil || !found || record.Initialising {
		t.Fatalf("the cluster's creation is not recorded as finished: %+v %v %v", record, found, err)
	}
}
