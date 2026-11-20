package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
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
func (e *Engine) postgresManifest(ctx context.Context, db pgClient, a domain.Application, image string, m *backup.Manifest) error {
	release, ok := domain.PostgresReleaseOf(image)
	if !ok {
		return errUnsupportedPostgresImage
	}
	version, err := e.pgQuery(ctx, db, a, "SHOW server_version_num")
	if err != nil || strings.TrimSpace(string(version)) != strconv.Itoa(release.Version) {
		return errors.New("unsupported PostgreSQL version")
	}
	layout, err := e.pgQuery(ctx, db, a, "SHOW data_directory")
	if err != nil || strings.TrimSpace(string(layout)) != release.DataDirectory {
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
		if !slices.Contains(backup.Extensions, ext) {
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
