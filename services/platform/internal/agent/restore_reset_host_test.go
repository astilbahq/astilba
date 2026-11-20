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

// A dump's SQL runs as the restoring superuser. What it leaves in the cluster beyond what a dump of
// one database holds is refused before anything uses the cluster, as far as the checks can see,
// what it changes in the cluster's settings files is put back, and settings in the database only
// an administrator may choose are cleared.
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
	// restore backs up a database that setup also prepares, whose rows run attack when they are
	// loaded again, and restores that backup onto a new volume.
	restore := func(t *testing.T, c cluster, setup string, attack ...string) (uuid.UUID, []clusterSetting, error) {
		t.Helper()
		a := domain.Application{ID: uuid.New(), Source: domain.ApplicationSource{Image: &c.image}, Postgres: &domain.Postgres{Username: "app", Database: c.database}}
		p := plan
		p.Specification.Applications = []domain.Application{a}
		p.Images = map[string]string{a.ID.String(): c.image}
		policy := backup.Policy{Plan: p, ResourceID: a.ID}
		source, target := uuid.New(), uuid.New()
		for _, volume := range []uuid.UUID{source, target} {
			if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Labels: visualLabels(p, volume)}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = docker.VolumeRemove(ctx, deployment.VolumeName(volume), client.VolumeRemoveOptions{Force: true})
			})
		}
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
ALTER ROLE reader IN DATABASE `+quoteIdentifier(c.database)+` SET work_mem = '16MB';
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
		if err := e.dumpPostgres(dumpCtx, backup.Installation{Policy: policy, Access: backup.Access{DatabasePassword: password}}, uuid.New(), dir, &m); err != nil {
			t.Fatal(err)
		}
		release, _ := domain.PostgresReleaseOf(*a.Source.Image)
		cleared, err := e.restorePostgres(ctx, policy, a, uuid.New(), target, dir, m, release, password)
		return target, cleared, err
	}

	for _, c := range []cluster{current, older} {
		t.Run("a dump that changes nothing beyond "+c.database+" on "+domain.PostgresMajor(c.image), func(t *testing.T) {
			if _, cleared, err := restore(t, c, ""); err != nil || len(cleared) != 0 {
				t.Fatal(cleared, err)
			}
		})
	}

	// A database's own settings and its roles' settings in it are part of its dump, so none is
	// refused. Those only an administrator may choose are cleared, even from a database whose
	// sessions are read-only; the rest are kept. The
	// server's settings view is made to call every setting one any client may choose, and a
	// source that never loaded the language keeps a name as it was written.
	t.Run("settings only an administrator may choose are cleared", func(t *testing.T) {
		target, cleared, err := restore(t, current, `ALTER DATABASE app SET timezone = 'UTC';
ALTER ROLE reader IN DATABASE app SET datestyle = 'ISO, DMY';
ALTER DATABASE app SET default_transaction_read_only = on;
ALTER DATABASE app SET log_min_duration_statement = 0;
ALTER DATABASE app SET session_preload_libraries = 'auto_explain';
ALTER DATABASE app SET session_replication_role = replica;
ALTER DATABASE app SET "PLPGSQL.VARIABLE_CONFLICT" = use_column;
ALTER ROLE reader IN DATABASE app SET log_statement = 'all';
CREATE VIEW public.settings AS SELECT name, setting, unit, category, short_desc, extra_desc, 'user'::text AS context, vartype, source, min_val, max_val, enumvals, boot_val, reset_val, sourcefile, sourceline, pending_restart FROM pg_catalog.pg_show_all_settings();
CREATE SUBSCRIPTION idle CONNECTION 'host=192.0.2.1' PUBLICATION idle WITH (connect = false);`,
			"UPDATE pg_catalog.pg_rewrite SET ev_action = (SELECT ev_action FROM pg_catalog.pg_rewrite WHERE ev_class = 'public.settings'::pg_catalog.regclass) WHERE ev_class = 'pg_catalog.pg_settings'::pg_catalog.regclass",
		)
		if err != nil {
			t.Fatal(err)
		}
		names := []string{}
		for _, s := range cleared {
			names = append(names, s.role+":"+s.name)
		}
		slices.Sort(names)
		if want := []string{":PLPGSQL.VARIABLE_CONFLICT", ":log_min_duration_statement", ":session_preload_libraries", ":session_replication_role", "reader:log_statement"}; !slices.Equal(names, want) {
			t.Fatalf("cleared %v, want %v", names, want)
		}
		db := start(t, current, plan, target, uuid.New(), "none")
		got, err := e.dataOutput(ctx, pgClient{container: db}, "psql", "-XAt", "-U", "app", "-d", "app", "-c", "SELECT string_agg(coalesce(r.rolname, '-') || ':' || pg_catalog.array_to_string(s.setconfig, ','), ';' ORDER BY r.rolname NULLS FIRST) FROM pg_catalog.pg_db_role_setting s LEFT JOIN pg_catalog.pg_roles r ON r.oid = s.setrole")
		if err != nil || strings.TrimSpace(string(got)) != "-:maintenance_work_mem=1MB,TimeZone=UTC,default_transaction_read_only=on;reader:work_mem=16MB,DateStyle=ISO, DMY" {
			t.Fatalf("the restored database kept settings only an administrator may choose: %q, %v", got, err)
		}
	})

	const role, outside = "a role or membership the restore didn't create", "a setting outside the database"
	for _, c := range []struct {
		name, reason string
		attack       []string
	}{
		{"a superuser", role, []string{"CREATE ROLE intruder LOGIN SUPERUSER PASSWORD 'intruder'"}},
		{"a role given attributes", role, []string{"ALTER ROLE reader LOGIN"}},
		{"a role renamed among the predefined roles", role, []string{"CREATE ROLE intruder LOGIN SUPERUSER PASSWORD 'intruder'", "UPDATE pg_catalog.pg_authid SET rolname = 'pg_backdoor' WHERE rolname = 'intruder'"}},
		{"a predefined role given attributes", role, []string{"UPDATE pg_catalog.pg_authid SET rolcanlogin = true, rolsuper = true WHERE rolname = 'pg_monitor'"}},
		{"a predefined role given a membership", role, []string{"GRANT pg_execute_server_program TO pg_monitor"}},
		{"a changed password", role, []string{"ALTER ROLE app PASSWORD 'intruder'"}},
		{"a role's own settings", outside, []string{"ALTER ROLE app SET search_path = public"}},
		{"a setting in another database", outside, []string{"ALTER DATABASE postgres SET work_mem = '1MB'"}},
		{"a setting the server doesn't know", "a setting the server doesn't know", []string{"UPDATE pg_catalog.pg_db_role_setting SET setconfig = setconfig || '{intruder=on}'::text[] WHERE setdatabase = (SELECT oid FROM pg_catalog.pg_database WHERE datname = 'app') AND setrole = 0"}},
		{"an enabled subscription", "an enabled subscription", []string{"CREATE SUBSCRIPTION intruder CONNECTION 'host=192.0.2.1' PUBLICATION intruder WITH (connect = false)", "ALTER SUBSCRIPTION intruder ENABLE"}},
		{"a file in the cluster", "a file a cluster doesn't hold", []string{"COPY (SELECT 'loaded') TO '" + data + "/intruder.so'"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, _, err := restore(t, current, "", c.attack...)
			if !errors.As(err, new(clusterRefusal)) || clusterReason(err) != c.reason {
				t.Fatalf("the restore was not refused for %s: %v", c.reason, err)
			}
		})
	}

	// A setting the dump's code adds isn't in the archive, so the restore is refused before it is
	// cleared.
	t.Run("a setting the dump's code adds", func(t *testing.T) {
		_, _, err := restore(t, current, "", "ALTER DATABASE app SET session_preload_libraries = 'auto_explain'")
		if err == nil || !strings.Contains(err.Error(), "differs from archived records") {
			t.Fatalf("the restore was not refused: %v", err)
		}
	})

	t.Run("settings files are put back", func(t *testing.T) {
		target, _, err := restore(t, current, "",
			"COPY (SELECT 'archive_mode = on' || chr(10) || 'archive_command = ''cat %p''') TO '"+data+"/postgresql.auto.conf'",
			"COPY (SELECT 'host all all all trust') TO '"+data+"/pg_hba.conf'",
		)
		if err != nil {
			t.Fatal(err)
		}
		db := start(t, current, plan, target, uuid.New(), "bridge")
		got, err := e.dataOutput(ctx, pgClient{container: db}, "psql", "-XAt", "-U", "app", "-d", "app", "-c", "SELECT current_setting('archive_mode') || '|' || (SELECT count(*) FROM pg_file_settings WHERE name LIKE 'archive%') || '|' || (SELECT value FROM public.records)")
		if err != nil || strings.TrimSpace(string(got)) != "off|0|restored" {
			t.Fatalf("the restored cluster kept the dump's settings: %q, %v", got, err)
		}
		// Only a client on the server's own network address is held to the cluster's client
		// authentication rules.
		connects := func(password string) bool {
			_, err := e.dataOutput(ctx, pgClient{container: db, env: []string{"PGPASSWORD=" + password}}, "sh", "-c", `psql -XAtw -h "$(hostname -i)" -U app -d app -c 'SELECT 1'`)
			return err == nil
		}
		if !connects(password) || connects("") {
			t.Fatal("the restored cluster does not hold its clients to the deployment's password")
		}
	})
}
