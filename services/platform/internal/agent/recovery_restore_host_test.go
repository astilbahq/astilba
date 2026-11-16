package agent

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// recoveryHost is one server's applied deployment of a database and a worker with a files volume.
type recoveryHost struct {
	engine     *Engine
	plan       deployment.Plan
	db, worker string
}

// newRecoveryHost applies spec on a new server, as its deployment would: the unit's network and
// volumes, a PostgreSQL container created with password, and a worker.
func newRecoveryHost(t *testing.T, docker *client.Client, spec domain.Specification, environment, unit uuid.UUID, password string) recoveryHost {
	t.Helper()
	ctx := context.Background()
	j, p := testVisualJournal(t)
	db, worker := spec.Applications[0], spec.Applications[1]
	p.EnvironmentID, p.UnitID, p.Specification, p.ExpiresAt = environment, unit, spec, time.Now().Add(time.Hour)
	p.Images = map[string]string{db.ID.String(): domain.PostgresImage, worker.ID.String(): execution.TestImage}
	p = p.WithHash()
	if err := p.Validate(true); err != nil {
		t.Fatal("fixture plan: ", err)
	}
	e := &Engine{Docker: docker, Journal: j, ResticCLI: "/opt/astilba-platform-tools/restic"}
	if err := e.ensureStorage(ctx, p); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.NetworkRemove(context.Background(), deployment.NetworkName(p.UnitID), client.NetworkRemoveOptions{})
		for _, v := range p.Specification.Volumes {
			_, _ = docker.VolumeRemove(context.Background(), deployment.VolumeName(v.ID), client.VolumeRemoveOptions{Force: true})
		}
	})
	network := container.NetworkMode(deployment.NetworkName(p.UnitID))
	h := recoveryHost{engine: e, plan: p}
	for _, a := range []struct {
		app   domain.Application
		image string
		env   []string
		out   *string
	}{
		{db, domain.PostgresImage, []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=" + password}, &h.db},
		{worker, execution.TestImage, nil, &h.worker},
	} {
		mounts := []mount.Mount{}
		for _, m := range a.app.Mounts {
			mounts = append(mounts, mount.Mount{Type: mount.TypeVolume, Source: deployment.VolumeName(m.VolumeID), Target: m.Target})
		}
		created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(a.app.ID), Config: &container.Config{Image: a.image, Env: a.env, Cmd: a.app.Command, Labels: visualLabels(p, a.app.ID)}, HostConfig: &container.HostConfig{NetworkMode: network, Mounts: mounts}})
		if err != nil {
			t.Fatal(err)
		}
		*a.out = created.ID
		t.Cleanup(func() {
			_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
		})
		if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.waitPostgresStartup(ctx, h.db, "app", "app"); err != nil {
		t.Fatal(err)
	}
	_, expected, err := e.observeUnit(ctx, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	return h
}

// remove takes the host's containers, volumes and network away, as losing the server does.
func (h recoveryHost) remove(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for _, id := range []string{h.db, h.worker} {
		if _, err := h.engine.Docker.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range h.plan.Specification.Volumes {
		if _, err := h.engine.Docker.VolumeRemove(ctx, deployment.VolumeName(v.ID), client.VolumeRemoveOptions{Force: true}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := h.engine.Docker.NetworkRemove(ctx, deployment.NetworkName(h.plan.UnitID), client.NetworkRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
}

// forgeSnapshot writes files, and symbolic links to the targets links names, into a policy's
// repository as a new snapshot, as anyone holding the repository's keys can.
func forgeSnapshot(t *testing.T, e *Engine, in backup.Installation, files map[string][]byte, links map[string]string) string {
	t.Helper()
	stage := t.TempDir()
	for name, data := range files {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(stage, name)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for name, target := range links {
		if err := os.Symlink(target, filepath.Join(stage, name)); err != nil {
			t.Fatal(err)
		}
	}
	out, err := e.restic(context.Background(), in, stage, "backup", "--quiet", "--host", in.Policy.ID.String(), ".")
	if err != nil {
		t.Fatal(err)
	}
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	for {
		var line struct {
			Type     string `json:"message_type"`
			Snapshot string `json:"snapshot_id"`
		}
		if err := json.UnmarshalDecode(dec, &line); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if line.Type == "summary" && backup.SnapshotID(line.Snapshot) {
			return line.Snapshot
		}
	}
	t.Fatal("forged snapshot not written")
	return ""
}

// PLAT-163. A replacement server restores the PostgreSQL and file snapshots a lost server uploaded
// to S3, through the restore command, though its journal never held their policies: the source
// policy bound to the replacement and its current plan, the repository's access, and the time
// management first received the snapshot are all it is given. Only the snapshot decides what is
// restored, and a snapshot that wasn't written for the policy, whose archive differs from its
// manifest, that claims to be newer than its receipt, or whose repository was altered is refused
// before anything is created. A restore stopped after its first effect is settled by removing what
// it started, and the restore's released access isn't kept once its outcome is acknowledged.
func TestNativeRecoveryRestore(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	image, worker := domain.PostgresImage, execution.TestImage
	dbID, workerID, dbVolume, filesVolume := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: dbVolume, Name: "Data"}, {ID: filesVolume, Name: "Files"}}, Applications: []domain.Application{
		{ID: dbID, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}, Mounts: []domain.VolumeMount{{VolumeID: dbVolume, Target: "/var/lib/postgresql"}}},
		{ID: workerID, Name: "Worker", Kind: "worker", Source: domain.ApplicationSource{Kind: "image", Image: &worker}, Command: []string{"sleep", "86400"}, Mounts: []domain.VolumeMount{{VolumeID: filesVolume, Target: "/data"}}},
	}}
	environment, unit, workspace := uuid.New(), uuid.New(), uuid.New()
	const password = "database-password-canary"
	s3 := startS3Fixture(t, docker, uuid.New())
	root := t.TempDir()
	if err = os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}

	// The lost server: its data, its policies, and their uploads.
	lost := newRecoveryHost(t, docker, spec, environment, unit, password)
	if _, err = lost.engine.pgQuery(ctx, pgClient{container: lost.db}, spec.Applications[0], "CREATE TABLE records(value text); INSERT INTO records VALUES ('recovered-record')"); err != nil {
		t.Fatal(err)
	}
	if _, err = lost.engine.dataOutput(ctx, pgClient{container: lost.worker}, "sh", "-c", "printf 'recovered file' > /data/record.txt"); err != nil {
		t.Fatal(err)
	}
	lost.engine.BackupRoots = []string{root}
	policy := func(name, method string, resource uuid.UUID) backup.Policy {
		return backup.Policy{ID: uuid.New(), Name: name, WorkspaceID: workspace, EnvironmentID: environment, ServerID: lost.plan.ServerID, DestinationID: uuid.New(), ResourceID: resource, Generation: 2, Method: method, Schedule: backup.Schedule{Timezone: "UTC", Hour: 2}, Retain: 7, Enabled: true, Plan: lost.plan}
	}
	s3Access := func(repository string) backup.Access {
		return backup.Access{AccessKey: s3.access.AccessKey, SecretKey: s3.access.SecretKey, Password: repository}
	}
	type uploaded struct {
		in       backup.Installation
		snapshot string
		manifest backup.Manifest
	}
	upload := func(p backup.Policy, destination backup.Destination, access backup.Access, cli string) uploaded {
		t.Helper()
		destination.ID = p.DestinationID
		in := backup.Installation{Policy: p, Destination: destination, Access: access}
		if p.Method == "postgres-logical" {
			in.Access.DatabasePassword = password
		}
		lost.engine.ResticCLI = cli
		if err := lost.engine.prepareRepository(ctx, &in, true); err != nil {
			t.Fatal(err)
		}
		s := scheduleState{Installation: in, Runs: []backup.Run{}}
		if err := lost.engine.runScheduled(ctx, &s, time.Now().UTC(), uuid.New(), "", uuid.Nil); err != nil {
			t.Fatalf("upload %s: %v", p.Name, err)
		}
		in.Access.DatabasePassword = ""
		return uploaded{in: in, snapshot: s.Runs[0].Snapshot, manifest: *s.Runs[0].Manifest}
	}
	database := upload(policy("Database", "postgres-logical", dbID), s3.destination, s3Access("database-repository-canary"), s3.restic)
	files := upload(policy("Files", backup.QuiescedFiles, filesVolume), s3.destination, s3Access("files-repository-canary"), s3.restic)
	local := upload(policy("Local files", backup.QuiescedFiles, filesVolume), backup.Destination{Name: "Local", Kind: "filesystem", Root: root}, backup.Access{Password: "local-repository-canary"}, "/opt/astilba-platform-tools/restic")
	received := time.Now().UTC()
	lost.remove(t)

	// The replacement: the same deployment applied on a new server, with an empty database.
	replacement := newRecoveryHost(t, docker, spec, environment, unit, password)
	e := replacement.engine
	e.ResticCLI = s3.restic
	e.BackupRoots = []string{root}
	for _, u := range []uploaded{database, files, local} {
		if _, err = e.Journal.schedule(u.in.Policy.ID); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the replacement already held a policy: ", err)
		}
	}
	generation := int64(0)
	restore := func(u uploaded, snapshot string, at time.Time, target uuid.UUID, change func(*backup.Recovery)) (api.ExecutionCommand, Record) {
		t.Helper()
		view := u.in
		view.Policy.ServerID, view.Policy.Plan = replacement.plan.ServerID, replacement.plan
		v := backup.Recovery{Installation: view, RunID: uuid.New(), Snapshot: snapshot, TargetID: target, ReceivedAt: at}
		if change != nil {
			change(&v)
		}
		raw, _ := stablejson.Marshal(v)
		value := jsontext.Value(raw)
		generation++
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: replacement.plan.ServerID, EnvironmentId: environment, ResourceId: u.in.Policy.ID, Kind: "restore", Action: "inspect", Generation: generation, PlanHash: v.Digest(), Backup: &value, Deadline: time.Now().Add(time.Hour)}
		if err := e.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := e.Journal.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		// Management acknowledges the outcome, so the host takes the next command.
		if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return c, r
	}
	absent := func(t *testing.T, target uuid.UUID) {
		t.Helper()
		if _, err := docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); err == nil {
			t.Fatal("a refused restore created its target")
		}
	}
	result := func(t *testing.T, r Record) recoveryResult {
		t.Helper()
		var out recoveryResult
		if r.Report.State != "succeeded" || r.Report.Restore == nil || json.Unmarshal(*r.Report.Restore, &out) != nil {
			t.Fatalf("restore ended %s: %s", r.Report.State, r.Report.Note)
		}
		return out
	}

	t.Run("database", func(t *testing.T) {
		target := uuid.New()
		c, r := restore(database, database.snapshot, received, target, nil)
		got := result(t, r)
		if got.Snapshot != database.snapshot || got.TargetID != target || got.Manifest.SHA256 != database.manifest.SHA256 || got.Manifest.RunID != database.manifest.RunID {
			t.Fatalf("restore reported another snapshot: %+v", got)
		}
		if v, err := docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); err != nil || v.Volume.Labels[labelPostgresMajor] != domain.PostgresMajor(domain.PostgresImage) {
			t.Fatal("the target does not record its PostgreSQL major: ", err)
		}
		created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: domain.PostgresImage, Env: []string{"POSTGRES_PASSWORD=" + password}}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(target), Target: domain.PostgresMount(domain.PostgresImage)}}}})
		if err != nil {
			t.Fatal(err)
		}
		defer docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
		if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		if err = e.waitPostgresStartup(ctx, created.ID, "app", "app"); err != nil {
			t.Fatal(err)
		}
		value, err := e.pgQuery(ctx, pgClient{container: created.ID}, spec.Applications[0], "SELECT value FROM records")
		if err != nil || strings.TrimSpace(string(value)) != "recovered-record" {
			t.Fatalf("restored records differ: %q %v", value, err)
		}
		live, err := e.pgQuery(ctx, pgClient{container: replacement.db}, spec.Applications[0], "SELECT count(*) FROM pg_tables WHERE tablename = 'records'")
		if err != nil || strings.TrimSpace(string(live)) != "0" {
			t.Fatal("the restore changed the live database")
		}
		for _, n := range []int64{r.Report.Sequence, r.Report.Sequence + 1} {
			if err = e.Journal.Acknowledge(c.Id, n); err != nil {
				t.Fatal(err)
			}
		}
		sealed := sealedRecord(t, e.Journal, c.Id)
		if bytes.Contains(sealed, []byte(s3.access.SecretKey)) || bytes.Contains(sealed, []byte("database-repository-canary")) || bytes.Contains(sealed, []byte(password)) {
			t.Fatal("an acknowledged restore kept the access released for it")
		}
	})

	t.Run("files", func(t *testing.T) {
		target := uuid.New()
		_, r := restore(files, files.snapshot, received, target, nil)
		if got := result(t, r); got.Snapshot != files.snapshot || got.Manifest.SHA256 != files.manifest.SHA256 {
			t.Fatalf("restore reported another snapshot: %+v", got)
		}
		view := files.in.Policy
		view.ServerID, view.Plan = replacement.plan.ServerID, replacement.plan
		helper, cleanup, err := e.dataContainer(ctx, view, target, uuid.New(), true)
		if err != nil {
			t.Fatal(err)
		}
		data, err := e.dataOutput(ctx, pgClient{container: helper}, "cat", "/data/record.txt")
		cleanup()
		if err != nil || string(data) != "recovered file" {
			t.Fatalf("restored file differs: %q %v", data, err)
		}
	})

	// Each is refused before anything is created, and says why.
	genuine := database.manifest
	archive := []byte("an archive that is not the database's dump")
	manifest := func(change func(*backup.Manifest)) []byte {
		m := genuine
		change(&m)
		raw, _ := stablejson.Marshal(m)
		return raw
	}
	for _, refusal := range []struct {
		name, note string
		files      map[string][]byte
		links      map[string]string
		receivedAt time.Time
		change     func(*backup.Recovery)
	}{
		{name: "manifest_for_another_resource", note: "snapshot was not written for this policy", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) { m.ResourceID = filesVolume }), "data.dump": archive}},
		{name: "manifest_for_another_policy", note: "snapshot was not written for this policy", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) { m.PolicyID = files.in.Policy.ID }), "data.dump": archive}},
		{name: "archive_unlike_its_manifest", note: "snapshot archive does not match its manifest", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) {}), "data.dump": archive}},
		// A large declared footprint fails before anything is downloaded or created.
		{name: "more_than_this_server_can_hold", note: "insufficient restore capacity", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) { m.Bytes = 1 << 39 }), "data.dump": archive}},
		{name: "archive_larger_than_its_manifest", note: "snapshot archive is larger than its manifest says", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) { m.Bytes = 4 }), "data.dump": archive}},
		// Restored as a link, it would be read without end.
		{name: "archive_that_is_a_link", note: "snapshot has no archive file beside its manifest", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) {})}, links: map[string]string{"data.dump": "/dev/zero"}},
		{name: "a_database_this_deployment_does_not_run", note: "the snapshot's database is not the one this deployment runs", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) { m.Database = "other" }), "data.dump": archive}},
		{name: "two_manifests", note: "snapshot must hold exactly one manifest", files: map[string][]byte{"manifest.json": manifest(func(m *backup.Manifest) {}), "copy/manifest.json": manifest(func(m *backup.Manifest) {}), "data.dump": archive}},
		// The access opens the repository, but the policy names another destination.
		{name: "another_destination", note: "Check the destination's access", change: func(v *backup.Recovery) { v.Installation.Destination.ID = uuid.New() }},
		{name: "a_target_the_deployment_declares", note: "restore target must be a new volume", change: func(v *backup.Recovery) { v.TargetID = filesVolume }},
		{name: "newer_than_its_receipt", note: "snapshot claims to be newer than its first report", receivedAt: genuine.CreatedAt.Add(-backup.RecoveryClockAllowance - time.Minute)},
		{name: "another_deployment", note: "this server does not run the deployment the restore names", change: func(v *backup.Recovery) {
			p := replacement.plan
			p.RevisionID = uuid.New()
			v.Installation.Policy.Plan = p.WithHash()
		}},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			snapshot, at := database.snapshot, received
			if refusal.files != nil {
				snapshot = forgeSnapshot(t, e, database.in, refusal.files, refusal.links)
			}
			if !refusal.receivedAt.IsZero() {
				at = refusal.receivedAt
			}
			target := uuid.New()
			_, r := restore(database, snapshot, at, target, refusal.change)
			if r.Report.State != "failed" || !strings.Contains(r.Report.Note, refusal.note) || r.Report.Restore != nil {
				t.Fatalf("ended %s: %s", r.Report.State, r.Report.Note)
			}
			absent(t, target)
		})
	}

	t.Run("altered_pack", func(t *testing.T) {
		e.ResticCLI = "/opt/astilba-platform-tools/restic"
		defer func() { e.ResticCLI = s3.restic }()
		packs := filepath.Join(root, workspace.String(), local.in.Policy.ID.String(), "repository", "data")
		altered := 0
		if err := filepath.WalkDir(packs, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			raw[len(raw)/2] ^= 0xff
			altered++
			return os.WriteFile(path, raw, 0600)
		}); err != nil || altered == 0 {
			t.Fatal("no pack altered: ", err)
		}
		target := uuid.New()
		_, r := restore(local, local.snapshot, received, target, nil)
		if r.Report.State != "failed" || r.Report.Restore != nil {
			t.Fatalf("an altered repository was restored: %s %s", r.Report.State, r.Report.Note)
		}
		absent(t, target)
	})

	t.Run("stopped_after_its_first_effect", func(t *testing.T) {
		target := uuid.New()
		stopping, stop := context.WithCancel(ctx)
		defer stop()
		var left string
		e.Point = func(point string) {
			if point != recoveryEffect {
				return
			}
			// What a restore leaves when its agent stops: a container labelled with its run.
			r, err := e.Journal.Record(currentRestore(t, e))
			if err != nil {
				t.Error(err)
				return
			}
			var v backup.Recovery
			_ = json.Unmarshal(*r.Command.Backup, &v)
			labels := visualLabels(replacement.plan, target)
			labels["com.astilba.run"] = v.RunID.String()
			created, err := docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
			if err == nil {
				_, err = docker.ContainerStart(context.Background(), created.ID, client.ContainerStartOptions{})
			}
			if err != nil {
				t.Error(err)
			}
			left = created.ID
			stop()
		}
		view := files.in
		view.Policy.ServerID, view.Policy.Plan = replacement.plan.ServerID, replacement.plan
		v := backup.Recovery{Installation: view, RunID: uuid.New(), Snapshot: files.snapshot, TargetID: target, ReceivedAt: received}
		raw, _ := stablejson.Marshal(v)
		value := jsontext.Value(raw)
		generation++
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: replacement.plan.ServerID, EnvironmentId: environment, ResourceId: v.Installation.Policy.ID, Kind: "restore", Action: "inspect", Generation: generation, PlanHash: v.Digest(), Backup: &value, Deadline: time.Now().Add(time.Hour)}
		if err := e.Execute(stopping, c); !errors.Is(err, context.Canceled) {
			t.Fatal("the stopping agent finished the restore: ", err)
		}
		e.Point = nil
		if r, err := e.Journal.Record(c.Id); err != nil || r.Report.State != "running" || r.Checkpoint != recoveryEffect {
			t.Fatal("the stopped restore did not keep its checkpoint: ", err)
		}
		// The agent starts again.
		if err := e.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := e.Journal.Record(c.Id)
		if err != nil || r.Report.State != "failed" || !strings.Contains(r.Report.Note, "Nothing it started is still running") {
			t.Fatalf("ended %s: %s %v", r.Report.State, r.Report.Note, err)
		}
		if _, err = docker.ContainerInspect(ctx, left, client.ContainerInspectOptions{}); err == nil {
			t.Fatal("the stopped restore's container was left")
		}
		if err = e.Journal.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("failed_after_its_first_effect", func(t *testing.T) {
		target, run := uuid.New(), uuid.Nil
		t.Cleanup(func() {
			_, _ = docker.VolumeRemove(context.Background(), deployment.VolumeName(target), client.VolumeRemoveOptions{Force: true})
		})
		e.Point = func(point string) {
			if point != recoveryEffect {
				return
			}
			// A container holding the name the restore's own data container needs makes the restore
			// fail once its volume exists. It carries the run's label, as the restore's would.
			r, err := e.Journal.Record(currentRestore(t, e))
			if err != nil {
				t.Error(err)
				return
			}
			var v backup.Recovery
			_ = json.Unmarshal(*r.Command.Backup, &v)
			run = v.RunID
			labels := visualLabels(replacement.plan, target)
			labels["com.astilba.run"] = run.String()
			if _, err = docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{Name: "astilba-data-" + v.RunID.String(), Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none"}}); err != nil {
				t.Error(err)
			}
		}
		_, r := restore(files, files.snapshot, received, target, nil)
		e.Point = nil
		if r.Report.State != "failed" || !strings.Contains(r.Report.Note, "failed while restoring or verifying its data") || !strings.Contains(r.Report.Note, "Nothing it started is still running") || r.Report.Restore != nil {
			t.Fatalf("ended %s: %s", r.Report.State, r.Report.Note)
		}
		if _, err := docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); err != nil {
			t.Fatal("the restore failed before its first effect: ", err)
		}
		list, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: runContainers(replacement.plan.ServerID, run)})
		if err != nil || len(list.Items) != 0 {
			t.Fatal("the failed restore left its run's containers: ", err)
		}
	})
}

// currentRestore is the restore command this host is running.
func currentRestore(t *testing.T, e *Engine) uuid.UUID {
	t.Helper()
	pending, err := e.Journal.Pending()
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range pending {
		if r.Command.Kind == "restore" && r.Report.State == "running" {
			return r.Command.Id
		}
	}
	t.Fatal("no restore running")
	return uuid.Nil
}
