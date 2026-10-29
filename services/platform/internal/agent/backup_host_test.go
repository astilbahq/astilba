package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

func nativePostgresMetadataRestore(t *testing.T, e *Engine, base deployment.Plan) {
	t.Helper()
	for _, fixture := range []struct{ name, image, database string }{
		{"postgres18", domain.PostgresImage, "app"},
		{"postgres17_maintenance_database", domain.Postgres17Image, "postgres"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx := context.Background()
			p := base
			a := domain.Application{ID: uuid.New(), Source: domain.ApplicationSource{Image: &fixture.image}, Postgres: &domain.Postgres{Username: "app", Database: fixture.database}}
			p.Specification.Applications = []domain.Application{a}
			p.Images = map[string]string{a.ID.String(): fixture.image}
			policy := backup.Policy{Plan: p, ResourceID: a.ID}
			start := func(volume uuid.UUID) string {
				t.Helper()
				created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: fixture.image, Env: []string{"POSTGRES_USER=app", "POSTGRES_DB=" + fixture.database, "POSTGRES_PASSWORD=metadata-fixture-password"}, Labels: visualLabels(p, a.ID)}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: domain.PostgresMount(fixture.image)}}}})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _, _ = e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true}) })
				if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
					t.Fatal(err)
				}
				if err = e.waitPostgresStartup(ctx, created.ID, a.Postgres.Username, a.Postgres.Database); err != nil {
					t.Fatal(err)
				}
				return created.ID
			}
			volumes := []uuid.UUID{uuid.New(), uuid.New()}
			for _, volume := range volumes {
				if _, err := e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Labels: visualLabels(p, volume)}); err != nil {
					t.Fatal(err)
				}
			}
			source := start(volumes[0])
			db := quoteIdentifier(fixture.database)
			if _, err := e.pgQuery(ctx, source, a, `CREATE ROLE backup_reader NOLOGIN;
CREATE SCHEMA reporting;
CREATE TABLE reporting.records(value text);
INSERT INTO reporting.records VALUES ('retained');
ALTER DATABASE `+db+` SET search_path=reporting,public;
ALTER ROLE backup_reader IN DATABASE `+db+` SET work_mem='16MB';
REVOKE CONNECT ON DATABASE `+db+` FROM PUBLIC;
GRANT CONNECT ON DATABASE `+db+` TO backup_reader;
COMMENT ON DATABASE `+db+` IS 'metadata recovery fixture';
ALTER DATABASE `+db+` CONNECTION LIMIT 7;
ALTER DATABASE `+db+` OWNER TO backup_reader;`); err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			m := backup.Manifest{}
			if err := e.dumpPostgres(ctx, policy, dir, &m); err != nil {
				t.Fatal(err)
			}
			// These are the existing custom-format archives: no new manifest or
			// dump option is required to recover their database metadata.
			if err := e.restorePostgres(ctx, policy, &backup.Run{ID: uuid.New(), TargetID: volumes[1]}, dir, m, "metadata-fixture-password"); err != nil {
				t.Fatal(err)
			}
			restored := start(volumes[1])
			got, err := e.pgQuery(ctx, restored, a, `SELECT
current_setting('search_path'),
(SELECT value FROM reporting.records),
has_database_privilege('backup_reader', current_database(), 'CONNECT'),
(SELECT setconfig::text FROM pg_db_role_setting WHERE setdatabase=(SELECT oid FROM pg_database WHERE datname=current_database()) AND setrole=(SELECT oid FROM pg_roles WHERE rolname='backup_reader')),
(SELECT concat_ws(',',datconnlimit,pg_get_userbyid(datdba),shobj_description(oid,'pg_database')) FROM pg_database WHERE datname=current_database()),
NOT EXISTS (SELECT 1 FROM pg_database, LATERAL aclexplode(coalesce(datacl,acldefault('d',datdba))) WHERE datname=current_database() AND grantee=0 AND privilege_type='CONNECT')`)
			if err != nil || strings.TrimSpace(string(got)) != "reporting, public|retained|t|{work_mem=16MB}|7,backup_reader,metadata recovery fixture|t" {
				t.Fatalf("database settings, privileges, ownership, comments or records were lost: %q, %v", got, err)
			}
		})
	}
}

func nativeBackupJourney(t *testing.T, e *Engine, p deployment.Plan, finish func(backup.Command) Record) {
	t.Helper()
	ctx := context.Background()
	e.ResticCLI = "/opt/astilba-platform-tools/restic"
	nativePostgresStartup(t, e, p)
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	e.BackupRoots = []string{root}
	a := p.Specification.Applications[1]
	policy := backup.Policy{ID: uuid.New(), Name: "Daily database", WorkspaceID: uuid.New(), EnvironmentID: p.EnvironmentID, ServerID: p.ServerID, DestinationID: uuid.New(), ResourceID: a.ID, Generation: 1, Method: "postgres-logical", Schedule: backup.Schedule{Timezone: "Europe/London", Hour: 2, Minute: 30}, Retain: 1, Enabled: true, Plan: p}
	c, err := e.sourceContainer(ctx, policy, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = e.pgQuery(ctx, c.ID, a, `CREATE ROLE backup_reader NOLOGIN; CREATE EXTENSION hstore; CREATE SCHEMA reporting; CREATE TABLE reporting.records(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, payload hstore NOT NULL); INSERT INTO reporting.records(payload) VALUES ('a=>b'); ALTER TABLE reporting.records OWNER TO backup_reader; GRANT USAGE ON SCHEMA reporting TO backup_reader; CREATE TABLE large_records AS SELECT repeat('x',70*1024*1024) AS payload`); err != nil {
		t.Fatal(err)
	}
	in := backup.Installation{Policy: policy, Destination: backup.Destination{ID: policy.DestinationID, Name: "External recovery fixture", Kind: "filesystem", Root: root}, Access: backup.Access{Password: "native-repository-password-canary"}}
	finish(backup.Command{Action: "install", Installation: in, RunID: uuid.New(), TargetID: uuid.New()})
	// Stop this policy before its temporary repository is removed; later
	// journeys exercise the same journal's scheduler.
	t.Cleanup(func() {
		current, err := e.Journal.schedule(policy.ID)
		if err != nil {
			t.Error(err)
			return
		}
		current.Installation.Policy.Enabled = false
		if err := e.Journal.saveSchedule(current); err != nil {
			t.Error(err)
		}
	})
	s, err := e.Journal.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	// A due schedule executes with no management client at all.
	due := policy.Schedule.Next(time.Now().UTC().Add(-48 * time.Hour))
	s.Installation.Next = due
	if err = e.Journal.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = e.TickSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = e.Journal.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Runs) != 1 || s.Runs[0].State != "succeeded" || !backup.SnapshotID(s.Runs[0].Snapshot) || s.Runs[0].Verified {
		t.Fatalf("offline backup not recorded: %+v", s.Runs)
	}
	snapshot := s.Runs[0].Snapshot
	// Restore into a separate owned volume and compare archived schema/data.
	if _, err = e.Docker.ContainerStop(ctx, c.ID, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	target := uuid.New()
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), snapshot, target); err != nil {
		t.Fatalf("fresh restore: %v", err)
	}
	restored := s.Runs[len(s.Runs)-1]
	if !restored.Verified || restored.State != "succeeded" || restored.TargetID != target {
		t.Fatalf("restore not verified: %+v", restored)
	}
	if _, err = e.volumePath(ctx, policy, target); err != nil {
		t.Fatal(err)
	}
	if _, err = e.Docker.ContainerStart(ctx, c.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < 60; n++ {
		if _, err = e.dataOutput(ctx, c.ID, "pg_isready", "-U", a.Postgres.Username, "-d", a.Postgres.Database); err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	value, err := e.pgQuery(ctx, c.ID, a, "SELECT payload::text FROM reporting.records")
	if err != nil || !strings.Contains(string(value), `"a"=>"b"`) {
		t.Fatal("source records changed during restore")
	}
	// A large declared footprint must fail before creating target storage.
	capacity := s
	capacity.Runs = append([]backup.Run{}, s.Runs...)
	for i := range capacity.Runs {
		if capacity.Runs[i].Manifest != nil {
			m := *capacity.Runs[i].Manifest
			m.Bytes = 1 << 39
			capacity.Runs[i].Manifest = &m
		}
	}
	blocked := uuid.New()
	if err = e.runScheduled(ctx, &capacity, time.Now().UTC(), uuid.New(), snapshot, blocked); err == nil {
		t.Fatal("insufficient capacity accepted")
	}
	if _, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(blocked), client.VolumeInspectOptions{}); err == nil {
		t.Fatal("capacity failure created a target")
	}
	// Invalid keys and unavailable destinations cannot publish usable records.
	s, err = e.Journal.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	originalPassword := s.Installation.Access.Password
	s.Installation.Access.Password = "incorrect-repository-password"
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err == nil {
		t.Fatal("invalid repository key accepted")
	}
	failed := s.Runs[len(s.Runs)-1]
	if failed.Snapshot != "" || failed.State != "failed" {
		t.Fatal("failed backup became usable")
	}
	s.Installation.Access.Password = originalPassword
	owned := filepath.Join(root, policy.WorkspaceID.String(), policy.ID.String())
	if err = os.Rename(owned, owned+"-offline"); err != nil {
		t.Fatal(err)
	}
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err == nil {
		t.Fatal("unavailable destination accepted")
	}
	if err = os.Rename(owned+"-offline", owned); err != nil {
		t.Fatal(err)
	}
	full := t.TempDir()
	if output, mountErr := exec.Command("mount", "-t", "tmpfs", "-o", "size=64k", "tmpfs", full).CombinedOutput(); mountErr != nil {
		t.Fatalf("mount disposable full-disk fixture: %v %s", mountErr, output)
	}
	if fillErr := os.WriteFile(filepath.Join(full, "fill"), make([]byte, 128<<10), 0600); !errors.Is(fillErr, syscall.ENOSPC) {
		_ = exec.Command("umount", full).Run()
		t.Fatalf("fixture did not fill: %v", fillErr)
	}
	originalDir := e.Journal.dir
	e.Journal.dir = full
	diskErr := e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil)
	e.Journal.dir = originalDir
	if output, unmountErr := exec.Command("umount", full).CombinedOutput(); unmountErr != nil {
		t.Fatalf("unmount owned full-disk fixture: %v %s", unmountErr, output)
	}
	if diskErr == nil || s.Runs[len(s.Runs)-1].Snapshot != "" {
		t.Fatal("full disk produced a usable backup")
	}
	// A successful later upload and retention keep the demonstrated recovery point.
	if err = e.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, r := range s.Runs {
		if r.Snapshot == snapshot && r.Verified && !r.Pruned {
			found = true
		}
	}
	if !found {
		t.Fatal("retention removed the only verified snapshot")
	}
	nativeS3Backup(t, e, policy, s.Installation.Access.DatabasePassword)
	// Expire after restic writes a snapshot but before its result reaches the
	// agent. The repository effect must not become a usable catalogue entry.
	originalRestic := e.ResticCLI
	receipt := filepath.Join(root, "upload-receipt")
	interrupted := filepath.Join(root, "interrupted-restic")
	script := fmt.Sprintf("#!/bin/sh\nif [ \"${3-}\" = backup ]; then\n  %s \"$@\" > %s || exit $?\n  exec sleep 60\nfi\nexec %s \"$@\"\n", originalRestic, receipt, originalRestic)
	if err = os.WriteFile(interrupted, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	e.ResticCLI = interrupted
	expiring, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = e.runScheduled(expiring, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil)
	cancel()
	e.ResticCLI = originalRestic
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("upload did not expire: %v", err)
	}
	rawReceipt, receiptErr := os.ReadFile(receipt)
	if receiptErr != nil || !strings.Contains(string(rawReceipt), "snapshot_id") {
		t.Fatal("fixture did not interrupt an acknowledged repository write")
	}
	if err = e.TickSchedules(ctx); err != nil {
		t.Fatal(err)
	}
	s, err = e.Journal.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	uncertain := s.Runs[len(s.Runs)-1]
	if uncertain.State != "needs_attention" || uncertain.Snapshot != "" {
		t.Fatal("expired upload became usable or retryable without inspection")
	}
	if err = e.ResolveScheduledRun(ctx, policy.ID, uncertain.ID); err != nil {
		t.Fatal(err)
	}
	reports, err := e.Journal.ScheduleReports()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reports)
	if strings.Contains(string(raw), "canary") || strings.Contains(string(raw), root) {
		t.Fatal("schedule report leaked repository credentials or local paths")
	}
}

func nativePostgresStartup(t *testing.T, e *Engine, p deployment.Plan) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	file := filepath.Join(t.TempDir(), "startup.sql")
	if err := os.WriteFile(file, []byte("SELECT pg_sleep(3); CREATE TABLE initialization_finished AS SELECT 1 AS value;"), 0644); err != nil {
		t.Fatal(err)
	}
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: domain.PostgresImage, Env: []string{"POSTGRES_USER=fixture", "POSTGRES_DB=fixture", "POSTGRES_PASSWORD=startup-fixture-canary"}, Labels: visualLabels(p, uuid.New())}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeBind, Source: file, Target: "/docker-entrypoint-initdb.d/startup.sql", ReadOnly: true}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer e.Docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	if err = e.waitPostgresStartup(ctx, created.ID, "fixture", "fixture"); err != nil {
		t.Fatal(err)
	}
	value, err := e.dataOutput(ctx, created.ID, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-c", "SELECT value FROM initialization_finished")
	if err != nil || strings.TrimSpace(string(value)) != "1" {
		t.Fatal("readiness accepted the temporary initialization server")
	}
}

func TestScheduleRestartClockAndSkip(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	p := backup.Policy{ID: uuid.New(), Generation: 1, Method: "container-job", Enabled: true, Schedule: backup.Schedule{Timezone: "UTC", Hour: 10}}
	s := scheduleState{Installation: backup.Installation{Policy: p, Next: at}, Runs: []backup.Run{}}
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	j.Close()
	j, err = Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := Engine{Journal: j}
	forward := at.Add(72*time.Hour + time.Minute*5)
	if err = e.tickSchedules(context.Background(), func() time.Time { return forward }); err != nil {
		t.Fatal(err)
	}
	s, err = j.schedule(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Runs) != 1 || s.Runs[0].State != "skipped" || !s.Installation.Next.After(forward) {
		t.Fatal("missed jobs were replayed")
	}
	sequence := s.Sequence
	for _, now := range []time.Time{forward, at.Add(-time.Hour), forward.Add(time.Second)} {
		if err = e.tickSchedules(context.Background(), func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
	}
	s, _ = j.schedule(p.ID)
	if len(s.Runs) != 1 || s.Sequence != sequence {
		t.Fatal("reconnect or backwards clock duplicated a run")
	}
	// Persisted uncertain work blocks another run across restart.
	s.Runs[0].State = "running"
	s.Installation.Next = at
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	if err = e.tickSchedules(context.Background(), func() time.Time { return forward }); err != nil {
		t.Fatal(err)
	}
	s, _ = j.schedule(p.ID)
	if len(s.Runs) != 1 || s.Runs[0].State != "needs_attention" {
		t.Fatal("uncertain job repeated after restart")
	}
}

func TestScheduleHistoryKeepsSnapshotsWithinTransportBounds(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	policy := backup.Policy{ID: uuid.New(), Generation: 1}
	s := scheduleState{Installation: backup.Installation{Policy: policy}}
	roles := []backup.Role{}
	for n := range 100 {
		roles = append(roles, backup.Role{Name: fmt.Sprintf("%03d", n) + strings.Repeat("r", 60)})
	}
	for n := range 200 {
		snapshot := fmt.Sprintf("%064x", n%100)
		m := &backup.Manifest{PolicyID: policy.ID, Roles: roles, Extensions: []string{"plpgsql"}, Image: domain.PostgresImage, CreatedAt: time.Now()}
		s.Runs = append(s.Runs, backup.Run{ID: uuid.New(), PolicyID: policy.ID, Generation: 1, State: "succeeded", Snapshot: snapshot, Manifest: m, TargetID: uuid.New()})
	}
	if err = j.saveSchedule(s); err != nil {
		t.Fatal(err)
	}
	stored, err := j.schedule(policy.ID)
	if err != nil {
		t.Fatal(err)
	}
	snapshots := map[string]bool{}
	for _, r := range stored.Runs {
		snapshots[r.Snapshot] = true
		if r.ManifestHash == "" || len(r.Manifest.Roles) != 0 {
			t.Fatal("role metadata was duplicated in operational history")
		}
	}
	if len(stored.Runs) != 128 || len(snapshots) != 100 {
		t.Fatalf("history discarded a retained snapshot: %d rows, %d snapshots", len(stored.Runs), len(snapshots))
	}
	reports, err := j.ScheduleReports()
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(reports[0])
	if len(raw) > 196608 {
		t.Fatalf("valid history exceeds the report body limit: %d", len(raw))
	}
	if err = trimRunHistory(&stored, 127); err != nil {
		t.Fatal("subsequent run cannot be admitted", err)
	}
}

func TestSkippedHistoryAndAdmissionClock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := Engine{Journal: j}
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	p := backup.Policy{ID: uuid.New(), Generation: 1, Method: "container-job", Enabled: true, Schedule: backup.Schedule{Timezone: "UTC", Hour: 10}}
	if err = j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: p, Next: at}}); err != nil {
		t.Fatal(err)
	}
	for n := range 200 {
		now := at.Add(time.Duration(n)*24*time.Hour + 5*time.Minute)
		if err = e.tickSchedules(context.Background(), func() time.Time { return now }); err != nil {
			t.Fatal(err)
		}
	}
	s, _ := j.schedule(p.ID)
	if len(s.Runs) != 128 {
		t.Fatal("skipped history escaped its bound")
	}
	now := s.Installation.Next
	_ = e.tickSchedules(context.Background(), func() time.Time { return now })
	s, _ = j.schedule(p.ID)
	if s.Runs[len(s.Runs)-1].State != "needs_attention" {
		t.Fatal("a timely job could not be admitted after bounded skipped history")
	}
}
func TestScheduleAdmissionClock(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	e := Engine{Journal: j}
	at := time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)
	p := backup.Policy{Generation: 1, Method: "container-job", Enabled: true, Schedule: backup.Schedule{Timezone: "UTC", Hour: 10}}
	// Time is read after acquiring the host lock and separately for each policy.
	first, second := p, p
	first.ID = uuid.New()
	first.Schedule.Hour = 9
	second.ID = uuid.New()
	for _, entry := range []struct {
		p  backup.Policy
		at time.Time
	}{{first, at.Add(-time.Hour)}, {second, at}} {
		if err = j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: entry.p, Next: entry.at}}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	clock := func() time.Time {
		if e.mu.TryLock() {
			e.mu.Unlock()
			t.Fatal("admission clock read before host lock")
		}
		calls++
		if calls == 1 {
			return at.Add(-58 * time.Minute)
		}
		return at.Add(2 * time.Minute)
	}
	if err = e.tickSchedules(context.Background(), clock); err != nil {
		t.Fatal(err)
	}
	a, _ := j.schedule(first.ID)
	b, _ := j.schedule(second.ID)
	if len(a.Runs) != 1 || len(b.Runs) != 1 || a.Runs[0].State != "skipped" || b.Runs[0].State != "skipped" {
		t.Fatal("later policy used the first policy's admission time")
	}
}
