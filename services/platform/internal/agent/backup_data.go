package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func (e *Engine) dataTool(ctx context.Context, input io.Reader, out io.Writer, args ...string) error {
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + e.Docker.DaemonHost()}
	cmd.Stdin = input
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("data tool failed")
	}
	return nil
}
func (e *Engine) dataOutput(ctx context.Context, db pgClient, args ...string) ([]byte, error) {
	out := &boundedOutput{limit: 65536, stopOnLimit: true}
	if err := e.containerExec(ctx, db, out, args...); err != nil {
		return nil, err
	}
	return out.b.Bytes(), nil
}
func (e *Engine) pgQuery(ctx context.Context, db pgClient, a domain.Application, sql string) ([]byte, error) {
	return e.dataOutput(ctx, db, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-c", sql)
}

// dumpPostgres dumps the source database from a helper labelled with the run, so removing the
// run's containers ends the dump and its connection whatever stopped it.
func (e *Engine) dumpPostgres(ctx context.Context, in backup.Installation, run uuid.UUID, stage string, m *backup.Manifest) error {
	a, err := sourceApp(in.Policy)
	if err != nil {
		return err
	}
	c, err := e.sourceContainer(ctx, in.Policy, a.ID)
	if err != nil {
		return err
	}
	labels := visualLabels(in.Policy.Plan, a.ID)
	labels[labelKind] = "backup-dump-v1"
	labels["com.astilba.run"] = run.String()
	helper, err := e.postgresHelper(ctx, "astilba-dump-"+run.String(), labels, c.ID, *a.Source.Image, in.Access.DatabasePassword)
	if err != nil {
		return err
	}
	defer e.removeHelper(helper)
	if err = e.postgresManifest(ctx, helper, a, *a.Source.Image, m); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = e.containerExec(ctx, helper, file, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", a.Postgres.Username, "-d", a.Postgres.Database)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return checksumFile(filepath.Join(stage, "data.dump"), m)
}
func checksumFile(path string, m *backup.Manifest) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	m.Bytes = n
	m.SHA256 = hex.EncodeToString(h.Sum(nil))
	return nil
}
func (e *Engine) quiesceFiles(ctx context.Context, s *scheduleState) error {
	p := s.Installation.Policy
	list, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true})
	if err != nil {
		return err
	}
	for _, v := range list.Items {
		uses := false
		for _, m := range v.Mounts {
			if m.Type == mount.TypeVolume && m.Name == deployment.VolumeName(p.ResourceID) {
				uses = true
			}
		}
		if !uses {
			continue
		}
		id, err := uuid.Parse(v.Labels[labelResource])
		if err != nil || !visualOwned(v.Labels, p.Plan) {
			return errors.New("unknown volume user")
		}
		declared := false
		for _, a := range p.Plan.Specification.Applications {
			if a.ID == id {
				for _, m := range a.Mounts {
					if m.VolumeID == p.ResourceID {
						declared = true
					}
				}
			}
		}
		if !declared {
			return errors.New("undeclared volume user")
		}
		if v.State == "running" {
			s.Writers = append(s.Writers, v.ID)
		}
	}
	if err = e.Journal.saveSchedule(*s); err != nil {
		return err
	}
	for _, id := range s.Writers {
		if _, err = e.Docker.ContainerStop(ctx, id, client.ContainerStopOptions{}); err != nil {
			return err
		}
	}
	// Re-inspect all containers after stopping; another mount or writer cannot be
	// accepted merely because it was absent from the initial list.
	list, err = e.Docker.ContainerList(ctx, client.ContainerListOptions{})
	if err != nil {
		return err
	}
	for _, v := range list.Items {
		for _, m := range v.Mounts {
			if m.Type == mount.TypeVolume && m.Name == deployment.VolumeName(p.ResourceID) {
				return errors.New("volume still has a running writer")
			}
		}
	}
	return nil
}
func (e *Engine) resumeBackupWriters(ctx context.Context, s *scheduleState) error {
	for _, id := range s.Writers {
		c, err := e.Docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || !visualOwned(c.Container.Config.Labels, s.Installation.Policy.Plan) {
			return errors.New("stopped writer identity changed")
		}
		if !c.Container.State.Running {
			if _, err = e.Docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
				return err
			}
		}
	}
	s.Writers = nil
	return e.Journal.saveSchedule(*s)
}
func (e *Engine) dumpFiles(ctx context.Context, s *scheduleState, stage string, m *backup.Manifest) (err error) {
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err = errors.Join(err, e.resumeBackupWriters(cleanup, s))
	}()
	if err = e.quiesceFiles(ctx, s); err != nil {
		return err
	}
	// Labelled with the run, so an interrupted run's helper is found where the run is inspected.
	name, cleanup, err := e.dataContainer(ctx, s.Installation.Policy, s.Installation.Policy.ResourceID, m.RunID, true)
	if err != nil {
		return err
	}
	defer cleanup()
	f, err := os.OpenFile(filepath.Join(stage, "files.tar"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	err = e.dataTool(ctx, nil, f, "exec", name, "tar", "-cpf", "-", "-C", "/data", ".")
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if _, err = backup.ArchiveContents(filepath.Join(stage, "files.tar")); err != nil {
		return err
	}
	return checksumFile(filepath.Join(stage, "files.tar"), m)
}
func (e *Engine) dataContainer(ctx context.Context, p backup.Policy, volume, run uuid.UUID, readOnly bool) (string, func(), error) {
	name := "astilba-data-" + run.String()
	labels := visualLabels(p.Plan, volume)
	labels[labelKind] = "backup-data-v1"
	labels["com.astilba.run"] = run.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, CapAdd: []string{"CHOWN", "DAC_OVERRIDE", "FOWNER"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: "/data", ReadOnly: readOnly}}, Resources: container.Resources{Memory: 128 << 20, PidsLimit: ptr(int64(32))}}})
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() {
		clean, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = e.Docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{Force: true})
	}
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return created.ID, cleanup, nil
}
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
	stored, err := e.restoreArchive(ctx, *in, r.ID, r.TargetID, r.Snapshot, m, original.ManifestHash, func() error { return nil })
	if err != nil {
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

// restoreArchive restores snapshot into the absent volume target, and returns the manifest it found
// in the snapshot. m is the manifest the caller expects, and manifestHash its hash in full: the
// snapshot must hold exactly one manifest with that hash, and an archive matching m's size and
// digest. begin is called once these checks pass, just before the volume is created, which is the
// restore's first effect outside the journal and its staging directory. The restored data is read
// back and compared with the archive before it returns.
func (e *Engine) restoreArchive(ctx context.Context, in backup.Installation, run, target uuid.UUID, snapshot string, m backup.Manifest, manifestHash string, begin func() error) (backup.Manifest, error) {
	// Reserve headroom for the downloaded archive and expanded database, retaining
	// both old and new volumes. Refuse unknown capacity rather than assuming room.
	if freeBytes(e.Journal.dir) < m.Bytes*2+256<<20 || dockerFreeBytes(ctx, e.Docker) < m.Bytes*4+256<<20 {
		return backup.Manifest{}, restoreRefusal("insufficient restore capacity")
	}
	if _, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return backup.Manifest{}, restoreRefusal("restore target must be absent")
	}
	stage, err := os.MkdirTemp(e.Journal.dir, "restore-stage-")
	if err != nil {
		return backup.Manifest{}, err
	}
	defer os.RemoveAll(stage)
	if _, err = e.restic(ctx, in, stage, "restore", snapshot, "--target", stage, "--verify"); err != nil {
		return backup.Manifest{}, err
	}
	manifestPath := ""
	err = filepath.WalkDir(stage, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Name() == "manifest.json" {
			if manifestPath != "" {
				return errors.New("ambiguous snapshot")
			}
			manifestPath = path
		}
		return nil
	})
	if err != nil || manifestPath == "" {
		return backup.Manifest{}, restoreRefusal("snapshot manifest unavailable")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return backup.Manifest{}, err
	}
	var stored backup.Manifest
	if json.Unmarshal(raw, &stored) != nil || backup.Hash(stored) != manifestHash {
		return backup.Manifest{}, restoreRefusal("snapshot manifest differs from the one expected")
	}
	dir := filepath.Dir(manifestPath)
	filename := "files.tar"
	if m.Method == "postgres-logical" {
		filename = "data.dump"
	}
	checked := m
	if err = checksumFile(filepath.Join(dir, filename), &checked); err != nil || checked.Bytes != m.Bytes || checked.SHA256 != m.SHA256 {
		return backup.Manifest{}, restoreRefusal("snapshot archive does not match its manifest")
	}
	if err = begin(); err != nil {
		return backup.Manifest{}, err
	}
	if _, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(target), Driver: "local", Labels: restoreTargetLabels(in.Policy.Plan, target, m)}); err != nil {
		return backup.Manifest{}, err
	}
	if _, err = e.volumePath(ctx, in.Policy, target); err != nil {
		return backup.Manifest{}, err
	}
	if m.Method == "postgres-logical" {
		err = e.restorePostgres(ctx, in.Policy, run, target, dir, stored, in.Access.DatabasePassword)
	} else {
		err = e.restoreFiles(ctx, in.Policy, run, target, dir, stage)
	}
	if err != nil || ctx.Err() != nil {
		return backup.Manifest{}, errors.Join(errors.New("fresh restore failed"), err, ctx.Err())
	}
	return stored, nil
}

// restoreFiles extracts dir's files.tar into the target volume and compares what the volume then
// holds, contents and metadata, with the archive.
func (e *Engine) restoreFiles(ctx context.Context, p backup.Policy, run, target uuid.UUID, dir, stage string) error {
	name, cleanup, err := e.dataContainer(ctx, p, target, run, false)
	if err != nil {
		return err
	}
	defer cleanup()
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
func (e *Engine) restorePostgres(ctx context.Context, p backup.Policy, run, target uuid.UUID, dir string, m backup.Manifest, password string) error {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || (m.Image != domain.PostgresImage && m.Image != domain.Postgres17Image) || m.Database != a.Postgres.Database || m.Username != a.Postgres.Username {
		return errors.New("unsupported restore recipe")
	}
	if password == "" {
		return errors.New("database initialization credential unavailable")
	}
	maintenance := "postgres"
	if m.Database == maintenance {
		maintenance = "template1"
	}
	labels := visualLabels(p.Plan, target)
	labels[labelKind] = "backup-restore-v1"
	labels["com.astilba.run"] = run.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-restore-" + run.String(), Config: &container.Config{Image: m.Image, Env: []string{"POSTGRES_USER=" + m.Username, "POSTGRES_DB=" + m.Database, "POSTGRES_PASSWORD=" + password}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(target), Target: domain.PostgresMount(m.Image)}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
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
	if err = e.waitPostgresStartup(ctx, created.ID, m.Username, m.Database); err != nil {
		return err
	}
	for _, role := range m.Roles {
		if role.Name == m.Username || len(role.Name) > 63 || strings.ContainsRune(role.Name, 0) {
			return errors.New("invalid role")
		}
		if _, err = e.pgQuery(ctx, pgClient{container: created.ID}, a, "CREATE ROLE "+quoteIdentifier(role.Name)+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
			return err
		}
	}
	file, err := os.Open(filepath.Join(dir, "data.dump"))
	if err != nil {
		return err
	}
	defer file.Close()
	// Recreate only inside this fresh, isolated cluster. CREATE DATABASE cannot
	// run in a transaction; a failed restore never exposes this target to writers.
	if err = e.dataTool(ctx, file, io.Discard, "exec", "-i", created.ID, "pg_restore", "--exit-on-error", "--clean", "--if-exists", "--create", "-U", m.Username, "-d", maintenance); err != nil {
		return err
	}
	// Re-dump with the same pinned tool to verify all archived definitions and data,
	// including database settings, owners, grants and sequence contents.
	verify, err := os.CreateTemp(e.Journal.dir, "restore-verification-")
	if err != nil {
		return err
	}
	defer os.Remove(verify.Name())
	dumpArgs := []string{"exec", created.ID, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", m.Username, "-d", m.Database}
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
		err = e.dataTool(ctx, file, out, "exec", "-i", created.ID, "pg_restore", "--create", "--file=-", "--restrict-key=AstilbaRestoreVerification")
		closeErr := file.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
	}
	if !bytes.Equal(originalSQL.Sum(nil), restoredSQL.Sum(nil)) {
		return errors.New("restored database differs from archived records or schema")
	}
	if _, err = e.Docker.ContainerStop(ctx, created.ID, client.ContainerStopOptions{}); err != nil {
		return err
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

func (e *Engine) postgresManifest(ctx context.Context, db pgClient, a domain.Application, image string, m *backup.Manifest) error {
	versionNumber, dataDirectory := "180004", "/var/lib/postgresql/18/docker"
	if image == domain.Postgres17Image {
		versionNumber, dataDirectory = "170010", "/var/lib/postgresql/data"
	} else if image != domain.PostgresImage {
		return errUnsupportedPostgresImage
	}
	version, err := e.pgQuery(ctx, db, a, "SHOW server_version_num")
	if err != nil || strings.TrimSpace(string(version)) != versionNumber {
		return errors.New("unsupported PostgreSQL version")
	}
	layout, err := e.pgQuery(ctx, db, a, "SHOW data_directory")
	if err != nil || strings.TrimSpace(string(layout)) != dataDirectory {
		return errors.New("unsupported PostgreSQL layout")
	}
	// Restore is intentionally limited to unprivileged NOLOGIN roles. SQL from a
	// source database is never passed through as arbitrary cluster administration.
	raw, err := e.pgQuery(ctx, db, a, `SELECT coalesce(json_agg(json_build_object('name',rolname,'unsupported',rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolconfig IS NOT NULL) ORDER BY rolname),'[]') FROM pg_roles WHERE rolname NOT LIKE 'pg_%' AND rolname<>current_user`)
	if err != nil {
		return err
	}
	var roles []struct {
		Name        string `json:"name"`
		Unsupported bool   `json:"unsupported"`
	}
	if json.Unmarshal(raw, &roles) != nil || len(roles) > 100 {
		return errors.New("unsupported roles")
	}
	for _, r := range roles {
		if r.Unsupported || len(r.Name) > 63 {
			return errors.New("unsupported role attributes")
		}
		m.Roles = append(m.Roles, backup.Role{Name: r.Name})
	}
	members, err := e.pgQuery(ctx, db, a, "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid JOIN pg_roles u ON u.oid=m.member WHERE r.rolname NOT LIKE 'pg_%' OR u.rolname NOT LIKE 'pg_%'")
	if err != nil || strings.TrimSpace(string(members)) != "0" {
		return errors.New("role memberships require a separate recipe")
	}
	raw, err = e.pgQuery(ctx, db, a, "SELECT coalesce(json_agg(extname ORDER BY extname),'[]') FROM pg_extension")
	if err != nil || json.Unmarshal(raw, &m.Extensions) != nil {
		return errors.New("extension query failed")
	}
	for _, ext := range m.Extensions {
		if ext != "plpgsql" && ext != "hstore" {
			return errors.New("unsupported extension")
		}
	}
	supported, err := e.pgQuery(ctx, db, a, "SELECT coalesce(bool_and(extversion=CASE extname WHEN 'plpgsql' THEN '1.0' WHEN 'hstore' THEN '1.8' ELSE '' END),false) FROM pg_extension")
	if err != nil || strings.TrimSpace(string(supported)) != "t" {
		return errors.New("unsupported extension version")
	}

	m.Image = image
	m.Database = a.Postgres.Database
	m.Username = a.Postgres.Username
	return nil
}
