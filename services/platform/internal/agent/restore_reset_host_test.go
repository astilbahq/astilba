package agent

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// A restore runs a dump's SQL as the deployment's database user, never as the superuser, so a dump
// whose code tries what only a superuser may do fails to restore, and what the user may do beyond
// the database is refused before anything uses the cluster. Settings the user can't choose aren't
// restored. The archives here come from clusters whose database user was the superuser, so they
// are also restored into the owner model.
func TestNativeRestoreHoldsTheClusterToItsDump(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	j, plan := testVisualJournal(t)
	e := &Engine{Docker: docker, Journal: j}
	const password = "deployment-password-canary"
	type cluster struct{ image, database string }
	current, older := cluster{domain.PostgresImage, "app"}, cluster{domain.Postgres17Image, "postgres"}
	data := "/var/lib/postgresql/18/docker"

	start := func(t *testing.T, c cluster, p deployment.Plan, volume, resource uuid.UUID, network string) string {
		t.Helper()
		created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: c.image, Env: []string{"POSTGRES_USER=app", "POSTGRES_DB=" + c.database, "POSTGRES_PASSWORD=" + password}, Labels: visualLabels(p, resource)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(network), Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: domain.PostgresMount(c.image)}}}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _, _ = docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}) })
		if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		if err = e.waitPostgresStartup(ctx, created.ID, "app", c.database); err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	newVolume := func(t *testing.T, p deployment.Plan) uuid.UUID {
		t.Helper()
		volume := uuid.New()
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Labels: visualLabels(p, volume)}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			_, _ = docker.VolumeRemove(ctx, deployment.VolumeName(volume), client.VolumeRemoveOptions{Force: true})
		})
		return volume
	}
	// backUp backs up a database that setup also prepares, whose rows run attack when they are
	// loaded again.
	backUp := func(t *testing.T, c cluster, setup string, attack ...string) (backup.Policy, string, backup.Manifest, error) {
		t.Helper()
		a := domain.Application{ID: uuid.New(), Source: domain.ApplicationSource{Image: &c.image}, Postgres: &domain.Postgres{Username: "app", Database: c.database}}
		p := plan
		p.Specification.Applications = []domain.Application{a}
		p.Images = map[string]string{a.ID.String(): c.image}
		policy := backup.Policy{Plan: p, ResourceID: a.ID}
		source := newVolume(t, p)
		db := start(t, c, p, source, a.ID, "none")
		body := ""
		for _, statement := range attack {
			body += "EXECUTE $a$" + statement + "$a$;\n"
		}
		// The primary key is built again with too little memory, so the restore leaves the
		// directory a server's temporary files go in. The check constraint is evaluated for each
		// row the restore loads, as the restoring role; the row is inserted before the function
		// attacks.
		if _, err := e.pgQuery(ctx, pgClient{container: db}, a, `CREATE ROLE reader NOLOGIN;
ALTER DATABASE `+quoteIdentifier(c.database)+` SET maintenance_work_mem = '1MB';
`+setup+`
CREATE TABLE public.spilled AS SELECT generate_series(1, 100000) AS value;
ALTER TABLE public.spilled ADD PRIMARY KEY (value);
CREATE FUNCTION public.armed() RETURNS boolean LANGUAGE sql AS 'SELECT true';
CREATE TABLE public.records(value text CHECK (public.armed()));
INSERT INTO public.records VALUES ('restored');
GRANT SELECT ON public.records TO reader;
CREATE OR REPLACE FUNCTION public.armed() RETURNS boolean LANGUAGE plpgsql AS $f$BEGIN
`+body+`RETURN true;
END$f$;`); err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		m := backup.Manifest{}
		dumpCtx, stop := context.WithTimeout(ctx, 5*time.Minute)
		defer stop()
		err := e.dumpPostgres(dumpCtx, backup.Installation{Policy: policy, Access: backup.Access{DatabasePassword: password}}, uuid.New(), dir, &m)
		return policy, dir, m, err
	}
	// restore restores that backup onto a new volume.
	restore := func(t *testing.T, c cluster, setup string, attack ...string) (uuid.UUID, []clusterSetting, error) {
		t.Helper()
		policy, dir, m, err := backUp(t, c, setup, attack...)
		if err != nil {
			t.Fatal(err)
		}
		target := newVolume(t, policy.Plan)
		release, _ := domain.PostgresReleaseOf(c.image)
		restored, err := e.restorePostgres(ctx, policy, uuid.New(), target, dir, m, release, password)
		return target, restored.left, err
	}

	for _, c := range []cluster{current, older} {
		t.Run("a dump that changes nothing beyond "+c.database+" on "+domain.PostgresMajor(c.image), func(t *testing.T) {
			target, left, err := restore(t, c, "")
			if err != nil || len(left) != 0 {
				t.Fatal(left, err)
			}
			db := start(t, c, plan, target, uuid.New(), "bridge")
			ordinaryDatabaseUser(t, e, db, c.database, password)
			got, err := e.dataOutput(ctx, pgClient{container: db}, "psql", "-XAt", "-U", "postgres", "-d", c.database, "-c", "SELECT (SELECT value FROM public.records) || '|' || pg_catalog.pg_get_userbyid(datdba) || '|' || (SELECT count(*) FROM pg_catalog.pg_authid WHERE rolname = 'postgres' AND rolpassword IS NULL) FROM pg_catalog.pg_database WHERE datname = current_database()")
			if err != nil || strings.TrimSpace(string(got)) != "restored|app|1" {
				t.Fatalf("the restored database isn't its user's, or the superuser has a password: %q, %v", got, err)
			}
		})
	}

	// A database's own settings, and its user's in it, are part of its dump. Those its user may
	// choose are restored, even into a database whose sessions are read-only; the rest, and other
	// roles' settings, are left out and named. A source that never loaded the language keeps a
	// name as it was written.
	t.Run("settings the user can't choose aren't restored", func(t *testing.T) {
		target, left, err := restore(t, current, `ALTER ROLE reader IN DATABASE app SET work_mem = '16MB';
ALTER DATABASE app SET timezone = 'UTC';
ALTER ROLE reader IN DATABASE app SET datestyle = 'ISO, DMY';
ALTER ROLE app IN DATABASE app SET statement_timeout = '1min';
ALTER DATABASE app SET default_transaction_read_only = on;
ALTER DATABASE app SET log_min_duration_statement = 0;
ALTER DATABASE app SET session_preload_libraries = 'auto_explain';
ALTER DATABASE app SET session_replication_role = replica;
ALTER DATABASE app SET "PLPGSQL.VARIABLE_CONFLICT" = use_column;
ALTER DATABASE app SET plpgsql.extra_warnings = 'all';
ALTER DATABASE app SET role = app;
ALTER ROLE reader IN DATABASE app SET log_statement = 'all';`)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, s := range left {
			names = append(names, s.role+":"+s.name)
		}
		slices.Sort(names)
		if want := []string{":PLPGSQL.VARIABLE_CONFLICT", ":log_min_duration_statement", ":plpgsql.extra_warnings", ":role", ":session_preload_libraries", ":session_replication_role", "reader:DateStyle", "reader:log_statement", "reader:work_mem"}; !slices.Equal(names, want) {
			t.Fatalf("left out %v, want %v", names, want)
		}
		db := start(t, current, plan, target, uuid.New(), "none")
		got, err := e.dataOutput(ctx, pgClient{container: db}, "psql", "-XAt", "-U", "postgres", "-d", "app", "-c", "SELECT string_agg(coalesce(r.rolname, '-') || ':' || pg_catalog.array_to_string(s.setconfig, ','), ';' ORDER BY r.rolname NULLS FIRST) FROM pg_catalog.pg_db_role_setting s LEFT JOIN pg_catalog.pg_roles r ON r.oid = s.setrole")
		if err != nil || strings.TrimSpace(string(got)) != "-:maintenance_work_mem=1MB,TimeZone=UTC,default_transaction_read_only=on;app:statement_timeout=1min" {
			t.Fatalf("the restored database's settings are %q, %v", got, err)
		}
	})

	// A dump prints values as its session's settings say, which a database's user can choose for
	// its own sessions. The restore's verification dumps under the settings the backup's dump ran
	// with, or, for an archive from before dumps recorded them, as that user's sessions are set
	// in the restored database.
	t.Run("a dump verifies under the settings it was made with", func(t *testing.T) {
		policy, dir, m, err := backUp(t, current, `ALTER ROLE app IN DATABASE app SET timezone = 'Europe/Paris';
ALTER ROLE app IN DATABASE app SET bytea_output = 'escape';
CREATE TABLE public.stamped(at timestamptz, raw bytea);
INSERT INTO public.stamped VALUES ('2026-06-01 12:00:00+00', 'stamped');`)
		if err != nil {
			t.Fatal(err)
		}
		if m.DumpSettings["TimeZone"] != "Europe/Paris" || m.DumpSettings["bytea_output"] != "escape" {
			t.Fatalf("the backup recorded %v", m.DumpSettings)
		}
		release, _ := domain.PostgresReleaseOf(current.image)
		if _, err = e.restorePostgres(ctx, policy, uuid.New(), newVolume(t, policy.Plan), dir, m, release, password); err != nil {
			t.Fatal("a dump made under its user's settings failed to restore: ", err)
		}
		m.DumpSettings = nil
		if _, err = e.restorePostgres(ctx, policy, uuid.New(), newVolume(t, policy.Plan), dir, m, release, password); err != nil {
			t.Fatal("an archive whose settings weren't recorded failed to restore: ", err)
		}
	})

	// What only a superuser may do fails as the database user, so the restore fails.
	for _, c := range []struct{ name, setup, attack string }{
		{"a superuser", "", "CREATE ROLE intruder LOGIN SUPERUSER PASSWORD 'intruder'"},
		{"a role given attributes", "", "ALTER ROLE reader LOGIN"},
		{"a catalog rewritten", "", "UPDATE pg_catalog.pg_authid SET rolcanlogin = true, rolsuper = true WHERE rolname = 'pg_monitor'"},
		{"a predefined role given a membership", "", "GRANT pg_execute_server_program TO app"},
		{"a setting in another database", "", "ALTER DATABASE template1 SET work_mem = '1MB'"},
		{"a setting only an administrator may choose", "", "ALTER DATABASE app SET session_preload_libraries = 'auto_explain'"},
		{"a file in the cluster", "", "COPY (SELECT 'loaded') TO '" + data + "/intruder.so'"},
		{"a program", "", "COPY (SELECT 1) TO PROGRAM 'id'"},
		{"the server's settings", "", "ALTER SYSTEM SET archive_command = 'cat %p'"},
		{"a subscription", "", "CREATE SUBSCRIPTION idle CONNECTION 'host=192.0.2.1' PUBLICATION idle WITH (connect = false)"},
		{"an event trigger", "CREATE FUNCTION public.ddl() RETURNS event_trigger LANGUAGE plpgsql AS $f$BEGIN END$f$;", "CREATE EVENT TRIGGER ddl ON ddl_command_start EXECUTE FUNCTION public.ddl()"},
	} {
		t.Run(c.name, func(t *testing.T) {
			attack := []string{}
			if c.attack != "" {
				attack = append(attack, c.attack)
			}
			if _, _, err := restore(t, current, c.setup, attack...); err == nil {
				t.Fatal("the restore ran what only a superuser may")
			}
		})
	}

	// A database from before the owner model may hold what only a superuser can create or grant.
	// Its backup is refused, since no restore could load it.
	const objects, privileges = "only a superuser can create", "granted by a role that doesn't own the object"
	for _, c := range []struct{ name, setup, reason string }{
		{"a subscription", "CREATE SUBSCRIPTION idle CONNECTION 'host=192.0.2.1' PUBLICATION idle WITH (connect = false);", objects},
		{"an event trigger", "CREATE FUNCTION public.ddl() RETURNS event_trigger LANGUAGE plpgsql AS $f$BEGIN END$f$; CREATE EVENT TRIGGER ddl ON ddl_command_start EXECUTE FUNCTION public.ddl();", objects},
		{"a leakproof function", "CREATE FUNCTION public.open() RETURNS boolean LEAKPROOF LANGUAGE sql AS 'SELECT true';", objects},
		{"a cast between built-in types", "CREATE CAST (varchar AS uuid) WITH INOUT AS IMPLICIT;", objects},
		{"a function setting a module's setting", "CREATE FUNCTION public.tenant() RETURNS integer LANGUAGE sql SET myapp.tenant = 'x' AS 'SELECT 1';", objects},
		{"a privilege on PostgreSQL's own function", "GRANT EXECUTE ON FUNCTION pg_catalog.pg_stat_reset() TO reader;", privileges},
		{"an aggregate whose state is internal", "CREATE AGGREGATE public.gather(anynonarray) (SFUNC = array_agg_transfn, STYPE = internal, FINALFUNC = array_agg_finalfn, FINALFUNC_EXTRA);", objects},
		{"a database privilege another role granted", "GRANT TEMPORARY ON DATABASE app TO reader WITH GRANT OPTION; SET ROLE reader; GRANT TEMPORARY ON DATABASE app TO PUBLIC; RESET ROLE;", privileges},
		{"a privilege another role granted", "CREATE ROLE granting NOLOGIN; CREATE TABLE public.shared(value integer); GRANT SELECT ON public.shared TO granting WITH GRANT OPTION; SET ROLE granting; GRANT SELECT ON public.shared TO reader; RESET ROLE;", privileges},
	} {
		t.Run("a backup of "+c.name, func(t *testing.T) {
			if _, _, _, err := backUp(t, current, c.setup); err == nil || !strings.Contains(err.Error(), c.reason) {
				t.Fatalf("the backup was not refused for what %s: %v", c.reason, err)
			}
		})
	}

	// What the user may do beyond its database's contents, or beyond what the archive holds, is
	// refused once the dump's server has stopped.
	for _, c := range []struct{ name, reason, attack string }{
		{"a changed password", "a role or membership the restore didn't create", "ALTER ROLE app PASSWORD 'intruder'"},
		{"a role's own settings", "a setting outside the database", "ALTER ROLE app SET search_path = public"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := restore(t, current, "", c.attack)
			if !errors.As(err, new(clusterRefusal)) || clusterReason(err) != c.reason {
				t.Fatalf("the restore was not refused for %s: %v", c.reason, err)
			}
		})
	}
	for _, attack := range []string{"ALTER DATABASE app SET work_mem = '1MB'", "GRANT CONNECT ON DATABASE app TO reader", "COMMENT ON DATABASE app IS 'intruder'"} {
		t.Run("the dump's code adding "+attack, func(t *testing.T) {
			_, _, err := restore(t, current, "", attack)
			if err == nil || !strings.Contains(err.Error(), "differ from the archive's") {
				t.Fatalf("the restore was not refused: %v", err)
			}
		})
	}
}
