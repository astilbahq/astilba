package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
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
func (e *Engine) dataOutput(ctx context.Context, id string, args ...string) ([]byte, error) {
	out := &boundedOutput{limit: 65536, stopOnLimit: true}
	if err := e.dataTool(ctx, nil, out, append([]string{"exec", id}, args...)...); err != nil {
		return nil, err
	}
	return out.b.Bytes(), nil
}
func (e *Engine) sourceContainer(ctx context.Context, p backup.Policy, id uuid.UUID) (container.InspectResponse, error) {
	list, err := e.ownedContainers(ctx, p.Plan)
	if err != nil {
		return container.InspectResponse{}, err
	}
	for _, v := range list {
		if v.Labels[labelResource] == id.String() {
			found, err := e.Docker.ContainerInspect(ctx, v.ID, client.ContainerInspectOptions{})
			if err != nil {
				return container.InspectResponse{}, err
			}
			if !visualOwned(found.Container.Config.Labels, p.Plan) || found.Container.Config.Labels[labelResource] != id.String() || found.Container.Config.Image != p.Plan.Images[id.String()] {
				return container.InspectResponse{}, errors.New("source ownership or image changed")
			}
			return found.Container, nil
		}
	}
	return container.InspectResponse{}, errors.New("source not found")
}
func sourceApp(p backup.Policy) (domain.Application, error) {
	for _, a := range p.Plan.Specification.Applications {
		if a.ID == p.ResourceID {
			return a, nil
		}
	}
	return domain.Application{}, errors.New("source not found")
}
func (e *Engine) checkBackupSource(ctx context.Context, p backup.Policy) error {
	if e.Docker == nil {
		return errors.New("Docker unavailable")
	}
	cfg, err := e.Journal.Config()
	if err != nil || cfg.ServerID != p.ServerID || cfg.Role == "build" {
		return errors.New("source server mismatch")
	}
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil || unit.Plan.Hash != p.Plan.Hash {
		return errors.New("policy must be refreshed after deployment")
	}
	observation, _, err := e.observeUnit(ctx, p.Plan, nil)
	for _, resource := range observation.Resources {
		observation.ResourceDrift = observation.ResourceDrift || resource.Drift
	}
	if err != nil || observation.ResourceDrift || observation.InfrastructureDrift {
		return errors.New("source drift requires review")
	}
	if p.Method == "postgres-logical" || p.Method == "container-job" {
		a, err := sourceApp(p)
		if err != nil {
			return err
		}
		if p.Method == "postgres-logical" && (a.Postgres == nil || p.Plan.Specification.Compose != nil || !deployment.ResolvedImage(*a.Source.Image, p.Plan.Images[a.ID.String()])) {
			return errors.New("unsupported PostgreSQL recipe")
		}
		if p.Method == "container-job" && (a.Postgres != nil || p.Plan.Specification.Compose != nil) {
			return errors.New("scheduled jobs require an image worker")
		}
		c, err := e.sourceContainer(ctx, p, p.ResourceID)
		if err != nil {
			return err
		}
		if !c.State.Running {
			return errors.New("source is stopped")
		}
		return nil
	}
	exists := false
	for _, v := range p.Plan.Specification.Volumes {
		exists = exists || v.ID == p.ResourceID
	}
	if !exists {
		return errors.New("volume missing")
	}
	for _, a := range p.Plan.Specification.Applications {
		for _, m := range a.Mounts {
			if m.VolumeID == p.ResourceID && a.Postgres != nil {
				return errors.New("database files require native logical backup")
			}
		}
	}
	_, err = e.volumePath(ctx, p, p.ResourceID)
	return err
}
func (e *Engine) volumePath(ctx context.Context, p backup.Policy, id uuid.UUID) (string, error) {
	v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(id), client.VolumeInspectOptions{})
	if err != nil || !visualOwned(v.Volume.Labels, p.Plan) || v.Volume.Labels[labelResource] != id.String() || v.Volume.Driver != "local" || len(v.Volume.Options) > 0 {
		return "", errors.New("volume ownership or driver mismatch")
	}
	return v.Volume.Mountpoint, nil
}
func (e *Engine) pgQuery(ctx context.Context, c string, a domain.Application, sql string) ([]byte, error) {
	return e.dataOutput(ctx, c, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-c", sql)
}
func (e *Engine) dumpPostgres(ctx context.Context, p backup.Policy, stage string, m *backup.Manifest) error {
	a, err := sourceApp(p)
	if err != nil {
		return err
	}
	c, err := e.sourceContainer(ctx, p, a.ID)
	if err != nil {
		return err
	}
	if err = e.postgresManifest(ctx, c.ID, a, *a.Source.Image, m); err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = e.dataTool(ctx, nil, file, "exec", c.ID, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", a.Postgres.Username, "-d", a.Postgres.Database)
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
	name, cleanup, err := e.dataContainer(ctx, s.Installation.Policy, s.Installation.Policy.ResourceID, uuid.New(), true)
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
	// Reserve headroom for the downloaded archive and expanded database, retaining
	// both old and new volumes. Refuse unknown capacity rather than assuming room.
	if freeBytes(e.Journal.dir) < m.Bytes*2+256<<20 || dockerFreeBytes(ctx, e.Docker) < m.Bytes*4+256<<20 {
		return errors.New("insufficient restore capacity")
	}
	if _, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(r.TargetID), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return errors.New("restore target must be absent")
	}
	stage, err := os.MkdirTemp(e.Journal.dir, "restore-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	if _, err = e.restic(ctx, *in, stage, "restore", r.Snapshot, "--target", stage, "--verify"); err != nil {
		return err
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
		return errors.New("snapshot manifest unavailable")
	}
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var stored backup.Manifest
	if json.Unmarshal(raw, &stored) != nil || backup.Hash(stored) != original.ManifestHash {
		return errors.New("manifest differs from uploaded record")
	}
	dir := filepath.Dir(manifestPath)
	filename := "files.tar"
	if m.Method == "postgres-logical" {
		filename = "data.dump"
	}
	checked := m
	if err = checksumFile(filepath.Join(dir, filename), &checked); err != nil || checked.Bytes != m.Bytes || checked.SHA256 != m.SHA256 {
		return errors.New("snapshot checksum mismatch")
	}
	if _, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(r.TargetID), Driver: "local", Labels: visualLabels(in.Policy.Plan, r.TargetID)}); err != nil {
		return err
	}
	if _, err = e.volumePath(ctx, in.Policy, r.TargetID); err != nil {
		return err
	}
	if m.Method == "postgres-logical" {
		err = e.restorePostgres(ctx, in.Policy, r, dir, stored, in.Access.DatabasePassword)
	} else {
		var name string
		var cleanup func()
		name, cleanup, err = e.dataContainer(ctx, in.Policy, r.TargetID, r.ID, false)
		if err != nil {
			return err
		}
		defer cleanup()
		var f *os.File
		f, err = os.Open(filepath.Join(dir, "files.tar"))
		if err == nil {
			defer f.Close()
			err = e.dataTool(ctx, f, io.Discard, "exec", "-i", name, "tar", "-xpf", "-", "-C", "/data")
		}
		if err == nil {
			verification, openErr := os.CreateTemp(stage, "verified-files-")
			if openErr != nil {
				return openErr
			}
			err = e.dataTool(ctx, nil, verification, "exec", name, "tar", "-cpf", "-", "-C", "/data", ".")
			err = errors.Join(err, verification.Close())
			if err != nil {
				return err
			}
			expected, readErr := backup.ArchiveContents(filepath.Join(dir, "files.tar"))
			if readErr != nil {
				return readErr
			}
			actual, readErr := backup.ArchiveContents(verification.Name())
			if readErr != nil {
				return readErr
			}
			if actual != expected {
				return errors.New("restored files or metadata differ from archive")
			}
		}

	}
	if err != nil || ctx.Err() != nil {
		return errors.Join(errors.New("fresh restore failed"), err, ctx.Err())
	}
	r.Manifest = &stored
	r.ManifestHash = backup.Hash(stored)
	r.Verified = true
	return nil
}
func quoteIdentifier(v string) string { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }
func (e *Engine) restorePostgres(ctx context.Context, p backup.Policy, r *backup.Run, dir string, m backup.Manifest, password string) error {
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
	labels := visualLabels(p.Plan, r.TargetID)
	labels[labelKind] = "backup-restore-v1"
	labels["com.astilba.run"] = r.ID.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-restore-" + r.ID.String(), Config: &container.Config{Image: m.Image, Env: []string{"POSTGRES_USER=" + m.Username, "POSTGRES_DB=" + m.Database, "POSTGRES_PASSWORD=" + password}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(r.TargetID), Target: domain.PostgresMount(m.Image)}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
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
		if _, err = e.pgQuery(ctx, created.ID, a, "CREATE ROLE "+quoteIdentifier(role.Name)+" NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"); err != nil {
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
		if _, err := e.dataOutput(ctx, container, "pg_isready", "-h", "127.0.0.1", "-U", user, "-d", database); err == nil {
			return nil
		}
		if !wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return errors.New("fresh database not ready")
}

func (e *Engine) runContainerJob(ctx context.Context, s *scheduleState, r *backup.Run) error {
	p := s.Installation.Policy
	if err := e.checkBackupSource(ctx, p); err != nil {
		return err
	}
	a, err := sourceApp(p)
	if err != nil || a.ServiceKind() != "worker" {
		return errors.New("scheduled jobs require a worker")
	}
	source, err := e.sourceContainer(ctx, p, a.ID)
	if err != nil {
		return err
	}
	labels := visualLabels(p.Plan, a.ID)
	labels[labelKind] = "scheduled-job-v1"
	labels["com.astilba.run"] = r.ID.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-job-" + r.ID.String(), Config: &container.Config{Image: p.Plan.Images[a.ID.String()], Cmd: p.Command, Env: source.Config.Env, Labels: labels, WorkingDir: source.Config.WorkingDir, User: source.Config.User}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.Plan.UnitID)), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: source.HostConfig.Mounts, Resources: container.Resources{Memory: 256 << 20, PidsLimit: ptr(int64(64))}}})
	if err != nil {
		return err
	}
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	for {
		v, err := e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if !v.Container.State.Running {
			code := v.Container.State.ExitCode
			r.ExitCode = &code
			_, err = e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{})
			if code != 0 {
				return fmt.Errorf("job exit %d", code)
			}
			return err
		}
		if !wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
}

func (e *Engine) checkRestoreOwnership(ctx context.Context, p backup.Policy) error {
	cfg, err := e.Journal.Config()
	if err != nil || cfg.ServerID != p.ServerID || cfg.Role == "build" {
		return errors.New("restore server mismatch")
	}
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil || unit.Plan.Hash != p.Plan.Hash {
		return errors.New("restore policy must match the applied deployment")
	}
	volume := p.ResourceID
	if p.Method == "postgres-logical" {
		app, err := sourceApp(p)
		if err != nil || app.Postgres == nil {
			return errors.New("unsupported database source")
		}
		volume = app.Mounts[0].VolumeID
		c, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(app.ID), client.ContainerInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
		if err == nil && (!visualOwned(c.Container.Config.Labels, p.Plan) || c.Container.Config.Labels[labelResource] != app.ID.String() || c.Container.Config.Image != p.Plan.Images[app.ID.String()]) {
			return errors.New("source container ownership changed")
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

func (e *Engine) postgresManifest(ctx context.Context, id string, a domain.Application, image string, m *backup.Manifest) error {
	versionNumber, dataDirectory := "180004", "/var/lib/postgresql/18/docker"
	if image == domain.Postgres17Image {
		versionNumber, dataDirectory = "170010", "/var/lib/postgresql/data"
	} else if image != domain.PostgresImage {
		return errors.New("unsupported PostgreSQL image")
	}
	version, err := e.pgQuery(ctx, id, a, "SHOW server_version_num")
	if err != nil || strings.TrimSpace(string(version)) != versionNumber {
		return errors.New("unsupported PostgreSQL version")
	}
	layout, err := e.pgQuery(ctx, id, a, "SHOW data_directory")
	if err != nil || strings.TrimSpace(string(layout)) != dataDirectory {
		return errors.New("unsupported PostgreSQL layout")
	}
	// Restore is intentionally limited to unprivileged NOLOGIN roles. SQL from a
	// source database is never passed through as arbitrary cluster administration.
	raw, err := e.pgQuery(ctx, id, a, `SELECT coalesce(json_agg(json_build_object('name',rolname,'unsupported',rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolconfig IS NOT NULL) ORDER BY rolname),'[]') FROM pg_roles WHERE rolname NOT LIKE 'pg_%' AND rolname<>current_user`)
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
	members, err := e.pgQuery(ctx, id, a, "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid JOIN pg_roles u ON u.oid=m.member WHERE r.rolname NOT LIKE 'pg_%' OR u.rolname NOT LIKE 'pg_%'")
	if err != nil || strings.TrimSpace(string(members)) != "0" {
		return errors.New("role memberships require a separate recipe")
	}
	raw, err = e.pgQuery(ctx, id, a, "SELECT coalesce(json_agg(extname ORDER BY extname),'[]') FROM pg_extension")
	if err != nil || json.Unmarshal(raw, &m.Extensions) != nil {
		return errors.New("extension query failed")
	}
	for _, ext := range m.Extensions {
		if ext != "plpgsql" && ext != "hstore" {
			return errors.New("unsupported extension")
		}
	}
	supported, err := e.pgQuery(ctx, id, a, "SELECT coalesce(bool_and(extversion=CASE extname WHEN 'plpgsql' THEN '1.0' WHEN 'hstore' THEN '1.8' ELSE '' END),false) FROM pg_extension")
	if err != nil || strings.TrimSpace(string(supported)) != "t" {
		return errors.New("unsupported extension version")
	}

	m.Image = image
	m.Database = a.Postgres.Database
	m.Username = a.Postgres.Username
	return nil
}
