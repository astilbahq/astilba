package agent

import (
	"context"
	"errors"
	"os"
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

// A dump's SQL runs as the restoring superuser. Whatever it leaves in the cluster beyond what a
// dump of one database holds is refused before anything uses the cluster, and what it changes in
// the cluster's settings files and the deployment role's password is put back.
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
	// restore backs up a database whose rows run attack when they are loaded again, and restores
	// that backup onto a new volume.
	restore := func(t *testing.T, c cluster, attack ...string) (uuid.UUID, error) {
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
		// The check constraint is evaluated for each row the restore loads, as the restoring
		// role; the row is inserted before the function attacks.
		if _, err := e.pgQuery(ctx, pgClient{container: db}, a, `CREATE ROLE reader NOLOGIN;
ALTER ROLE reader IN DATABASE `+quoteIdentifier(c.database)+` SET work_mem = '16MB';
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
		return target, e.restorePostgres(ctx, policy, a, uuid.New(), target, dir, m, password)
	}

	for _, c := range []cluster{current, older} {
		t.Run("a dump that changes nothing beyond "+c.database+" on "+domain.PostgresMajor(c.image), func(t *testing.T) {
			if _, err := restore(t, c); err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, c := range []struct {
		name, reason string
		attack       []string
	}{
		{"a superuser", "a role the backup doesn't hold", []string{"CREATE ROLE intruder LOGIN SUPERUSER PASSWORD 'intruder'"}},
		{"a role given attributes", "a role with attributes the backup doesn't give it", []string{"ALTER ROLE reader LOGIN"}},
		{"a role's own settings", "a setting only an administrator may choose", []string{"ALTER ROLE app SET search_path = public"}},
		{"a database setting that loads code", "a setting only an administrator may choose", []string{"ALTER DATABASE app SET session_preload_libraries = 'auto_explain'"}},
		{"an event trigger", "an event trigger", []string{"CREATE FUNCTION public.on_ddl() RETURNS event_trigger LANGUAGE plpgsql AS 'BEGIN END'", "CREATE EVENT TRIGGER on_ddl ON ddl_command_start EXECUTE FUNCTION public.on_ddl()"}},
		{"an extension", "an extension the backup doesn't hold", []string{"CREATE EXTENSION pg_trgm SCHEMA public"}},
		{"a native function", "a native function no extension provides", []string{"CREATE FUNCTION public.native(integer) RETURNS integer LANGUAGE internal STRICT AS 'int4inc'"}},
		{"a file in the cluster", "a file a cluster doesn't hold", []string{"COPY (SELECT 'loaded') TO '" + data + "/intruder.so'"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := restore(t, current, c.attack...)
			if !errors.As(err, new(clusterRefusal)) || clusterReason(err) != c.reason {
				t.Fatalf("the restore was not refused for %s: %v", c.reason, err)
			}
		})
	}

	t.Run("settings files and the deployment's password are put back", func(t *testing.T) {
		target, err := restore(t, current,
			"ALTER ROLE app PASSWORD 'intruder'",
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
		if !connects(password) || connects("intruder") || connects("") {
			t.Fatal("the restored cluster does not hold its clients to the deployment's password")
		}
	})
}
