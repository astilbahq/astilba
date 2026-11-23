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
	return e.dataOutput(ctx, db, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.MaintenanceUser(), "-d", a.Postgres.Database, "-c", sql)
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
	helper, err := e.postgresHelper(ctx, "astilba-dump-"+run.String(), labels, c.ID, *a.Source.Image)
	if err != nil {
		return err
	}
	defer e.removeHelper(helper)
	if err = e.postgresManifest(ctx, helper, a, *a.Source.Image, m); err != nil {
		return err
	}
	dump, err := e.dumpSession(ctx, helper, a, m)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(stage, "data.dump"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = e.containerExec(ctx, dump, file, "pg_dump", "-Fc", "-Z0", "--no-password", "-U", a.Postgres.MaintenanceUser(), "-d", a.Postgres.Database)
	if err == nil {
		err = file.Sync()
	}
	err = errors.Join(err, file.Close())
	if err != nil {
		return err
	}
	return checksumFile(filepath.Join(stage, "data.dump"), m)
}

// dumpSession records in m the value of each of backup.DumpSettings in db's sessions as a's
// maintenance user, and returns db set so that a dump there runs with exactly those values, however
// the database's settings change meanwhile. A restore's verification dumps with them too.
func (e *Engine) dumpSession(ctx context.Context, db pgClient, a domain.Application, m *backup.Manifest) (pgClient, error) {
	raw, err := e.pgQuery(ctx, db, a, dumpSettingsSQL)
	if err != nil {
		return pgClient{}, err
	}
	if m.DumpSettings, err = readDumpSettings(raw); err != nil {
		return pgClient{}, err
	}
	env := slices.Clone(db.env)
	for i, v := range env {
		if strings.HasPrefix(v, "PGOPTIONS=") {
			env[i] = v + " -c " + strings.Join(dumpOptions(m.DumpSettings), " -c ")
			return pgClient{container: db.container, env: env}, nil
		}
	}
	return pgClient{}, errors.New("a dump's client has no session options")
}

// dumpSettingsSQL reads a session's value of each of backup.DumpSettings.
var dumpSettingsSQL = "SELECT pg_catalog.json_object_agg(name, setting) FROM pg_catalog.pg_settings WHERE name IN ('" + strings.Join(backup.DumpSettings, "', '") + "')" // inline-sql: reads a managed database's settings, not the platform database.

// readDumpSettings reads dumpSettingsSQL's result.
func readDumpSettings(raw []byte) (map[string]string, error) {
	var settings map[string]string
	if json.Unmarshal(raw, &settings) != nil || !backup.ValidDumpSettings(settings) {
		return nil, errors.New("the database's sessions print values under settings a backup can't record")
	}
	return settings, nil
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
	// source database is never passed through as arbitrary cluster administration. The
	// database user and the superuser maintenance connects as, the same role before the owner
	// model, are the restore's own.
	raw, err := e.pgQuery(ctx, db, a, `SELECT coalesce(json_agg(json_build_object('name',rolname,'unsupported',rolcanlogin OR rolsuper OR rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolconfig IS NOT NULL) ORDER BY rolname),'[]') FROM pg_roles WHERE NOT starts_with(rolname, 'pg_') AND rolname<>current_user AND rolname<>`+quoteLiteral(a.Postgres.Username))
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
	// A restore makes the database user a member of each of those roles, without the right to
	// grant it, so it can manage what they own. No other membership is the restore's to make.
	members, err := e.pgQuery(ctx, db, a, "SELECT count(*) FROM pg_auth_members m JOIN pg_roles r ON r.oid=m.roleid JOIN pg_roles u ON u.oid=m.member WHERE (NOT starts_with(r.rolname, 'pg_') OR NOT starts_with(u.rolname, 'pg_')) AND NOT (u.rolname="+quoteLiteral(a.Postgres.Username)+" AND NOT m.admin_option AND NOT r.rolcanlogin AND NOT r.rolsuper AND NOT starts_with(r.rolname, 'pg_') AND r.rolname<>current_user)")
	if err != nil || strings.TrimSpace(string(members)) != "0" {
		return errors.New("role memberships require a separate recipe")
	}
	// A restore runs the dump as the database user, which can't create what only a superuser may,
	// as a database from before the owner model could, so a dump holding any of it is never one a
	// restore can use. An extension's own objects come from the extension.
	if count, err := e.pgQuery(ctx, db, a, superuserObjectsSQL); err != nil || strings.TrimSpace(string(count)) != "0" {
		return errors.New("objects only a superuser can create, such as event triggers, C functions or operator classes, require a separate recipe")
	}
	if count, err := e.pgQuery(ctx, db, a, superuserPrivilegesSQL); err != nil || strings.TrimSpace(string(count)) != "0" {
		return errors.New("privileges changed on PostgreSQL's or an extension's objects, or granted by a role that doesn't own the object, require a separate recipe")
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

// superuserObjectsSQL counts the objects in a database, other than an extension's, that only a
// superuser can create: event triggers, aggregates whose state is internal, operator classes and families, casts without a function or
// between types none of which is the database's own, text search templates and parsers, access
// methods, foreign-data wrappers, procedural languages, publications of all tables or of schemas,
// subscriptions, and functions that are leakproof, in an untrusted language such as C, have a
// support function, or set a setting only an administrator may. A module's setting, or a name no
// module defines, is one of those: an ordinary role can set it in a function only while its module
// is loaded, which a restore's order decides. Objects initdb creates have lower OIDs than any
// other.
var superuserObjectsSQL = func() string {
	// ownAs is whether the object alias names in catalog is neither initdb's nor an extension's.
	ownAs := func(catalog, alias string) string {
		return alias + ".oid >= 16384 AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = '" + catalog + "'::regclass AND d.objid = " + alias + ".oid AND d.deptype = 'e')"
	}
	own := func(catalog string) string { return ownAs(catalog, "x") }
	// inline-sql: reads a managed database's catalog, not the platform database.
	counts := []string{"SELECT count(*) FROM pg_event_trigger"}
	for _, catalog := range []string{"pg_opclass", "pg_opfamily", "pg_ts_template", "pg_ts_parser", "pg_am", "pg_foreign_data_wrapper", "pg_language"} {
		// inline-sql: reads a managed database's catalog, not the platform database.
		counts = append(counts, "SELECT count(*) FROM "+catalog+" x WHERE "+own(catalog))
	}
	counts = append(counts,
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT count(*) FROM pg_cast x WHERE "+own("pg_cast")+" AND (x.castmethod = 'b' OR NOT EXISTS (SELECT 1 FROM pg_type t WHERE t.oid IN (x.castsource, x.casttarget) AND "+ownAs("pg_type", "t")+"))",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT count(*) FROM pg_publication x WHERE x.puballtables OR EXISTS (SELECT 1 FROM pg_publication_namespace n WHERE n.pnpubid = x.oid)",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT count(*) FROM pg_subscription x WHERE x.subdbid = (SELECT oid FROM pg_database WHERE datname = current_database())",
		// An aggregate whose state is internal is only a superuser's to create.
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT count(*) FROM pg_aggregate g JOIN pg_proc x ON x.oid = g.aggfnoid WHERE "+own("pg_proc")+" AND 'internal'::regtype IN (g.aggtranstype, g.aggmtranstype)",
		// An aggregate's own function, and a range type's constructors, are PostgreSQL's, in its
		// internal language, and come with the aggregate or the type. A function may run as a role
		// or session user its user chooses, which pg_settings doesn't list.
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT count(*) FROM pg_proc x JOIN pg_language l ON l.oid = x.prolang WHERE "+own("pg_proc")+" AND x.prokind <> 'a' AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = x.oid AND d.deptype = 'i') AND (NOT l.lanpltrusted OR x.proleakproof OR x.prosupport <> 0 OR EXISTS (SELECT 1 FROM unnest(x.proconfig) c WHERE lower(split_part(c, '=', 1)) NOT IN ('role', 'session_authorization') AND NOT EXISTS (SELECT 1 FROM pg_settings s WHERE lower(s.name) = lower(split_part(c, '=', 1)) AND s.context = 'user' AND position('.' IN s.name) = 0)))",
	)
	return "SELECT (" + strings.Join(counts, ") + (") + ")"
}()

// superuserPrivilegesSQL counts the privileges in a database that a restore, as its user, can't
// grant: those changed on PostgreSQL's own catalog or an extension's objects, which the dump names
// but the bootstrap superuser owns, and those whose grantor isn't the object's owner, the
// database's own included, which the dump grants as that grantor.
var superuserPrivilegesSQL = func() string {
	objects := []string{
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_class'::regclass AS catalog, x.oid, 0 AS column, x.relnamespace AS namespace, x.relowner AS owner, x.relacl AS acl, CASE x.relkind WHEN 'S' THEN 's' ELSE 'r' END AS kind FROM pg_class x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_class'::regclass, x.attrelid, x.attnum, r.relnamespace, r.relowner, x.attacl, 'c' FROM pg_attribute x JOIN pg_class r ON r.oid = x.attrelid",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_proc'::regclass, x.oid, 0, x.pronamespace, x.proowner, x.proacl, 'f' FROM pg_proc x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_namespace'::regclass, x.oid, 0, x.oid, x.nspowner, x.nspacl, 'n' FROM pg_namespace x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_type'::regclass, x.oid, 0, x.typnamespace, x.typowner, x.typacl, 'T' FROM pg_type x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_language'::regclass, x.oid, 0, NULL::oid, x.lanowner, x.lanacl, 'l' FROM pg_language x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_foreign_data_wrapper'::regclass, x.oid, 0, NULL::oid, x.fdwowner, x.fdwacl, 'F' FROM pg_foreign_data_wrapper x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_foreign_server'::regclass, x.oid, 0, NULL::oid, x.srvowner, x.srvacl, 'S' FROM pg_foreign_server x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_largeobject_metadata'::regclass, x.oid, 0, NULL::oid, x.lomowner, x.lomacl, 'L' FROM pg_largeobject_metadata x",
		// inline-sql: reads a managed database's catalog, not the platform database.
		"SELECT 'pg_database'::regclass, x.oid, 0, NULL::oid, x.datdba, x.datacl, 'd' FROM pg_database x WHERE x.datname = current_database()",
	}
	// inline-sql: reads a managed database's catalog, not the platform database.
	base := "coalesce((SELECT i.initprivs FROM pg_init_privs i WHERE i.classoid = o.catalog AND i.objoid = o.oid AND i.objsubid = o.column), acldefault(o.kind::\"char\", o.owner))"
	// inline-sql: reads a managed database's catalog, not the platform database.
	return "SELECT count(*) FROM (" + strings.Join(objects, " UNION ALL ") + ") o WHERE o.acl IS NOT NULL AND (EXISTS (SELECT 1 FROM aclexplode(o.acl) e WHERE e.grantor <> o.owner) OR ((o.namespace = 'pg_catalog'::regnamespace OR EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = o.catalog AND d.objid = o.oid AND d.deptype = 'e')) AND NOT (o.acl @> " + base + " AND o.acl <@ " + base + ")))"
}()
