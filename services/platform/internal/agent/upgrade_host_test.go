package agent

import (
	"context"
	"encoding/json/jsontext"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Upgrades share the ingress, journal and command stream with an unrelated unit.
// None of them may disturb that unit or hold back later commands on the host.
func TestNativeSupportedMajorUpgrades(t *testing.T) {
	f := newNativeVisualFixture(t)
	ctx := context.Background()
	first, firstSecrets := f.plan("first", true)
	first.Specification.Applications[0].Replicas = 3
	first.PrepareInstances(deployment.Plan{})
	first = f.resolve(first.WithHash())
	f.finish(f.command("deploy", first, firstSecrets))
	f.request("first.example.test", "v1")
	database := first.Specification.Applications[1]
	startup, cancelStartup := context.WithTimeout(ctx, 2*time.Minute)
	defer cancelStartup()
	if err := f.engine.waitPostgresStartup(startup, deployment.UnitName(database.ID), database.Postgres.Username, database.Postgres.Database); err != nil {
		t.Fatal(err)
	}
	databaseURL := url.URL{Scheme: "postgres", Host: "127.0.0.1:15432", Path: "/app", User: url.UserPassword("app", firstSecrets[database.Postgres.PasswordSecretVersionID.String()]), RawQuery: "sslmode=disable"}
	db, err := pgx.Connect(ctx, databaseURL.String())
	if err != nil {
		t.Fatal("TCP route did not connect to PostgreSQL")
	}
	if _, err = db.Exec(ctx, "CREATE TABLE fixture(value text); INSERT INTO fixture VALUES ('data-survived')"); err != nil {
		t.Fatal(err)
	}
	db.Close(ctx)

	releaseVerified := false
	nativeUpgradeJourney(t, f.engine, func(t *testing.T) (deployment.Plan, map[string]string) {
		p, secrets := f.plan("upgrade-"+uuid.NewString(), true)
		p.Specification.Routes = p.Specification.Routes[:1]
		image := domain.Postgres17Image
		p.Specification.Applications[1].Source.Image = &image
		p.Specification.Applications[1].Mounts[0].Target = domain.PostgresMount(image)
		p = p.WithHash()
		if !releaseVerified {
			p.Specification.Applications[0].Command = []string{"sh", "-c", `mkdir -p /tmp/www/cgi-bin; printf ready > /tmp/www/health; printf '%s' "$VALUE" > /tmp/www/index.html; printf '#!/bin/sh\nsleep 6\nprintf "Content-Type: text/plain\\r\\n\\r\\n"\nprintf "%%s" "$VALUE"\n' > /tmp/www/cgi-bin/slow; chmod +x /tmp/www/cgi-bin/slow; exec httpd -f -p 8080 -h /tmp/www`}
			p = p.WithHash()
		}
		p = f.resolve(p)
		f.finish(f.command("deploy", p, secrets))
		if !releaseVerified {
			p = nativeReleaseJourney(t, f.engine, p, secrets, f.resolve, f.command, f.finish)
			releaseVerified = true
		}
		return p, secrets
	}, func(cmd upgrade.Command) api.ExecutionCommand {
		f.generation++
		raw, _ := stablejson.Marshal(cmd)
		value := jsontext.Value(raw)
		return api.ExecutionCommand{Id: uuid.New(), ServerId: f.server, EnvironmentId: cmd.Plan.Source.EnvironmentID, ResourceId: cmd.Plan.ID, Kind: "upgrade", Action: "inspect", Generation: f.generation, PlanHash: cmd.Digest(), Upgrade: &value, Deadline: time.Now().Add(time.Hour)}
	})

	// The scheduler, daemon and volume deletion also read finished upgrade records.
	nativeFilesAndJobs(t, f.engine, f.server)
	second, secondSecrets := f.plan("second", false)
	second = f.resolve(second)
	f.finish(f.command("deploy", second, secondSecrets))
	f.request("first.example.test", "v1")
	f.request("second.example.test", "v1")
	db, err = pgx.Connect(ctx, databaseURL.String())
	if err != nil {
		t.Fatal("upgrades disturbed an unrelated TCP route")
	}
	defer db.Close(ctx)
	var value string
	if err = db.QueryRow(ctx, "SELECT value FROM fixture").Scan(&value); err != nil || value != "data-survived" {
		t.Fatal("upgrades disturbed an unrelated database", err)
	}
	nativeControllerTransfer(t, f.engine)
	f.refuseInUseVolumeDeletion(first)
}

// The surrounding native fixture supplies real Docker, ingress and the same
// monotonically increasing command stream used by deployments and backups.
func nativeUpgradeJourney(t *testing.T, e *Engine, newSource func(*testing.T) (deployment.Plan, map[string]string), next func(upgrade.Command) api.ExecutionCommand) {
	t.Helper()
	ctx := context.Background()
	e.ResticCLI = "/opt/astilba-platform-tools/restic"
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	e.BackupRoots = append(e.BackupRoots, root)
	for _, crash := range []string{"", "upgrade-admitted", "upgrade-preflight", "upgrade-quiesce-effect", "upgrade-isolate-effect", "upgrade-backup-effect", "upgrade-restore-effect", "upgrade-database-effect", "upgrade-routes-effect", "upgrade-writers-effect"} {
		t.Run(strings.TrimPrefix(crash, "upgrade-"), func(t *testing.T) {
			p, secrets := newSource(t)
			a := p.Specification.Applications[1]
			policy := backup.Policy{ID: uuid.New(), Name: "Upgrade recovery", WorkspaceID: uuid.New(), EnvironmentID: p.EnvironmentID, ServerID: p.ServerID, DestinationID: uuid.New(), ResourceID: a.ID, Generation: 1, Method: "postgres-logical", Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 1, Plan: p}
			in := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Recovery fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "upgrade-repository-password-canary"}}
			plan := upgrade.Plan{ID: uuid.New(), Recipe: 1, PolicyID: policy.ID, ResourceID: a.ID, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: 30, ExpiresAt: time.Now().Add(time.Hour), Source: p}
			source, err := e.sourceContainer(ctx, policy, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if crash == "" {
				state := upgradeState{Command: upgrade.Command{Plan: plan, Installation: in}}
				state.Command.Installation.Access.DatabasePassword = secrets[a.Postgres.PasswordSecretVersionID.String()]
				nativeStoppedPreflight(t, e, state, source.ID, a)
				// The checks run as the preflight runs them: from a helper in the source's namespace.
				checks, stopChecks := context.WithTimeout(ctx, 10*time.Minute)
				defer stopChecks()
				helper, err := e.postgresHelper(checks, "astilba-upgrade-preflight-"+plan.ID.String(), upgradeHelperLabels(state, a, "upgrade-preflight-v1"), source.ID, domain.PostgresImage, state.Command.Installation.Access.DatabasePassword)
				if err != nil {
					t.Fatal(err)
				}
				defer e.removeHelper(helper)
				t.Run("managed_database_client", func(t *testing.T) {
					// Share the managed application's network identity while holding a real TCP session.
					writer, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
						Config: &container.Config{
							Image:  domain.Postgres17Image,
							Cmd:    []string{"psql", "-h", deployment.ServiceName(a.ID), "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-c", "SELECT pg_sleep(60)"},
							Env:    []string{"PGPASSWORD=" + secrets[a.Postgres.PasswordSecretVersionID.String()]},
							Labels: visualLabels(p, uuid.New()),
						},
						HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + p.ContainerName(p.Specification.Applications[0].ID))},
					})
					if err != nil {
						t.Fatal(err)
					}
					defer func() {
						_, _ = e.Docker.ContainerRemove(ctx, writer.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
					}()
					if _, err = e.Docker.ContainerStart(ctx, writer.ID, client.ContainerStartOptions{}); err != nil {
						t.Fatal(err)
					}
					connected := false
					for i := 0; i < 50; i++ {
						raw, err := e.pgQuery(ctx, pgClient{container: source.ID}, a, "SELECT count(*) FROM pg_stat_activity WHERE client_addr IS NOT NULL AND backend_type='client backend'")
						if err == nil && strings.TrimSpace(string(raw)) == "1" {
							connected = true
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
					if !connected {
						t.Fatal("managed application did not establish a database connection")
					}
					if err = e.knownUpgradeWriters(ctx, helper, a, p); err != nil {
						t.Fatalf("managed database client rejected: %v", err)
					}
				})
				for _, condition := range []struct {
					setup, cleanup string
					metadata       bool
				}{
					{"CREATE DATABASE unsupported_database", "DROP DATABASE unsupported_database", false},
					{"ALTER DATABASE app SET search_path=public", "ALTER DATABASE app RESET search_path", false},
					{"REVOKE CONNECT ON DATABASE app FROM PUBLIC", "UPDATE pg_database SET datacl=NULL WHERE datname='app'", false},
					{"COMMENT ON DATABASE postgres IS 'custom maintenance comment'", "COMMENT ON DATABASE postgres IS 'default administrative connection database'", false},
					{"COMMENT ON DATABASE app IS 'custom application comment'", "COMMENT ON DATABASE app IS NULL", false},
					{"CREATE ROLE unsupported_login LOGIN", "DROP ROLE unsupported_login", true},
					{"CREATE EXTENSION pg_trgm", "DROP EXTENSION pg_trgm", true},
					{"UPDATE pg_extension SET extversion='unsupported' WHERE extname='plpgsql'", "UPDATE pg_extension SET extversion='1.0' WHERE extname='plpgsql'", true},
				} {
					if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, a, condition.setup); err != nil {
						t.Fatal(err)
					}
					if condition.metadata {
						err = e.postgresManifest(ctx, helper, a, domain.Postgres17Image, &backup.Manifest{})
					} else {
						err = e.upgradeDatabaseChecks(ctx, helper, a)
					}
					if err == nil {
						t.Fatalf("unsupported source accepted: %s", condition.setup)
					}
					if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, a, condition.cleanup); err != nil {
						t.Fatal(err)
					}
				}

				admin := a
				pg := *a.Postgres
				pg.Database = "postgres"
				admin.Postgres = &pg
				for _, object := range []struct{ create, drop string }{
					{"CREATE FUNCTION public.marker() RETURNS integer LANGUAGE sql AS 'SELECT 1'", "DROP FUNCTION public.marker()"},
					{"CREATE TYPE public.marker_state AS ENUM ('ok')", "DROP TYPE public.marker_state"},
					{`CREATE COLLATION public.application_order FROM "C"`, "DROP COLLATION public.application_order"},
					{"SELECT lo_from_bytea(16500000, 'retained data'::bytea)", "SELECT lo_unlink(16500000)"},
					{"CREATE SCHEMA pgcustom; CREATE TABLE pgcustom.records(id integer)", "DROP SCHEMA pgcustom CASCADE"},
				} {
					if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, admin, object.create); err != nil {
						t.Fatal(err)
					}
					if err = e.upgradeDatabaseChecks(ctx, helper, a); err == nil {
						t.Fatal("maintenance object was accepted")
					}
					if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, admin, object.drop); err != nil {
						t.Fatal(err)
					}
				}
				writerContext, stopWriter := context.WithCancel(ctx)
				writerDone := make(chan struct{})
				go func() {
					defer close(writerDone)
					_, _ = e.pgQuery(writerContext, pgClient{container: source.ID}, a, "SELECT pg_sleep(60)")
				}()
				for i := 0; i < 50; i++ {
					raw, _ := e.pgQuery(ctx, pgClient{container: source.ID}, a, "SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND backend_type='client backend'")
					if strings.TrimSpace(string(raw)) == "1" {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				if err = e.knownUpgradeWriters(ctx, helper, a, p); err == nil {
					t.Fatal("unknown local database client accepted")
				}
				if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, a, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND backend_type='client backend'"); err != nil {
					t.Fatal(err)
				}
				stopWriter()
				<-writerDone
			}
			if _, err = e.pgQuery(ctx, pgClient{container: source.ID}, a, `CREATE ROLE upgrade_reader NOLOGIN; CREATE EXTENSION hstore; CREATE SCHEMA reports; CREATE TABLE reports.events(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, value hstore NOT NULL UNIQUE); INSERT INTO reports.events(value) VALUES ('a=>b'); ALTER TABLE reports.events OWNER TO upgrade_reader; GRANT USAGE ON SCHEMA reports TO upgrade_reader`); err != nil {
				t.Fatal(err)
			}
			send := func(action string, interrupt bool) Record {
				t.Helper()
				cmd := next(upgrade.Command{Action: action, Plan: plan, Installation: in, Secrets: secrets})
				fired := false
				if interrupt {
					e.Point = func(point string) {
						if point == crash {
							fired = true
							panic("simulated lost acknowledgement")
						}
					}
				}
				func() {
					defer func() {
						if r := recover(); r != nil && !fired {
							panic(r)
						}
					}()
					if err = e.Execute(ctx, cmd); err != nil {
						t.Fatal(err)
					}
				}()
				e.Point = nil
				if interrupt && !fired {
					t.Fatalf("fault point %s was not reached", crash)
				}
				if fired {
					// Close/reopen the encrypted journal, as an agent process restart does.
					dir := e.Journal.dir
					if err = e.Journal.Close(); err != nil {
						t.Fatal(err)
					}
					e.Journal, err = Open(dir)
					if err != nil {
						t.Fatal(err)
					}
					if err = e.Execute(ctx, cmd); err != nil {
						t.Fatal(err)
					}
				}
				r, err := e.Journal.Record(cmd.Id)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.Journal.Acknowledge(cmd.Id, r.Report.Sequence); err != nil {
					t.Fatal(err)
				}
				return r
			}
			cutoverCrash := strings.Contains(crash, "database-") || strings.Contains(crash, "routes-") || strings.Contains(crash, "writers-")
			r := send("prepare", crash != "" && !cutoverCrash)
			state, err := e.Journal.upgrade(plan.ID)
			if err != nil {
				t.Fatal(err)
			}
			if crash != "" && !cutoverCrash {
				if r.Report.State != "failed" || state.Status.Phase != "attention" || state.Status.WritesPossible {
					t.Fatalf("unsafe restart result: %+v", state.Status)
				}
				r = send("return", false)
				if r.Report.State != "succeeded" {
					t.Fatal(r.Report.Note)
				}
				state, _ = e.Journal.upgrade(plan.ID)
				if state.Status.Serving != "old" {
					t.Fatal("old database was not restored")
				}
			} else {
				if r.Report.State != "succeeded" || state.Status.Phase != "ready" || state.Status.WritesPossible || !backup.SnapshotID(state.Status.Snapshot) {
					t.Fatalf("prepare: %s %+v", r.Report.Note, state.Status)
				}
				for _, id := range state.Writers {
					v, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
					if err != nil || v.Container.State.Running {
						t.Fatal("writer running before cutover", err)
					}
				}
				r = send("cutover", cutoverCrash)
				if cutoverCrash {
					state, _ = e.Journal.upgrade(plan.ID)
					if r.Report.State != "failed" || !state.Status.WritesPossible {
						t.Fatal("new-write boundary was lost", r.Report.Note)
					}
					if state.Status.Allows("return") {
						t.Fatal("lossy old-volume return was allowed")
					}
					r = send("reconcile", false)
				}
				if r.Report.State != "succeeded" {
					t.Fatal(r.Report.Note)
				}
				state, _ = e.Journal.upgrade(plan.ID)
				if state.Status.Serving != "new" || state.Status.Phase != "complete" {
					t.Fatal("cutover not confirmed")
				}
				policy.Plan = plan.Target()
			}
			current, err := e.sourceContainer(ctx, policy, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			value, err := e.pgQuery(ctx, pgClient{container: current.ID}, a, "SELECT value::text FROM reports.events")
			if err != nil || !strings.Contains(string(value), `"a"=>"b"`) {
				t.Fatal("records did not survive", err)
			}
			if _, err = e.pgQuery(ctx, pgClient{container: current.ID}, a, "INSERT INTO reports.events(value) VALUES ('c=>d')"); err != nil {
				t.Fatal("application write failed", err)
			}
			if _, err = e.volumePath(ctx, policy, plan.OldVolume()); err != nil {
				t.Fatal("old volume was lost", err)
			}
		})
	}
}

// PLAT-390. The upgrade preflight inventories the live source's maintenance database with pg_dump
// from a helper in the source's network namespace. Stopped while that dump waits on a lock, it
// leaves nothing running: its helper is removed and the dump's backend ends, though the lock is
// still held.
func nativeStoppedPreflight(t *testing.T, e *Engine, s upgradeState, source string, a domain.Application) {
	t.Helper()
	ctx := context.Background()
	admin := a
	pg := *a.Postgres
	pg.Database = "postgres"
	admin.Postgres = &pg
	db := pgClient{container: source}
	if _, err := e.pgQuery(ctx, db, admin, "CREATE TABLE public.held(id integer)"); err != nil {
		t.Fatal(err)
	}
	holding, release := context.WithCancel(ctx)
	held := make(chan struct{})
	go func() {
		defer close(held)
		_, _ = e.pgQuery(holding, db, admin, "LOCK TABLE public.held IN ACCESS EXCLUSIVE MODE; SELECT pg_sleep(120)")
	}()
	defer func() {
		_, _ = e.pgQuery(ctx, db, a, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE query LIKE 'LOCK TABLE public.held%'")
		release()
		<-held
		if _, err := e.pgQuery(ctx, db, admin, "DROP TABLE public.held"); err != nil {
			t.Error(err)
		}
	}()
	until := func(what, sql, want string) {
		t.Helper()
		for n := 0; ; n++ {
			out, err := e.pgQuery(ctx, db, a, sql)
			if err == nil && strings.TrimSpace(string(out)) == want {
				return
			}
			if n == 100 {
				t.Fatalf("%s: %q (%v)", what, out, err)
			}
			time.Sleep(100 * time.Millisecond)
		}
	}
	locked := "SELECT count(*) FROM pg_locks l JOIN pg_database d ON d.oid=l.database WHERE d.datname='postgres' AND l.locktype='relation' AND l.mode='AccessExclusiveLock' AND l.granted"
	until("the lock was never held", locked, "1")
	stopped, stop := context.WithTimeout(ctx, 5*time.Minute)
	defer stop()
	checked := make(chan error, 1)
	go func() {
		_, err := e.upgradePreflight(stopped, s, source, a)
		checked <- err
	}()
	until("the preflight's dump never waited on the lock", "SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump' AND wait_event_type='Lock' AND client_addr='127.0.0.1'", "1")
	stop()
	if err := <-checked; err == nil {
		t.Fatal("a stopped preflight passed")
	}
	if _, err := e.Docker.ContainerInspect(ctx, "astilba-upgrade-preflight-"+s.Command.Plan.ID.String(), client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("the stopped preflight's helper remains:", err)
	}
	until("the stopped preflight's dump remains", "SELECT count(*) FROM pg_stat_activity WHERE application_name='pg_dump'", "0")
	until("the lock was released early", locked, "1")
}
