package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
)

type cappedOutput struct {
	bytes.Buffer
	limit int
}

func (w *cappedOutput) Write(p []byte) (int, error) {
	if w.Len()+len(p) > w.limit {
		return 0, errors.New("tool output limit exceeded")
	}
	return w.Buffer.Write(p)
}
func (e *Engine) restic(ctx context.Context, in backup.Installation, dir string, args ...string) ([]byte, error) {
	cli := e.ResticCLI
	if cli == "" {
		cli = "restic"
	}
	version, err := exec.CommandContext(ctx, cli, "version").Output()
	if err != nil || !strings.HasPrefix(string(version), "restic "+backup.ResticVersion+" ") {
		return nil, errors.New("supported restic version required")
	}
	secret, err := os.MkdirTemp(e.Journal.dir, "restic-access-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(secret)
	if err = os.WriteFile(filepath.Join(secret, "password"), []byte(in.Access.Password), 0600); err != nil {
		return nil, err
	}
	repo := ""
	if in.Destination.Kind == "filesystem" {
		repo = filepath.Join(in.Destination.Root, in.Policy.WorkspaceID.String(), in.Policy.ID.String(), "repository")
	} else {
		repo = "s3:" + in.Destination.Endpoint + "/" + in.Destination.Bucket + "/astilba/" + in.Policy.WorkspaceID.String() + "/" + in.Policy.ID.String()
	}
	cmd := exec.CommandContext(ctx, cli, append([]string{"--no-cache", "--json"}, args...)...)
	cmd.Dir = dir
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + secret, "RESTIC_REPOSITORY=" + repo, "RESTIC_PASSWORD_FILE=" + filepath.Join(secret, "password"), "AWS_ACCESS_KEY_ID=" + in.Access.AccessKey, "AWS_SECRET_ACCESS_KEY=" + in.Access.SecretKey, "AWS_DEFAULT_REGION=" + in.Destination.Region, "TMPDIR=" + secret}
	out := &cappedOutput{limit: 1 << 20}
	cmd.Stdout = out
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return nil, errors.New("repository command failed")
	}
	return out.Bytes(), nil
}
func (e *Engine) repositoryOwnership(in backup.Installation, create bool) error {
	if backup.ValidateDestination(in.Destination, in.Access) != nil || in.Destination.ID != in.Policy.DestinationID || len(in.Access.Password) < 16 {
		return errors.New("invalid repository configuration")
	}
	if in.Destination.Kind != "filesystem" {
		return nil
	}
	allowed := false
	for _, root := range e.BackupRoots {
		if filepath.Clean(root) == in.Destination.Root {
			allowed = true
		}
	}
	if !allowed {
		return errors.New("filesystem root is not enabled by the host operator")
	}
	dir := in.Destination.Root
	// Operator roots must already exist; symlinks cannot redirect a configured root.
	actual, err := filepath.EvalSymlinks(dir)
	if err != nil || actual != dir {
		return errors.New("backup root must be an existing real directory")
	}
	for _, component := range []string{in.Policy.WorkspaceID.String(), in.Policy.ID.String()} {
		dir = filepath.Join(dir, component)
		if create {
			if err = os.Mkdir(dir, 0700); err != nil && !os.IsExist(err) {
				return err
			}
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("private destination directory required")
		}
	}
	marker := filepath.Join(dir, "owner.json")
	expected := []byte(backup.Hash(struct{ Workspace, Policy string }{in.Policy.WorkspaceID.String(), in.Policy.ID.String()}))
	if create {
		f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err == nil {
			_, err = f.Write(expected)
			if err == nil {
				err = f.Sync()
			}
			closeErr := f.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		} else if !os.IsExist(err) {
			return err
		}
	}
	info, err := os.Lstat(marker)
	if err != nil || !info.Mode().IsRegular() {
		return errors.New("repository ownership marker unavailable")
	}
	raw, err := os.ReadFile(marker)
	if err != nil || !bytes.Equal(raw, expected) {
		return errors.New("repository ownership mismatch")
	}
	return nil
}
func (e *Engine) prepareRepository(ctx context.Context, in *backup.Installation, create bool) error {
	if err := e.repositoryOwnership(*in, create); err != nil {
		return err
	}
	raw, err := e.restic(ctx, *in, e.Journal.dir, "cat", "config")
	if err != nil && create && in.RepositoryID == "" {
		if _, err = e.restic(ctx, *in, e.Journal.dir, "init", "--repository-version", "2"); err != nil {
			return err
		}
		raw, err = e.restic(ctx, *in, e.Journal.dir, "cat", "config")
	}
	if err != nil {
		return err
	}
	var cfg struct {
		ID      string `json:"id"`
		Version int    `json:"version"`
	}
	if json.Unmarshal(raw, &cfg) != nil || !backup.SnapshotID(cfg.ID) || cfg.Version != 2 || in.RepositoryID != "" && cfg.ID != in.RepositoryID {
		return errors.New("repository identity mismatch")
	}
	in.RepositoryID = cfg.ID
	return nil
}
func (e *Engine) backupSnapshot(ctx context.Context, s *scheduleState, r *backup.Run) error {
	in := &s.Installation
	if err := e.checkBackupSource(ctx, in.Policy); err != nil {
		return err
	}
	if err := e.prepareRepository(ctx, in, false); err != nil {
		return err
	}
	if freeBytes(e.Journal.dir) < 256<<20 {
		return errors.New("insufficient staging capacity")
	}
	stage, err := os.MkdirTemp(e.Journal.dir, "backup-stage-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	m := backup.Manifest{Recipe: backup.Recipe, PolicyID: in.Policy.ID, RunID: r.ID, EnvironmentID: in.Policy.EnvironmentID, ResourceID: in.Policy.ResourceID, Method: in.Policy.Method, CreatedAt: r.StartedAt, Roles: []backup.Role{}, Extensions: []string{}}
	if in.Policy.Method == "postgres-logical" {
		err = e.dumpPostgres(ctx, in.Policy, stage, &m)
	} else {
		err = e.dumpFiles(ctx, s, stage, &m)
	}
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(m)
	if err = os.WriteFile(filepath.Join(stage, "manifest.json"), raw, 0600); err != nil {
		return err
	}
	out, err := e.restic(ctx, *in, stage, "backup", "--quiet", "--host", in.Policy.ID.String(), "--tag", "policy="+in.Policy.ID.String(), "--tag", "run="+r.ID.String(), ".")
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	snapshot := ""
	for dec.More() {
		var line struct {
			Type     string `json:"message_type"`
			Snapshot string `json:"snapshot_id"`
		}
		if err = dec.Decode(&line); err != nil {
			return err
		}
		if line.Type == "summary" {
			snapshot = line.Snapshot
		}
	}
	if !backup.SnapshotID(snapshot) || ctx.Err() != nil {
		return errors.New("backup did not finish")
	}
	r.Snapshot = snapshot
	r.Manifest = &m
	r.ManifestHash = backup.Hash(m)
	stored := *r
	compactManifest(&stored)
	s.Runs[len(s.Runs)-1] = stored
	if err = e.Journal.saveSchedule(*s); err != nil {
		return err
	}
	copyRuns := append([]backup.Run{}, s.Runs...)
	copyRun := *r
	copyRun.State = "succeeded"
	copyRuns[len(copyRuns)-1] = copyRun
	for _, id := range backup.Prunable(copyRuns, in.Policy.Retain) {
		if err = e.repositoryOwnership(*in, false); err != nil {
			return err
		}
		if _, err = e.restic(ctx, *in, stage, "forget", id, "--prune", "--max-repack-size", "1G"); err != nil {
			return err
		}
		for i := range s.Runs {
			if s.Runs[i].Snapshot == id {
				s.Runs[i].Pruned = true
			}
		}
		if err = e.Journal.saveSchedule(*s); err != nil {
			return err
		}
	}
	return nil
}
