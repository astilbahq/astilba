package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/upgrade"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// The surrounding native fixture supplies real Docker, ingress and the same
// monotonically increasing command stream used by deployments and backups.
func nativeUpgradeJourney(t *testing.T, e *Engine, newSource func() (deployment.Plan, map[string]string), next func(upgrade.Command) api.ExecutionCommand) {
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
			p, secrets := newSource()
			a := p.Specification.Applications[1]
			policy := backup.Policy{ID: uuid.New(), Name: "Upgrade recovery", WorkspaceID: uuid.New(), EnvironmentID: p.EnvironmentID, ServerID: p.ServerID, DestinationID: uuid.New(), ResourceID: a.ID, Generation: 1, Method: "postgres-logical", Schedule: backup.Schedule{Timezone: "UTC"}, Retain: 1, Plan: p}
			in := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "Recovery fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "upgrade-repository-password-canary"}}
			plan := upgrade.Plan{ID: uuid.New(), Recipe: 1, PolicyID: policy.ID, ResourceID: a.ID, TargetVolumeID: uuid.New(), TargetPlanID: uuid.New(), TargetRevisionID: uuid.New(), RetainDays: 30, ExpiresAt: time.Now().Add(time.Hour), Source: p}
			source, err := e.sourceContainer(ctx, policy, a.ID)
			if err != nil {
				t.Fatal(err)
			}
			if crash == "" {
				t.Run("managed_database_client", func(t *testing.T) {
					// Share the managed application's network identity while holding a real TCP session.
					writer, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
						Config: &container.Config{
							Image:  domain.Postgres17Image,
							Cmd:    []string{"psql", "-h", deployment.ServiceName(a.ID), "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-c", "SELECT pg_sleep(60)"},
							Env:    []string{"PGPASSWORD=" + secrets[a.Postgres.PasswordSecretVersionID.String()]},
							Labels: visualLabels(p, uuid.New()),
						},
						HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + deployment.UnitName(p.Specification.Applications[0].ID))},
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
						raw, err := e.pgQuery(ctx, source.ID, a, "SELECT count(*) FROM pg_stat_activity WHERE client_addr IS NOT NULL AND backend_type='client backend'")
						if err == nil && strings.TrimSpace(string(raw)) == "1" {
							connected = true
							break
						}
						time.Sleep(100 * time.Millisecond)
					}
					if !connected {
						t.Fatal("managed application did not establish a database connection")
					}
					if err = e.knownUpgradeWriters(ctx, source.ID, a, p); err != nil {
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
					if _, err = e.pgQuery(ctx, source.ID, a, condition.setup); err != nil {
						t.Fatal(err)
					}
					if condition.metadata {
						err = e.postgresManifest(ctx, source.ID, a, domain.Postgres17Image, &backup.Manifest{})
					} else {
						err = e.upgradeDatabaseChecks(ctx, source.ID, a)
					}
					if err == nil {
						t.Fatalf("unsupported source accepted: %s", condition.setup)
					}
					if _, err = e.pgQuery(ctx, source.ID, a, condition.cleanup); err != nil {
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
					if _, err = e.pgQuery(ctx, source.ID, admin, object.create); err != nil {
						t.Fatal(err)
					}
					if err = e.upgradeDatabaseChecks(ctx, source.ID, a); err == nil {
						t.Fatal("maintenance object was accepted")
					}
					if _, err = e.pgQuery(ctx, source.ID, admin, object.drop); err != nil {
						t.Fatal(err)
					}
				}
				writerContext, stopWriter := context.WithCancel(ctx)
				writerDone := make(chan struct{})
				go func() { defer close(writerDone); _, _ = e.pgQuery(writerContext, source.ID, a, "SELECT pg_sleep(60)") }()
				for i := 0; i < 50; i++ {
					raw, _ := e.pgQuery(ctx, source.ID, a, "SELECT count(*) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND backend_type='client backend'")
					if strings.TrimSpace(string(raw)) == "1" {
						break
					}
					time.Sleep(100 * time.Millisecond)
				}
				if err = e.knownUpgradeWriters(ctx, source.ID, a, p); err == nil {
					t.Fatal("unknown local database client accepted")
				}
				if _, err = e.pgQuery(ctx, source.ID, a, "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE pid<>pg_backend_pid() AND backend_type='client backend'"); err != nil {
					t.Fatal(err)
				}
				stopWriter()
				<-writerDone
			}
			if _, err = e.pgQuery(ctx, source.ID, a, `CREATE ROLE upgrade_reader NOLOGIN; CREATE EXTENSION hstore; CREATE SCHEMA reports; CREATE TABLE reports.events(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, value hstore NOT NULL UNIQUE); INSERT INTO reports.events(value) VALUES ('a=>b'); ALTER TABLE reports.events OWNER TO upgrade_reader; GRANT USAGE ON SCHEMA reports TO upgrade_reader`); err != nil {
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
				// Scheduled jobs and ordinary host operations cannot resume writers.
				if err = e.TickSchedules(ctx); err != nil {
					t.Fatal(err)
				}
				for _, id := range state.Writers {
					v, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
					if err != nil || v.Container.State.Running {
						t.Fatal("writer resumed before cutover", err)
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
			value, err := e.pgQuery(ctx, current.ID, a, "SELECT value::text FROM reports.events")
			if err != nil || !strings.Contains(string(value), `"a"=>"b"`) {
				t.Fatal("records did not survive", err)
			}
			if _, err = e.pgQuery(ctx, current.ID, a, "INSERT INTO reports.events(value) VALUES ('c=>d')"); err != nil {
				t.Fatal("application write failed", err)
			}
			if _, err = e.volumePath(ctx, policy, plan.OldVolume()); err != nil {
				t.Fatal("old volume was lost", err)
			}
		})
	}
}
