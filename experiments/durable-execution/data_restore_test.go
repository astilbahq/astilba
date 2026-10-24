// SPDX-License-Identifier: AGPL-3.0-only

package durableexecution

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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type backupRole struct {
	Name        string `json:"name"`
	Login       bool   `json:"login"`
	Super       bool   `json:"super"`
	CreateDB    bool   `json:"createdb"`
	CreateRole  bool   `json:"createrole"`
	Replication bool   `json:"replication"`
	BypassRLS   bool   `json:"bypassrls"`
}
type backupManifest struct {
	Version         int    `json:"version"`
	Image           string `json:"image"`
	ServerVersion   int    `json:"server_version"`
	SourceVolume    string `json:"source_volume"`
	SourceContainer string `json:"source_container"`
	DataDirectory   string `json:"data_directory"`
	ArchiveSHA      string `json:"archive_sha256"`
	ArchiveBytes    int64  `json:"archive_bytes"`
	RolesSHA        string `json:"roles_sha256"`
}

func pgCommand(ctx context.Context, h *runtimeHarness, p runtimeProject, input io.Reader, args ...string) ([]byte, error) {
	argv := append([]string{"exec", "-i", p.db}, args...)
	c := exec.CommandContext(ctx, hostTools+"/bin/docker", argv...)
	c.Env = buildEnv(h.config)
	c.Stdin = input
	return c.CombinedOutput()
}
func pgSQL(t *testing.T, h *runtimeHarness, p runtimeProject, sql string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, e := pgCommand(ctx, h, p, nil, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-v", "ON_ERROR_STOP=1", "-c", sql)
	if e != nil {
		t.Fatalf("fixture SQL failed: %v: %s", e, out)
	}
	return strings.TrimSpace(string(out))
}
func waitPG(t *testing.T, h *runtimeHarness, p runtimeProject) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if e := hostAwait(ctx, func() error {
		_, e := pgCommand(ctx, h, p, nil, "pg_isready", "-h", "127.0.0.1", "-U", "fixture", "-d", "fixture")
		return e
	}); e != nil {
		t.Fatal(e)
	}
}

func pgLayout(ctx context.Context, h *runtimeHarness, p runtimeProject) (backupManifest, error) {
	var mounts []struct{ Name, Destination, Type string }
	out, e := hostCommand(ctx, h.config, nil, "inspect", "--format", "{{json .Mounts}}", p.db)
	if e != nil {
		return backupManifest{}, e
	}
	if e = json.Unmarshal(out, &mounts); e != nil {
		return backupManifest{}, e
	}
	volume := ""
	for _, mount := range mounts {
		if mount.Destination == "/var/lib/postgresql" && mount.Type == "volume" {
			volume = mount.Name
		}
	}
	if volume == "" {
		return backupManifest{}, errors.New("unsupported PostgreSQL volume layout")
	}
	version, e := pgCommand(ctx, h, p, nil, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-c", "SHOW server_version_num")
	if e != nil {
		return backupManifest{}, e
	}
	number, e := strconv.Atoi(strings.TrimSpace(string(version)))
	if e != nil || number/10000 != 18 {
		return backupManifest{}, errors.New("unsupported PostgreSQL server version")
	}
	directory, e := pgCommand(ctx, h, p, nil, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-c", "SHOW data_directory")
	if e != nil {
		return backupManifest{}, e
	}
	if strings.TrimSpace(string(directory)) != "/var/lib/postgresql/18/docker" {
		return backupManifest{}, errors.New("unsupported PostgreSQL data directory")
	}
	tool, e := pgCommand(ctx, h, p, nil, "pg_dump", "--version")
	if e != nil || !strings.Contains(string(tool), "18.4") {
		return backupManifest{}, errors.New("unsupported dump tool version")
	}
	id, e := hostCommand(ctx, h.config, nil, "inspect", "--format", "{{.Id}}", p.db)
	if e != nil {
		return backupManifest{}, e
	}
	image, e := hostCommand(ctx, h.config, nil, "inspect", "--format", "{{.Config.Image}}", p.db)
	if e != nil || strings.TrimSpace(string(image)) != postgresImage {
		return backupManifest{}, errors.New("unsupported PostgreSQL image")
	}
	return backupManifest{Version: 1, Image: postgresImage, ServerVersion: number, SourceVolume: volume, SourceContainer: strings.TrimSpace(string(id)), DataDirectory: strings.TrimSpace(string(directory))}, nil
}

func createBackup(ctx context.Context, h *runtimeHarness, p runtimeProject, database, destination string) (backupManifest, error) {
	metadata, e := pgLayout(ctx, h, p)
	if e != nil {
		return metadata, e
	}
	if _, e := os.Stat(destination); !errors.Is(e, os.ErrNotExist) {
		return metadata, errors.New("backup destination must be new")
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".backup-")
	if e != nil {
		return metadata, e
	}
	defer os.RemoveAll(stage)
	roles, e := pgCommand(ctx, h, p, nil, "psql", "-XAt", "-U", "fixture", "-d", "fixture", "-c", `SELECT coalesce(json_agg(json_build_object('name',rolname,'login',rolcanlogin,'super',rolsuper,'createdb',rolcreatedb,'createrole',rolcreaterole,'replication',rolreplication,'bypassrls',rolbypassrls) ORDER BY rolname),'[]') FROM pg_roles WHERE rolname LIKE '`+p.id+`%'`)
	if e != nil {
		return metadata, e
	}
	metadata.RolesSHA = fmt.Sprintf("%x", sha256.Sum256(roles))
	if e = os.WriteFile(filepath.Join(stage, "roles.json"), roles, 0600); e != nil {
		return metadata, e
	}
	archive, e := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return metadata, e
	}
	command := exec.CommandContext(ctx, hostTools+"/bin/docker", "exec", p.db, "pg_dump", "-U", "fixture", "-d", database, "-Fc", "-Z0")
	command.Env = buildEnv(h.config)
	command.Stdout = archive
	var stderr bytes.Buffer
	command.Stderr = &stderr
	e = command.Run()
	if e == nil {
		e = archive.Sync()
	}
	closeErr := archive.Close()
	if e == nil {
		e = closeErr
	}
	if e != nil {
		return metadata, fmt.Errorf("dump failed: %w: %s", e, stderr.Bytes())
	}
	f, e := os.Open(filepath.Join(stage, "data.dump"))
	if e != nil {
		return metadata, e
	}
	sum := sha256.New()
	metadata.ArchiveBytes, e = io.Copy(sum, f)
	f.Close()
	if e != nil {
		return metadata, e
	}
	metadata.ArchiveSHA = hex.EncodeToString(sum.Sum(nil))
	data, e := json.Marshal(metadata)
	if e != nil {
		return metadata, e
	}
	if e = os.WriteFile(filepath.Join(stage, "manifest.json"), data, 0600); e != nil {
		return metadata, e
	}
	if e = os.Rename(stage, destination); e != nil {
		return metadata, e
	}
	return metadata, nil
}

func verifyBackup(path string) (backupManifest, []backupRole, error) {
	var m backupManifest
	data, e := os.ReadFile(filepath.Join(path, "manifest.json"))
	if e != nil {
		return m, nil, e
	}
	if e = json.Unmarshal(data, &m); e != nil {
		return m, nil, e
	}
	if m.Version != 1 || m.Image != postgresImage || m.ServerVersion/10000 != 18 || m.DataDirectory != "/var/lib/postgresql/18/docker" {
		return m, nil, errors.New("unsupported backup recipe or image layout")
	}
	roles, e := os.ReadFile(filepath.Join(path, "roles.json"))
	if e != nil {
		return m, nil, e
	}
	if fmt.Sprintf("%x", sha256.Sum256(roles)) != m.RolesSHA {
		return m, nil, errors.New("role metadata checksum mismatch")
	}
	var parsed []backupRole
	if e = json.Unmarshal(roles, &parsed); e != nil {
		return m, nil, e
	}
	for _, r := range parsed {
		if r.Login || r.Super || r.CreateDB || r.CreateRole || r.Replication || r.BypassRLS {
			return m, nil, errors.New("fixture recipe only restores unprivileged NOLOGIN roles")
		}
	}
	f, e := os.Open(filepath.Join(path, "data.dump"))
	if e != nil {
		return m, nil, e
	}
	defer f.Close()
	sum := sha256.New()
	size, e := io.Copy(sum, f)
	if e != nil {
		return m, nil, e
	}
	if size != m.ArchiveBytes || hex.EncodeToString(sum.Sum(nil)) != m.ArchiveSHA {
		return m, nil, errors.New("archive checksum or length mismatch")
	}
	return m, parsed, nil
}

func transferBackup(source, destination string, cut bool) error {
	m, _, e := verifyBackup(source)
	if e != nil {
		return e
	}
	stage, e := os.MkdirTemp(filepath.Dir(destination), ".transfer-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(stage)
	for _, name := range []string{"manifest.json", "roles.json", "data.dump"} {
		in, e := os.Open(filepath.Join(source, name))
		if e != nil {
			return e
		}
		out, e := os.OpenFile(filepath.Join(stage, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if e != nil {
			in.Close()
			return e
		}
		var reader io.Reader = in
		if cut && name == "data.dump" {
			reader = io.LimitReader(in, m.ArchiveBytes/2)
		}
		_, e = io.Copy(out, reader)
		in.Close()
		if e == nil {
			e = out.Sync()
		}
		closeErr := out.Close()
		if e == nil {
			e = closeErr
		}
		if e != nil {
			return e
		}
	}
	if _, _, e = verifyBackup(stage); e != nil {
		return e
	}
	return os.Rename(stage, destination)
}

func restoreBackup(ctx context.Context, h *runtimeHarness, p runtimeProject, path string) error {
	m, roles, e := verifyBackup(path)
	if e != nil {
		return e
	}
	target, e := pgLayout(ctx, h, p)
	if e != nil {
		return e
	}
	if target.SourceVolume == m.SourceVolume || target.SourceContainer == m.SourceContainer {
		return errors.New("restore requires a fresh target volume and container")
	}
	if target.ServerVersion != m.ServerVersion {
		return errors.New("unsupported source/target tool version pair")
	}
	var sql strings.Builder
	for _, role := range roles {
		fmt.Fprintf(&sql, "CREATE ROLE %s NOLOGIN;\n", pgx.Identifier{role.Name}.Sanitize())
	}
	if out, e := pgCommand(ctx, h, p, strings.NewReader(sql.String()), "psql", "-X", "-U", "fixture", "-d", "fixture", "-v", "ON_ERROR_STOP=1"); e != nil {
		return fmt.Errorf("restore roles: %w: %s", e, out)
	}
	archive, e := os.Open(filepath.Join(path, "data.dump"))
	if e != nil {
		return e
	}
	defer archive.Close()
	out, e := pgCommand(ctx, h, p, archive, "pg_restore", "--exit-on-error", "--single-transaction", "-U", "fixture", "-d", "fixture")
	if e != nil {
		return fmt.Errorf("restore archive: %w: %s", e, out)
	}
	return nil
}

func TestPostgresRestore(t *testing.T) {
	h := newBuildHarness(t)
	source := h.project("source")
	h.deploySDK(t, source)
	waitPG(t, h, source)
	owner, reader := source.id+"owner", source.id+"reader"
	pgSQL(t, h, source, fmt.Sprintf(`CREATE ROLE %s NOLOGIN; CREATE ROLE %s NOLOGIN; CREATE EXTENSION hstore; CREATE SCHEMA app AUTHORIZATION %s; SET ROLE %s; CREATE TABLE app.records(id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,payload text NOT NULL CHECK(length(payload)>0)); INSERT INTO app.records(payload) SELECT repeat(md5(i::text),40) FROM generate_series(1,2000) i; GRANT USAGE ON SCHEMA app TO %s; GRANT SELECT ON app.records TO %s;`, owner, reader, owner, owner, reader, reader))
	hashSQL := `SELECT count(*)||':'||md5(string_agg(id::text||payload,'' ORDER BY id)) FROM app.records`
	original := pgSQL(t, h, source, hashSQL)
	sourceID := h.inspectID(t, source.db)
	unchanged := func() {
		if h.inspectID(t, source.db) != sourceID || pgSQL(t, h, source, hashSQL) != original {
			t.Fatal("backup/restore changed the source")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	bundle := filepath.Join(h.dir, "backup")
	manifest, e := createBackup(ctx, h, source, "fixture", bundle)
	if e != nil {
		t.Fatal(e)
	}
	if manifest.ArchiveBytes < 1<<20 {
		t.Fatal("fixture must exceed the disk-failure budget")
	}
	for _, name := range []string{"data.dump", "roles.json", "manifest.json"} {
		data, e := os.ReadFile(filepath.Join(bundle, name))
		if e != nil {
			t.Fatal(e)
		}
		if bytes.Contains(data, []byte(source.secret)) {
			t.Fatal("backup contains the runtime database credential")
		}
	}
	if e := restoreBackup(ctx, h, source, bundle); e == nil {
		t.Fatal("source volume accepted as restore target")
	}
	unchanged()
	if !t.Run("failed_dump_and_interrupted_transfer", func(t *testing.T) {
		path := filepath.Join(h.dir, "failed-backup")
		if _, e := createBackup(ctx, h, source, "missing_database", path); e == nil {
			t.Fatal("failed dump reported success")
		}
		if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("failed dump published a backup")
		}
		path = filepath.Join(h.dir, "interrupted-copy")
		if e := transferBackup(bundle, path, true); e == nil {
			t.Fatal("truncated transfer was accepted")
		}
		if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("truncated transfer published a backup")
		}
		unchanged()
	}) {
		return
	}
	if !t.Run("backup_disk_exhaustion", func(t *testing.T) {
		mount, e := os.MkdirTemp(h.dir, "bounded-")
		if e != nil {
			t.Fatal(e)
		}
		if out, e := exec.Command("mount", "-t", "tmpfs", "-o", "size=64k,mode=0700", "tmpfs", mount).CombinedOutput(); e != nil {
			t.Fatalf("mount bounded backup filesystem: %v: %s", e, out)
		}
		defer func() {
			if out, e := exec.Command("umount", mount).CombinedOutput(); e != nil {
				t.Errorf("unmount backup filesystem: %v: %s", e, out)
			}
		}()
		path := filepath.Join(mount, "backup")
		_, e = createBackup(ctx, h, source, "fixture", path)
		if e == nil || !strings.Contains(strings.ToLower(e.Error()), "no space left") {
			t.Fatalf("expected bounded backup ENOSPC: %v", e)
		}
		if _, e := os.Stat(path); !errors.Is(e, os.ErrNotExist) {
			t.Fatal("full disk published a backup")
		}
		unchanged()
	}) {
		return
	}
	copied := filepath.Join(h.dir, "transferred")
	if e := transferBackup(bundle, copied, false); e != nil {
		t.Fatal(e)
	}
	if !t.Run("restore_tool_failure_preserves_source", func(t *testing.T) {
		failed := h.project("failed")
		h.deploySDK(t, failed)
		waitPG(t, h, failed)
		pgSQL(t, h, failed, "CREATE SCHEMA app; CREATE TABLE keep_me(value int); INSERT INTO keep_me VALUES(7);")
		if e := restoreBackup(ctx, h, failed, copied); e == nil {
			t.Fatal("conflicting schema did not fail restore")
		}
		if pgSQL(t, h, failed, "SELECT value FROM keep_me") != "7" || pgSQL(t, h, failed, "SELECT count(*) FROM pg_tables WHERE schemaname='app'") != "0" {
			t.Fatal("failed restore did not roll back archive transaction")
		}
		unchanged()
	}) {
		return
	}
	t.Run("fresh_volume_restore", func(t *testing.T) {
		target := h.project("target")
		h.deploySDK(t, target)
		waitPG(t, h, target)
		if e := restoreBackup(ctx, h, target, copied); e != nil {
			t.Fatal(e)
		}
		if pgSQL(t, h, target, hashSQL) != original {
			t.Fatal("restored records differ")
		}
		if pgSQL(t, h, target, "SELECT tableowner FROM pg_tables WHERE schemaname='app' AND tablename='records'") != owner {
			t.Fatal("restored table owner differs")
		}
		if pgSQL(t, h, target, fmt.Sprintf("SELECT has_table_privilege('%s','app.records','SELECT')", reader)) != "t" {
			t.Fatal("restored reader grant missing")
		}
		if pgSQL(t, h, target, fmt.Sprintf("SELECT count(*) FROM pg_roles WHERE rolname IN ('%s','%s') AND NOT rolcanlogin AND NOT rolsuper", owner, reader)) != "2" {
			t.Fatal("restored role attributes differ")
		}
		if pgSQL(t, h, target, "SELECT count(*) FROM pg_extension WHERE extname='hstore'") != "1" {
			t.Fatal("restored extension missing")
		}
		if pgSQL(t, h, target, "SELECT count(*) FROM pg_constraint WHERE conrelid='app.records'::regclass AND contype IN ('p','c')") != "2" {
			t.Fatal("restored schema constraints differ")
		}
		if pgSQL(t, h, target, "INSERT INTO app.records(payload) VALUES('new write') RETURNING id") != "2001\nINSERT 0 1" {
			t.Fatal("restored identity sequence differs")
		}
		unchanged()
	})
}
