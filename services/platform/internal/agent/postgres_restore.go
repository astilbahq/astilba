package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// ensureImage pulls a pinned public image the host does not hold, such as the release a restore
// or an upgrade runs when its database's container was removed.
func (e *Engine) ensureImage(ctx context.Context, image string) error {
	_, err := e.Docker.ImageInspect(ctx, image)
	if errdefs.IsNotFound(err) {
		return e.pullImageAuth(ctx, image, "")
	}
	return err
}

// restoreRefusal is a restore refused before it touched anything, with a reason the owner can act on.
type restoreRefusal struct{ error }

func (r restoreRefusal) Unwrap() error { return r.error }
func quoteIdentifier(v string) string  { return `"` + strings.ReplaceAll(v, `"`, `""`) + `"` }

// restorePostgres restores the archive into a fresh cluster of the given release, then proves the
// cluster holds exactly what was archived. A restore's caller chose the release with
// backup.RestoreRelease; the supported upgrade restores a PostgreSQL 17 archive into PostgreSQL 18.
func (e *Engine) restorePostgres(ctx context.Context, p backup.Policy, r *backup.Run, dir string, m backup.Manifest, password string, release domain.PostgresRelease, upgrade bool) error {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || m.Database != a.Postgres.Database || m.Username != a.Postgres.Username {
		return errors.New("unsupported restore recipe")
	}
	// A restore's release comes from backup.RestoreRelease; the upgrade's crosses majors.
	if upgrade {
		archive, known := domain.PostgresReleaseOf(m.Image)
		if !known {
			return errors.New("unsupported PostgreSQL release")
		}
		if err = archive.UpgradesTo(release); err != nil {
			return restoreRefusal{err}
		}
	}
	if err = e.ensureImage(ctx, release.Image); err != nil {
		return err
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
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-restore-" + r.ID.String(), Config: &container.Config{Image: release.Image, Env: []string{"POSTGRES_USER=" + m.Username, "POSTGRES_DB=" + m.Database, "POSTGRES_PASSWORD=" + password}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(r.TargetID), Target: release.Mount}}, Resources: container.Resources{Memory: 512 << 20, PidsLimit: ptr(int64(128))}}})
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
	// The upgrade dumped with every identifier quoted, so the comparison dump must too.
	if upgrade {
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
		out := &dumpVersionHeader{out: item.out}
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
