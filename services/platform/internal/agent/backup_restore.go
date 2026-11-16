package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// restoreSnapshot restores a snapshot from this server's own policy catalogue into a new volume,
// once the snapshot's manifest matches the one its upload recorded.
func (e *Engine) restoreSnapshot(ctx context.Context, s *scheduleState, r *backup.Run) error {
	in := &s.Installation
	if in.Policy.Method == "container-job" || !backup.SnapshotID(r.Snapshot) || r.TargetID == uuid.Nil {
		return errors.New("invalid restore")
	}
	if err := e.checkRestoreOwnership(ctx, in.Policy); err != nil {
		return err
	}
	if err := e.beginEffects(ctx, s, r); err != nil {
		return err
	}
	if err := e.prepareRepository(ctx, in, false); err != nil {
		return err
	}
	var original *backup.Run
	for i := range s.Runs {
		v := &s.Runs[i]
		if v.Snapshot == r.Snapshot && v.State == "succeeded" && !v.Pruned && v.Manifest != nil {
			original = v
			break
		}
	}
	if original == nil {
		return errors.New("snapshot not in successful policy catalogue")
	}
	m := *original.Manifest
	if m.Recipe != backup.Recipe || m.PolicyID != in.Policy.ID || m.ResourceID != in.Policy.ResourceID || m.Method != in.Policy.Method || m.Bytes <= 0 || m.Bytes > 1<<40 || !backup.SnapshotID(m.SHA256) {
		return errors.New("invalid backup manifest")
	}
	stored, archive, err := e.snapshotManifest(ctx, *in, r.Snapshot)
	if err != nil {
		return err
	}
	if backup.Hash(stored) != original.ManifestHash {
		return errors.New("snapshot manifest differs from the uploaded record")
	}
	if err = e.restoreArchive(ctx, *in, r.ID, r.TargetID, r.Snapshot, archive, stored, func() error { return nil }); err != nil {
		return err
	}
	r.Manifest = &stored
	r.ManifestHash = backup.Hash(stored)
	r.Verified = true
	return nil
}

// restoreRefusal is a check that stopped a restore before it created anything. Its text says which.
type restoreRefusal string

func (r restoreRefusal) Error() string { return string(r) }

// archiveName is the file a backup of method writes its data to, beside its manifest.
func archiveName(method string) string {
	if method == "postgres-logical" {
		return "data.dump"
	}
	return "files.tar"
}

// snapshotManifest reads the one manifest a snapshot holds and finds the archive file beside it,
// without restoring anything. A snapshot's content is decided by whoever wrote it, so nothing else
// in it is read.
func (e *Engine) snapshotManifest(ctx context.Context, in backup.Installation, snapshot string) (backup.Manifest, string, error) {
	out, err := e.restic(ctx, in, e.Journal.dir, "ls", snapshot)
	if err != nil {
		return backup.Manifest{}, "", err
	}
	dec := jsontext.NewDecoder(bytes.NewReader(out))
	manifest, archives := "", map[string]string{}
	for {
		var node struct {
			StructType string `json:"struct_type"`
			Type       string `json:"type"`
			Name       string `json:"name"`
			Path       string `json:"path"`
			Size       int64  `json:"size"`
		}
		err := json.UnmarshalDecode(dec, &node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return backup.Manifest{}, "", restoreRefusal("snapshot listing is unreadable")
		}
		if node.StructType != "node" {
			continue
		}
		if node.Name == archiveName(in.Policy.Method) {
			archives[node.Path] = node.Type
		}
		if node.Name != "manifest.json" {
			continue
		}
		if manifest != "" || node.Type != "file" || node.Size <= 0 || node.Size > backup.MaxManifestBytes {
			return backup.Manifest{}, "", restoreRefusal("snapshot must hold exactly one manifest")
		}
		manifest = node.Path
	}
	if manifest == "" {
		return backup.Manifest{}, "", restoreRefusal("snapshot holds no manifest")
	}
	archive := path.Join(path.Dir(manifest), archiveName(in.Policy.Method))
	if archives[archive] != "file" {
		return backup.Manifest{}, "", restoreRefusal("snapshot has no archive file beside its manifest")
	}
	raw, err := e.restic(ctx, in, e.Journal.dir, "dump", snapshot, manifest)
	if err != nil {
		return backup.Manifest{}, "", err
	}
	var m backup.Manifest
	if len(raw) > backup.MaxManifestBytes || json.Unmarshal(raw, &m) != nil {
		return backup.Manifest{}, "", restoreRefusal("snapshot manifest is unreadable")
	}
	return m, archive, nil
}

// restoreArchive restores the archive file at path archive in snapshot, which m describes, into the
// absent volume target. Only that file is downloaded, and no more of it than m says it holds. begin
// is called once it matches m, just before the volume is created, which is the restore's first
// effect outside the journal and its staging directory. The restored data is read back and compared
// with the archive before it returns.
func (e *Engine) restoreArchive(ctx context.Context, in backup.Installation, run, target uuid.UUID, snapshot, archive string, m backup.Manifest, begin func() error) error {
	// Reserve headroom for the downloaded archive and expanded database, retaining
	// both old and new volumes. Refuse unknown capacity rather than assuming room.
	if freeBytes(e.Journal.dir) < m.Bytes*2+256<<20 || dockerFreeBytes(ctx, e.Docker) < m.Bytes*4+256<<20 {
		return restoreRefusal("insufficient restore capacity")
	}
	var database domain.Application
	if m.Method == "postgres-logical" {
		a, err := postgresRecipe(in.Policy, m)
		if err != nil {
			return restoreRefusal("the snapshot's database is not the one this deployment runs")
		}
		database = a
	}
	// A volume the deployment declares would be adopted by it, unreviewed, if a restore created it.
	if slices.ContainsFunc(in.Policy.Plan.Specification.Volumes, func(v domain.Volume) bool { return v.ID == target }) {
		return restoreRefusal("restore target must be a new volume")
	}
	if _, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return restoreRefusal("restore target must be absent")
	}
	stage, err := os.MkdirTemp(e.Journal.dir, "restore-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	file := filepath.Join(stage, archiveName(m.Method))
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	capped := &cappedWriter{w: f, left: m.Bytes}
	err = errors.Join(e.resticTo(ctx, in, stage, capped, "dump", snapshot, archive), f.Close())
	if capped.over {
		return restoreRefusal("snapshot archive is larger than its manifest says")
	}
	if err != nil {
		return err
	}
	checked := m
	if err = checksumFile(file, &checked); err != nil || checked.Bytes != m.Bytes || checked.SHA256 != m.SHA256 {
		return restoreRefusal("snapshot archive does not match its manifest")
	}
	if err = begin(); err != nil {
		return err
	}
	if _, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(target), Driver: "local", Labels: restoreTargetLabels(in.Policy.Plan, target, m)}); err != nil {
		return err
	}
	if _, err = e.volumePath(ctx, in.Policy, target); err != nil {
		return err
	}
	if m.Method == "postgres-logical" {
		err = e.restorePostgres(ctx, in.Policy, database, run, target, stage, m, in.Access.DatabasePassword)
	} else {
		err = e.restoreFiles(ctx, in.Policy, run, target, stage, stage)
	}
	if err != nil || ctx.Err() != nil {
		return errors.Join(errors.New("fresh restore failed"), err, ctx.Err())
	}
	return nil
}

// cappedWriter writes to w until it has been offered more than left bytes, then fails.
type cappedWriter struct {
	w    io.Writer
	left int64
	over bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.over = true
		return 0, errors.New("more data than expected")
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// restoreFiles extracts dir's files.tar into the target volume and compares what the volume then
// holds, contents and metadata, with the archive.
func (e *Engine) restoreFiles(ctx context.Context, p backup.Policy, run, target uuid.UUID, dir, stage string) error {
	name, cleanup, err := e.dataContainer(ctx, p, target, run, false)
	if err != nil {
		return err
	}
	defer cleanup()
	e.point("restore-container")
	f, err := os.Open(filepath.Join(dir, "files.tar"))
	if err != nil {
		return err
	}
	defer f.Close()
	if err = e.dataTool(ctx, f, io.Discard, "exec", "-i", name, "tar", "-xpf", "-", "-C", "/data"); err != nil {
		return err
	}
	verification, err := os.CreateTemp(stage, "verified-files-")
	if err != nil {
		return err
	}
	err = e.dataTool(ctx, nil, verification, "exec", name, "tar", "-cpf", "-", "-C", "/data", ".")
	err = errors.Join(err, verification.Close())
	if err != nil {
		return err
	}
	expected, err := backup.ArchiveContents(filepath.Join(dir, "files.tar"))
	if err != nil {
		return err
	}
	actual, err := backup.ArchiveContents(verification.Name())
	if err != nil {
		return err
	}
	if actual != expected {
		return errors.New("restored files or metadata differ from archive")
	}
	return nil
}
func quoteIdentifier(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }

// postgresRecipe is the policy's database application, if m's archive can be restored for it: made
// with one of the pinned images, and holding the database and owner the deployment names.
func postgresRecipe(p backup.Policy, m backup.Manifest) (domain.Application, error) {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || (m.Image != domain.PostgresImage && m.Image != domain.Postgres17Image) || m.Database != a.Postgres.Database || m.Username != a.Postgres.Username {
		return domain.Application{}, errors.New("unsupported restore recipe")
	}
	return a, nil
}

// restorePostgres restores dir's data.dump, which m describes, into a fresh cluster on the target
// volume for a, the application postgresRecipe found for it. A dump may come from a server that was
// lost, and its SQL runs as the cluster's superuser, so each container that touches the cluster
// runs as postgres with no capabilities or network and mounts only its target volume, and the dump
// reaches it only through pg_restore's standard input. Once the dump's server has stopped, the
// cluster is held to what a dump of one database can leave before a fresh server verifies it.
func (e *Engine) restorePostgres(ctx context.Context, p backup.Policy, a domain.Application, run, target uuid.UUID, dir string, m backup.Manifest, password string) error {
	if password == "" {
		return errors.New("database initialization credential unavailable")
	}
	maintenance := "postgres"
	if m.Database == maintenance {
		maintenance = "template1"
	}
	var b clusterBaseline
	if err := e.restoreServer(ctx, p, run, target, m, "astilba-restore-", []string{"POSTGRES_USER=" + m.Username, "POSTGRES_DB=" + m.Database, "POSTGRES_PASSWORD=" + password}, nil, func(id string) (err error) {
		for _, role := range m.Roles {
			if role.Name == m.Username || len(role.Name) > 63 || strings.ContainsRune(role.Name, 0) {
				return errors.New("invalid role")
			}
			if _, err = e.pgQuery(ctx, pgClient{container: id}, a, "CREATE ROLE "+quoteIdentifier(role.Name)+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
				return err
			}
		}
		// Read before the dump runs anything.
		if b, err = e.baseline(ctx, id, m); err != nil {
			return err
		}
		file, err := os.Open(filepath.Join(dir, "data.dump"))
		if err != nil {
			return err
		}
		defer file.Close()
		// Recreate only inside this fresh, isolated cluster. CREATE DATABASE cannot
		// run in a transaction; a failed restore never exposes this target to writers.
		return e.dataTool(ctx, file, io.Discard, "exec", "-i", id, "pg_restore", "--exit-on-error", "--clean", "--if-exists", "--create", "-U", m.Username, "-d", maintenance)
	}); err != nil {
		return err
	}
	if err := e.resetClusterFiles(ctx, p, run, target, m.Image, b.files); err != nil {
		return err
	}
	// A server that has just started reads the cluster as the deployment's database will, not
	// through what the dump's server kept in memory. No client of it runs event triggers, and
	// autovacuum, which can run the dump's functions, stays off.
	if err := e.restoreServer(ctx, p, run, target, m, "astilba-verify-", []string{"PGOPTIONS=-c event_triggers=off"}, []string{"postgres", "-c", "autovacuum=off"}, func(id string) error {
		if err := e.checkRestoredCluster(ctx, id, m, b); err != nil {
			return err
		}
		return e.verifyRestoredDump(ctx, a, id, dir, m)
	}); err != nil {
		return err
	}
	// The dump's code can still have run while that server did, so its files are checked and
	// reset again.
	return e.resetClusterFiles(ctx, p, run, target, m.Image, b.files)
}

// restoreServer runs the pinned image's server on target in its own container, isolated as
// restorePostgres describes, calls use once it accepts connections, and then stops it, which must
// leave the cluster shut down cleanly. The container is gone when it returns.
func (e *Engine) restoreServer(ctx context.Context, p backup.Policy, run, target uuid.UUID, m backup.Manifest, name string, env, cmd []string, use func(id string) error) error {
	labels := visualLabels(p.Plan, target)
	labels[labelKind] = "backup-restore-v1"
	labels["com.astilba.run"] = run.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name + run.String(), Config: &container.Config{Image: m.Image, User: "postgres", Env: env, Cmd: cmd, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(target), Target: domain.PostgresMount(m.Image)}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = e.Docker.ContainerRemove(cleanup, created.ID, client.ContainerRemoveOptions{Force: true})
	}()
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	e.point("restore-container")
	if err = e.waitPostgresStartup(ctx, created.ID, m.Username, m.Database); err != nil {
		return err
	}
	if err = use(created.ID); err != nil {
		return err
	}
	if _, err = e.Docker.ContainerStop(ctx, created.ID, client.ContainerStopOptions{Timeout: ptr(60)}); err != nil {
		return err
	}
	stopped, err := e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil || stopped.Container.State.Running || stopped.Container.State.ExitCode != 0 {
		return errors.Join(errors.New("restored database did not stop cleanly"), err)
	}
	return nil
}

// verifyRestoredDump re-dumps the restored database with the same pinned tool and compares it
// with dir's archive, all definitions and data, including database settings, owners, grants and
// sequence contents.
func (e *Engine) verifyRestoredDump(ctx context.Context, a domain.Application, id, dir string, m backup.Manifest) error {
	verify, err := os.CreateTemp(e.Journal.dir, "restore-verification-")
	if err != nil {
		return err
	}
	defer os.Remove(verify.Name())
	dumpArgs := []string{"exec", id, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", m.Username, "-d", m.Database}
	crossMajor := a.Source.Image != nil && *a.Source.Image == domain.Postgres17Image && m.Image == domain.PostgresImage
	if crossMajor {
		dumpArgs = append(dumpArgs, "--quote-all-identifiers")
	}
	err = e.dataTool(ctx, nil, verify, dumpArgs...)
	err = errors.Join(err, verify.Close())
	if err != nil {
		return err
	}
	// The pinned PostgreSQL tools can use a fixed restrict key for deterministic
	// SQL. Hash complete streams, including COPY records, without retaining them.
	originalSQL := sha256.New()
	restoredSQL := sha256.New()
	for _, item := range []struct {
		path string
		out  io.Writer
	}{{filepath.Join(dir, "data.dump"), originalSQL}, {verify.Name(), restoredSQL}} {
		file, err := os.Open(item.path)
		if err != nil {
			return err
		}
		out := item.out
		if crossMajor {
			out = &upgradeSQLHeader{out: out}
		}
		err = e.dataTool(ctx, file, out, "exec", "-i", id, "pg_restore", "--create", "--file=-", "--restrict-key=AstilbaRestoreVerification")
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if !bytes.Equal(originalSQL.Sum(nil), restoredSQL.Sum(nil)) {
		return errors.New("restored database differs from archived records or schema")
	}
	return nil
}

func (e *Engine) waitPostgresStartup(ctx context.Context, container, user, database string) error {
	for range 60 {
		// The image's temporary initialization server accepts Unix-socket
		// connections. TCP becomes available only after initialization finishes.
		if _, err := e.dataOutput(ctx, pgClient{container: container}, "pg_isready", "-h", "127.0.0.1", "-U", user, "-d", database); err == nil {
			return nil
		}
		if !wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return errors.New("fresh database not ready")
}

func (e *Engine) checkRestoreOwnership(ctx context.Context, p backup.Policy) error {
	cfg, err := e.Journal.Config()
	if err != nil || cfg.ServerID != p.ServerID || cfg.Role == "build" {
		return restoreRefusal("this server is not the one the restore names")
	}
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil || unit.Plan.Hash != p.Plan.Hash {
		return restoreRefusal("this server does not run the deployment the restore names")
	}
	volume := p.ResourceID
	if p.Method == "postgres-logical" {
		app, err := sourceApp(p)
		if err != nil || app.Postgres == nil {
			return restoreRefusal("the deployment has no such database")
		}
		volume = app.Mounts[0].VolumeID
		c, err := e.Docker.ContainerInspect(ctx, p.Plan.ContainerName(app.ID), client.ContainerInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		if err == nil && (!visualOwned(c.Container.Config.Labels, p.Plan) || c.Container.Config.Labels[labelResource] != app.ID.String() || c.Container.Config.Image != p.Plan.Images[app.ID.String()]) {
			return restoreRefusal("the deployment's database container is not the one it describes")
		}
	}
	_, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	_, err = e.volumePath(ctx, p, volume)
	return err
}
