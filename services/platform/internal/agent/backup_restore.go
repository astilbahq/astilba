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
	"github.com/moby/moby/client"
)

// restoreSnapshot restores a snapshot from this server's own policy catalogue into a new volume,
// once the snapshot's manifest matches the one its upload recorded.
// It returns the database settings it left out of the restored database.
func (e *Engine) restoreSnapshot(ctx context.Context, s *scheduleState, r *backup.Run) ([]clusterSetting, error) {
	in := &s.Installation
	if in.Policy.Method == "container-job" || !backup.SnapshotID(r.Snapshot) || r.TargetID == uuid.Nil {
		return nil, errors.New("invalid restore")
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
		return nil, errors.New("snapshot not in successful policy catalogue")
	}
	m := *original.Manifest
	if m.Recipe != backup.Recipe || m.PolicyID != in.Policy.ID || m.ResourceID != in.Policy.ResourceID || m.Method != in.Policy.Method || m.Bytes <= 0 || m.Bytes > 1<<40 || !backup.SnapshotID(m.SHA256) {
		return nil, errors.New("invalid backup manifest")
	}
	// A backup the database's release cannot restore, such as one from another major, is refused
	// with its reason before anything on the host is touched.
	if m.Method == "postgres-logical" {
		if _, err := backup.RestoreRelease(in.Policy, m); err != nil {
			return nil, restoreRefusal(err.Error())
		}
	}
	if err := e.checkRestoreOwnership(ctx, in.Policy); err != nil {
		return nil, err
	}
	credential := ""
	if m.Method == "postgres-logical" {
		var err error
		if credential, err = e.databaseCredential(ctx, in.Policy); err != nil {
			return nil, restoreRefusal("this server's database is not running as the deployment describes")
		}
	}
	if err := e.beginEffects(ctx, s, r); err != nil {
		return nil, err
	}
	if err := e.prepareRepository(ctx, in, false); err != nil {
		return nil, err
	}
	stored, archive, err := e.snapshotManifest(ctx, *in, r.Snapshot)
	if err != nil {
		return nil, err
	}
	if backup.Hash(stored) != original.ManifestHash {
		return nil, errors.New("snapshot manifest differs from the uploaded record")
	}
	cleared, release, err := e.restoreArchive(ctx, *in, r.ID, r.TargetID, r.Snapshot, archive, stored, credential, func() error { return nil })
	if err != nil {
		return nil, err
	}
	r.Manifest = &stored
	r.ManifestHash = backup.Hash(stored)
	r.RestoreImage = release.Image
	r.RestoreRoles = restoredRoles(release)
	r.Verified = true
	return cleared, nil
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
// with the archive before it returns. A logical restore runs in the database's own release, which
// it returns, and the target is recorded as written by that release.
func (e *Engine) restoreArchive(ctx context.Context, in backup.Installation, run, target uuid.UUID, snapshot, archive string, m backup.Manifest, credential string, begin func() error) ([]clusterSetting, domain.PostgresRelease, error) {
	// Reserve headroom for the downloaded archive and expanded database, retaining
	// both old and new volumes. Refuse unknown capacity rather than assuming room.
	if freeBytes(e.Journal.dir) < m.Bytes*2+256<<20 || dockerFreeBytes(ctx, e.Docker) < m.Bytes*4+256<<20 {
		return nil, domain.PostgresRelease{}, restoreRefusal("insufficient restore capacity")
	}
	var release domain.PostgresRelease
	if m.Method == "postgres-logical" {
		if _, err := postgresRecipe(in.Policy, m); err != nil {
			return nil, domain.PostgresRelease{}, restoreRefusal("the snapshot's database is not the one this deployment runs")
		}
		var err error
		if release, err = backup.RestoreRelease(in.Policy, m); err != nil {
			return nil, domain.PostgresRelease{}, restoreRefusal(err.Error())
		}
		if _, err = restoreRoles(m); err != nil {
			return nil, domain.PostgresRelease{}, err
		}
		if credential == "" {
			return nil, domain.PostgresRelease{}, restoreRefusal("this server's database is not running as the deployment describes")
		}
	}
	// A volume the deployment declares would be adopted by it, unreviewed, if a restore created it.
	if slices.ContainsFunc(in.Policy.Plan.Specification.Volumes, func(v domain.Volume) bool { return v.ID == target }) {
		return nil, domain.PostgresRelease{}, restoreRefusal("restore target must be a new volume")
	}
	if _, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(target), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		return nil, domain.PostgresRelease{}, restoreRefusal("restore target must be absent")
	}
	stage, err := e.Journal.scratchDir(scratchRestoreStage)
	if err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	defer os.RemoveAll(stage)
	file := filepath.Join(stage, archiveName(m.Method))
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	capped := &cappedWriter{w: f, left: m.Bytes}
	err = errors.Join(e.resticTo(ctx, in, stage, capped, "dump", snapshot, archive), f.Close())
	if capped.over {
		return nil, domain.PostgresRelease{}, restoreRefusal("snapshot archive is larger than its manifest says")
	}
	if err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	checked := m
	if err = checksumFile(file, &checked); err != nil || checked.Bytes != m.Bytes || checked.SHA256 != m.SHA256 {
		return nil, domain.PostgresRelease{}, restoreRefusal("snapshot archive does not match its manifest")
	}
	if err = begin(); err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	if _, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(target), Driver: "local", Labels: restoreTargetLabels(in.Policy.Plan, target, release)}); err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	if _, err = e.volumePath(ctx, in.Policy, target); err != nil {
		return nil, domain.PostgresRelease{}, err
	}
	var restored restoredPostgres
	if m.Method == "postgres-logical" {
		restored, err = e.restorePostgres(ctx, in.Policy, run, target, stage, m, release, credential)
	} else {
		err = e.restoreFiles(ctx, in.Policy, run, target, stage, stage)
	}
	if err != nil || ctx.Err() != nil {
		return nil, domain.PostgresRelease{}, errors.Join(errors.New("fresh restore failed"), err, ctx.Err())
	}
	if release.Image != "" {
		if err = e.Journal.saveRestoredPostgres(target, release, restored.verifier); err != nil {
			return nil, domain.PostgresRelease{}, err
		}
	}
	return restored.left, release, nil
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

// quoteLiteral quotes v as a string constant, which standard_conforming_strings, on in every pinned
// release, reads without backslash escapes.
func quoteLiteral(v string) string { return `'` + strings.ReplaceAll(v, `'`, `''`) + `'` }

// postgresRecipe is the policy's database application, if m's archive holds the database and owner
// the deployment names. backup.RestoreRelease decides whether, and in which release, it restores.
func postgresRecipe(p backup.Policy, m backup.Manifest) (domain.Application, error) {
	a, err := sourceApp(p)
	if err != nil || a.Postgres == nil || m.Database != a.Postgres.Database || m.Username != a.Postgres.Username {
		return domain.Application{}, errors.New("unsupported restore recipe")
	}
	return a, nil
}

// restoredPostgres is what a restore leaves in the cluster it created: the database's settings it
// left out, and the password verifier its user has.
type restoredPostgres struct {
	left     []clusterSetting
	verifier string
}

// restoredRoles is the role model of the cluster a restore in release created: the owner model,
// for every logical restore.
func restoredRoles(release domain.PostgresRelease) string {
	if release.Image == "" {
		return ""
	}
	return domain.PostgresOwner
}

// restoreRoles are the manifest's roles a restore creates beside its database user. A dump of one
// database never makes a role PostgreSQL's own, the user or the superuser.
func restoreRoles(m backup.Manifest) ([]string, error) {
	if reservedRole(m.Username) || m.Database == "template0" || m.Database == "template1" {
		return nil, restoreRefusal("the backup's database user or database has a name PostgreSQL keeps for its own, so it can't be restored with an ordinary database user")
	}
	var roles []string
	for _, role := range m.Roles {
		if role.Name == m.Username || reservedRole(role.Name) || role.Name == "" || len(role.Name) > 63 || strings.ContainsRune(role.Name, 0) || slices.Contains(roles, role.Name) {
			return nil, errors.New("invalid role")
		}
		roles = append(roles, role.Name)
	}
	return roles, nil
}

func reservedRole(name string) bool {
	return name == domain.PostgresSuperuser || strings.HasPrefix(name, "pg_")
}

// restorePostgres restores dir's data.dump, which m describes, into a fresh cluster of release on
// the target volume, in the owner model, whatever model the archive's own server had: the
// cluster's superuser is postgres, without a password, and m's user is an ordinary role that owns
// the database, with credential, a password or the verifier PostgreSQL stored for one. A restore
// runs in the release backup.RestoreRelease chose, the database's own; the supported upgrade
// restores a PostgreSQL 17 archive into its PostgreSQL 18 target. A dump may come from a server
// that was lost or compromised, so each container that touches the cluster runs as postgres with
// no capabilities or network and mounts only its target volume, and the dump runs only as m's
// user, as restoreAsOwner describes. Once the dump's server has stopped, the cluster is held to what
// a dump of one database can leave before a fresh server verifies it.
func (e *Engine) restorePostgres(ctx context.Context, p backup.Policy, run, target uuid.UUID, dir string, m backup.Manifest, release domain.PostgresRelease, credential string) (restoredPostgres, error) {
	if credential == "" {
		return restoredPostgres{}, errors.New("database credential unavailable")
	}
	roles, err := restoreRoles(m)
	if err != nil {
		return restoredPostgres{}, err
	}
	// The database's container may have been removed, and its image with it.
	if err = e.ensureImage(ctx, release.Image); err != nil {
		return restoredPostgres{}, err
	}
	env, err := bootstrapEnv()
	if err != nil {
		return restoredPostgres{}, err
	}
	var b clusterBaseline
	var out restoredPostgres
	if err = e.restoreServer(ctx, p, run, target, isolatedCluster{name: "astilba-restore-", release: release, env: env}, func(id string) (err error) {
		archive, err := e.readArchive(ctx, id, dir, m, roles)
		if err != nil {
			return err
		}
		if err = e.superuserSQL(ctx, id, "template1", ownerClusterSQL(m.Username, m.Database, roles, archive.owner)); err != nil {
			return err
		}
		// Read before the dump runs anything.
		if b, err = e.baseline(ctx, id, release.Image, m); err != nil {
			return err
		}
		out.left, err = e.restoreAsOwner(ctx, id, archive, m, roles, b)
		return err
	}); err != nil {
		return restoredPostgres{}, err
	}
	if err = e.resetClusterFiles(ctx, p, run, target, release.Image, b.files); err != nil {
		return restoredPostgres{}, err
	}
	// A server that has just started reads the cluster as the deployment's database will, not
	// through what the dump's server kept in memory. No client of it runs event triggers, and
	// autovacuum, which runs the dump's index functions as their tables' owners, stays off.
	verify := isolatedCluster{name: "astilba-verify-", release: release, options: []string{"event_triggers=off"}, cmd: []string{"postgres", "-c", "autovacuum=off"}}
	if err = e.restoreServer(ctx, p, run, target, verify, func(id string) (err error) {
		if err = e.checkRestoredCluster(ctx, id, m, b); err != nil {
			return err
		}
		// The restored data is compared with the archive before anything in it changes.
		if err = e.verifyRestoredDump(ctx, id, dir, m, release, out.left, verify.options); err != nil {
			return err
		}
		if err = e.superuserSQL(ctx, id, "template1", passwordSQL(m.Username, credential)); err != nil {
			return err
		}
		out.verifier, err = e.clusterVerifier(ctx, id, m.Username)
		return err
	}); err != nil {
		return restoredPostgres{}, err
	}
	// The dump's code can still have run while that server did, so its files are checked and
	// reset again.
	return out, e.resetClusterFiles(ctx, p, run, target, release.Image, b.files)
}

// restoreServer runs c's server on target in its own container, isolated as restorePostgres
// describes and named c's name and then the run, calls use once it accepts connections, and then
// stops it, which must leave the cluster shut down cleanly. The container is gone when it returns.
func (e *Engine) restoreServer(ctx context.Context, p backup.Policy, run, target uuid.UUID, c isolatedCluster, use func(id string) error) error {
	c.labels = visualLabels(p.Plan, target)
	c.labels[labelKind] = "backup-restore-v1"
	c.labels["com.astilba.run"] = run.String()
	c.name += run.String()
	c.volume = target
	return e.runIsolated(ctx, c, use)
}

// verifyRestoredDump re-dumps the restored database with release's tools, as the superuser with
// options and the settings the archive's dump ran with, and compares it with dir's archive, all
// definitions and data, owners, grants and sequence contents, and the database's own statements,
// less the settings the restore left out.
func (e *Engine) verifyRestoredDump(ctx context.Context, id, dir string, m backup.Manifest, release domain.PostgresRelease, left []clusterSetting, options []string) error {
	settings := m.DumpSettings
	if settings == nil {
		// A dump from before dumps recorded their settings ran as the database's user, whose
		// settings in the database the restore kept, if the user could choose them.
		out := &boundedOutput{limit: 65536, stopOnLimit: true}
		err := e.dataTool(ctx, nil, out, "exec", "-e", "PGOPTIONS="+identityOptions, id, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", m.Username, "-d", m.Database, "-c", dumpSettingsSQL)
		if err != nil {
			return err
		}
		if settings, err = readDumpSettings(out.b.Bytes()); err != nil {
			return err
		}
	}
	verify, err := e.Journal.scratchFile(scratchRestoreVerification)
	if err != nil {
		return err
	}
	defer os.Remove(verify.Name())
	dumpArgs := []string{"exec", "-e", pgOptions(append(slices.Clone(options), dumpOptions(settings)...)...), id, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", domain.PostgresSuperuser, "-d", m.Database}
	// The upgrade dumped with every identifier quoted, so the comparison dump must too.
	if archive, _ := domain.PostgresReleaseOf(m.Image); archive.Major() != release.Major() {
		dumpArgs = append(dumpArgs, "--quote-all-identifiers")
	}
	err = e.dataTool(ctx, nil, verify, dumpArgs...)
	err = errors.Join(err, verify.Close())
	if err != nil {
		return err
	}
	// The pinned PostgreSQL tools can use a fixed restrict key for deterministic
	// SQL. Hash complete streams, including COPY records, without retaining them. Without
	// --create, a rendering leaves out the database's own entries, which are compared apart.
	originalSQL := sha256.New()
	restoredSQL := sha256.New()
	var statements [2][]string
	for i, item := range []struct {
		path string
		out  io.Writer
	}{{filepath.Join(dir, "data.dump"), originalSQL}, {verify.Name(), restoredSQL}} {
		// An archive an earlier release made names other versions in its preamble.
		if err = e.archiveTool(ctx, id, item.path, &dumpVersionHeader{out: item.out}, "pg_restore", "--file=-", "--restrict-key=AstilbaRestoreVerification"); err != nil {
			return err
		}
		listing := &boundedOutput{limit: 64 << 20, stopOnLimit: true}
		if err = e.archiveTool(ctx, id, item.path, listing, "pg_restore", "-l", "--create"); err != nil {
			return err
		}
		entries, err := readTOC(listing.b.Bytes())
		if err != nil {
			return err
		}
		script, err := e.renderDatabase(ctx, id, item.path, entries)
		if err != nil {
			return err
		}
		parsed, err := databaseStatements(script, m)
		if err != nil {
			return err
		}
		for _, s := range parsed {
			if s.kind != statementSetting || !slices.Contains(left, s.setting) {
				statements[i] = append(statements[i], s.text)
			}
		}
	}
	if !bytes.Equal(originalSQL.Sum(nil), restoredSQL.Sum(nil)) {
		return errors.New("restored database differs from archived records or schema")
	}
	if statements[0][0] != statements[1][0] {
		return clusterRefusal("a database encoding or locale other than this server's")
	}
	if !slices.Equal(statements[0], statements[1]) {
		return errors.New("restored database's settings or privileges differ from the archive's")
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
