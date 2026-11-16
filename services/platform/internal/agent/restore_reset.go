package agent

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// A restored cluster ran SQL from its dump as the deployment's superuser, so before anything may
// start the deployment's database on it, the restore holds it to what a dump of one database can
// legitimately leave: the restore refuses anything more, and puts back the settings files the
// pinned image wrote when it created the cluster. It never repairs what it finds. Its files are
// checked while no server runs, and its catalogs by a server started after that check, so neither
// sees what a server the dump ran in kept in memory rather than on disk.

// clusterRefusal is something a restored cluster holds that a dump of one database can't leave.
type clusterRefusal string

func (r clusterRefusal) Error() string { return string(r) }

// clusterReason is what err's refusal found.
func clusterReason(err error) string {
	var r clusterRefusal
	errors.As(err, &r)
	return string(r)
}

// clusterSettingsFiles are the files a cluster's server reads its configuration and client
// authentication from, which SQL run as a superuser can change.
var clusterSettingsFiles = []string{"postgresql.conf", "postgresql.auto.conf", "pg_hba.conf", "pg_ident.conf"}

// postgresDataDirectory is where a pinned image keeps its cluster, and the volume path to it from
// where the image mounts its volume.
func postgresDataDirectory(image string) (absolute, relative string, err error) {
	switch image {
	case domain.PostgresImage:
		return "/var/lib/postgresql/18/docker", "18/docker", nil
	case domain.Postgres17Image:
		return "/var/lib/postgresql/data", "", nil
	}
	return "", "", errUnsupportedPostgresImage
}

// clusterSettings reads the settings files of a cluster the pinned image has just created, before
// any SQL from a dump runs in it.
func (e *Engine) clusterSettings(ctx context.Context, id, image string) (map[string][]byte, error) {
	dir, _, err := postgresDataDirectory(image)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, name := range clusterSettingsFiles {
		if out[name], err = e.dataOutput(ctx, pgClient{container: id}, "cat", dir+"/"+name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// restoredCluster is what checkRestoredCluster reads from a restored cluster's shared catalogs.
type restoredCluster struct {
	Roles []struct {
		Name  string `json:"name"`
		Login bool   `json:"login"`
		Super bool   `json:"super"`
		Other bool   `json:"other"`
	} `json:"roles"`
	Memberships int      `json:"memberships"`
	Databases   []string `json:"databases"`
	Connectable []string `json:"connectable"`
	Settings    []struct {
		Database string `json:"database"`
		Name     string `json:"name"`
		Context  string `json:"context"`
	} `json:"settings"`
	Subscriptions int      `json:"subscriptions"`
	Slots         int      `json:"slots"`
	Prepared      int      `json:"prepared"`
	Tablespaces   []string `json:"tablespaces"`
}

// restoredDatabase is what checkRestoredCluster reads from each database a client can connect to.
type restoredDatabase struct {
	EventTriggers int      `json:"eventTriggers"`
	Extensions    []string `json:"extensions"`
	Native        int      `json:"native"`
}

// Each check runs with the catalog first on its search path, so nothing the dump created can
// stand in for a catalog object.
const (
	restoredClusterSQL = `SET search_path = pg_catalog, pg_temp;
SELECT json_build_object(
 'roles', (SELECT coalesce(json_agg(json_build_object('name', rolname, 'login', rolcanlogin, 'super', rolsuper, 'other', rolcreatedb OR rolcreaterole OR rolreplication OR rolbypassrls OR rolconnlimit <> -1 OR rolvaliduntil IS NOT NULL) ORDER BY rolname), '[]') FROM pg_authid WHERE rolname !~ '^pg_'),
 'memberships', (SELECT count(*) FROM pg_auth_members m JOIN pg_authid r ON r.oid = m.roleid JOIN pg_authid u ON u.oid = m.member WHERE r.rolname !~ '^pg_' OR u.rolname !~ '^pg_'),
 'databases', (SELECT coalesce(json_agg(datname ORDER BY datname), '[]') FROM pg_database),
 'connectable', (SELECT coalesce(json_agg(datname ORDER BY datname), '[]') FROM pg_database WHERE datallowconn),
 'settings', (SELECT coalesce(json_agg(json_build_object('database', coalesce(d.datname, ''), 'name', split_part(c.v, '=', 1), 'context', coalesce(g.context, ''))), '[]') FROM pg_db_role_setting s LEFT JOIN pg_database d ON d.oid = s.setdatabase CROSS JOIN LATERAL unnest(s.setconfig) c(v) LEFT JOIN pg_settings g ON g.name = split_part(c.v, '=', 1)),
 'subscriptions', (SELECT count(*) FROM pg_subscription),
 'slots', (SELECT count(*) FROM pg_replication_slots),
 'prepared', (SELECT count(*) FROM pg_prepared_xacts),
 'tablespaces', (SELECT coalesce(json_agg(spcname ORDER BY spcname), '[]') FROM pg_tablespace))`
	restoredDatabaseSQL = `SET search_path = pg_catalog, pg_temp;
SELECT json_build_object(
 'eventTriggers', (SELECT count(*) FROM pg_event_trigger),
 'extensions', (SELECT coalesce(json_agg(extname ORDER BY extname), '[]') FROM pg_extension),
 'native', (SELECT count(*) FROM pg_proc p JOIN pg_language l ON l.oid = p.prolang WHERE l.lanname IN ('c', 'internal') AND p.pronamespace <> 'pg_catalog'::regnamespace AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e')))`
)

// checkRestoredCluster refuses a restored cluster, running in container id with event triggers
// off for its clients, that holds anything a dump of m's database can't leave in the cluster the
// restore created for it: roles, settings, databases and cluster objects beyond the manifest's,
// and in any database a client can reach, event triggers, extensions beyond the manifest's, or
// native functions no extension owns.
func (e *Engine) checkRestoredCluster(ctx context.Context, id string, m backup.Manifest) error {
	query := func(database, sql string, v any) error {
		raw, err := e.dataOutput(ctx, pgClient{container: id}, "psql", "-XAqt", "-v", "ON_ERROR_STOP=1", "-U", m.Username, "-d", database, "-c", sql)
		if err != nil {
			return err
		}
		if json.Unmarshal(bytes.TrimSpace(raw), v) != nil {
			return errors.New("restored cluster is unreadable")
		}
		return nil
	}
	var c restoredCluster
	if err := query(m.Database, restoredClusterSQL, &c); err != nil {
		return err
	}
	roles := map[string]bool{}
	for _, r := range m.Roles {
		roles[r.Name] = true
	}
	for _, r := range c.Roles {
		switch {
		case r.Name == m.Username:
			if !r.Login || !r.Super {
				return clusterRefusal("a changed deployment role")
			}
		case !roles[r.Name]:
			return clusterRefusal("a role the backup doesn't hold")
		case r.Login || r.Super || r.Other:
			return clusterRefusal("a role with attributes the backup doesn't give it")
		default:
			delete(roles, r.Name)
		}
	}
	if len(roles) > 0 {
		return clusterRefusal("too few roles")
	}
	if c.Memberships != 0 {
		return clusterRefusal("a role membership")
	}
	databases := []string{"postgres", "template0", "template1"}
	connectable := []string{"postgres", "template1"}
	if !slices.Contains(databases, m.Database) {
		databases = append(databases, m.Database)
		connectable = append(connectable, m.Database)
	}
	slices.Sort(databases)
	slices.Sort(connectable)
	if !slices.Equal(c.Databases, databases) || !slices.Equal(c.Connectable, connectable) {
		return clusterRefusal("a database the backup doesn't hold")
	}
	for _, s := range c.Settings {
		// Settings in the database, its own or a role's, are part of its dump, but only those any
		// client may set: one that loads code or reaches past the database is the cluster's to
		// choose.
		if s.Database != m.Database || s.Context != "user" && (s.Context != "" || !strings.Contains(s.Name, ".")) {
			return clusterRefusal("a setting only an administrator may choose")
		}
	}
	if c.Subscriptions != 0 || c.Slots != 0 || c.Prepared != 0 || !slices.Equal(c.Tablespaces, []string{"pg_default", "pg_global"}) {
		return clusterRefusal("a subscription, replication slot, prepared transaction or tablespace")
	}
	for _, database := range connectable {
		var d restoredDatabase
		if err := query(database, restoredDatabaseSQL, &d); err != nil {
			return err
		}
		allowed := []string{"plpgsql"}
		if database == m.Database {
			allowed = m.Extensions
		}
		switch {
		case d.EventTriggers != 0:
			return clusterRefusal("an event trigger")
		case slices.ContainsFunc(d.Extensions, func(x string) bool { return !slices.Contains(allowed, x) }):
			return clusterRefusal("an extension the backup doesn't hold")
		case d.Native != 0:
			return clusterRefusal("a native function no extension provides")
		}
	}
	return nil
}

// resetClusterPassword gives the deployment's role its password again, which a dump may have
// changed. The password reaches psql only on its standard input.
func (e *Engine) resetClusterPassword(ctx context.Context, id string, m backup.Manifest, password string) error {
	sql := "SET search_path = pg_catalog, pg_temp;\nALTER ROLE " + quoteIdentifier(m.Username) + " PASSWORD '" + strings.ReplaceAll(password, "'", "''") + "';\n"
	return e.dataTool(ctx, strings.NewReader(sql), io.Discard, "exec", "-i", id, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", m.Username, "-d", m.Database, "-f", "-")
}

var (
	relationFile = regexp.MustCompile(`^[0-9]+(_(fsm|vm|init))?(\.[0-9]+)?$`)
	segmentFile  = regexp.MustCompile(`^[0-9A-F]{4,15}$`)
	walFile      = regexp.MustCompile(`^[0-9A-F]{24}$`)
	numeric      = regexp.MustCompile(`^[0-9]+$`)
)

// clusterDirectories are the directories a stopped cluster the pinned images create holds, and
// what each may hold: nil for nothing.
var clusterDirectories = map[string]func(string) bool{
	"": func(n string) bool {
		return slices.Contains([]string{"PG_VERSION", "postmaster.opts"}, n) || slices.Contains(clusterSettingsFiles, n)
	},
	"base": nil,
	"global": func(n string) bool {
		return relationFile.MatchString(n) || n == "pg_control" || n == "pg_filenode.map" || n == "pg_internal.init"
	},
	"pg_commit_ts":          segmentFile.MatchString,
	"pg_dynshmem":           nil,
	"pg_logical":            func(n string) bool { return n == "replorigin_checkpoint" },
	"pg_logical/mappings":   nil,
	"pg_logical/snapshots":  nil,
	"pg_multixact":          nil,
	"pg_multixact/members":  segmentFile.MatchString,
	"pg_multixact/offsets":  segmentFile.MatchString,
	"pg_notify":             segmentFile.MatchString,
	"pg_replslot":           nil,
	"pg_serial":             segmentFile.MatchString,
	"pg_snapshots":          nil,
	"pg_stat":               func(n string) bool { return n == "pgstat.stat" },
	"pg_stat_tmp":           nil,
	"pg_subtrans":           segmentFile.MatchString,
	"pg_tblspc":             nil,
	"pg_twophase":           nil,
	"pg_wal":                walFile.MatchString,
	"pg_wal/archive_status": nil,
	"pg_wal/summaries":      nil,
	"pg_xact":               segmentFile.MatchString,
}

// clusterEntry reports whether path, relative to the volume a stopped restored cluster is on, is
// a directory or file that cluster may hold. data is the cluster's path in the volume.
func clusterEntry(data, path string, directory bool) bool {
	if data != "" {
		// Only the path to the cluster may sit beside it in the volume.
		if directory && (data == path || strings.HasPrefix(data, path+"/")) {
			return true
		}
		rest, ok := strings.CutPrefix(path, data+"/")
		if !ok {
			return false
		}
		path = rest
	}
	parent, name := "", path
	if i := strings.LastIndexByte(path, '/'); i >= 0 {
		parent, name = path[:i], path[i+1:]
	}
	// A database's directory holds its relations and the files that describe them.
	if base, ok := strings.CutPrefix(parent, "base/"); ok {
		return !directory && numeric.MatchString(base) && (relationFile.MatchString(name) || name == "PG_VERSION" || name == "pg_filenode.map" || name == "pg_internal.init")
	}
	if directory {
		_, known := clusterDirectories[path]
		return known || parent == "base" && numeric.MatchString(name)
	}
	allowed, known := clusterDirectories[parent]
	return known && allowed != nil && allowed(name)
}

// clusterListing checks each NUL-terminated path it is given, relative to the volume, and stops
// at the first it refuses or once it has read more than its limit.
type clusterListing struct {
	accept  func(path string) bool
	left    int
	partial []byte
	refused bool
}

func (l *clusterListing) Write(p []byte) (int, error) {
	if l.left -= len(p); l.left < 0 {
		return 0, clusterRefusal("more files than a cluster holds")
	}
	l.partial = append(l.partial, p...)
	for {
		i := bytes.IndexByte(l.partial, 0)
		if i < 0 {
			return len(p), nil
		}
		path := strings.TrimPrefix(string(l.partial[:i]), "./")
		l.partial = l.partial[i+1:]
		if !l.accept(path) {
			l.refused = true
			return 0, clusterRefusal("a file a cluster doesn't hold")
		}
	}
}

// resetClusterFiles checks a stopped restored cluster's files from a container that runs nothing
// from the volume, then puts back the settings files the pinned image wrote when it created the
// cluster. Each is removed and written again, so no other path shares it, and read back. The
// relation cache files a server trusts at startup go too; the next server builds them again from
// the catalogs.
func (e *Engine) resetClusterFiles(ctx context.Context, p backup.Policy, run, target uuid.UUID, image string, settings map[string][]byte) error {
	dir, data, err := postgresDataDirectory(image)
	if err != nil {
		return err
	}
	root := domain.PostgresMount(image)
	labels := visualLabels(p.Plan, target)
	labels[labelKind] = "backup-restore-v1"
	labels["com.astilba.run"] = run.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-reset-" + run.String(), Config: &container.Config{Image: image, User: "postgres", Entrypoint: []string{"sleep", "3600"}, WorkingDir: root, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(target), Target: root}}, Resources: container.Resources{Memory: 128 << 20, PidsLimit: ptr(int64(32))}}})
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
	helper := pgClient{container: created.ID}
	none := func(string) bool { return false }
	for _, check := range []struct {
		accept func(string) bool
		find   []string
	}{
		{func(path string) bool { return clusterEntry(data, path, true) }, []string{"-type", "d"}},
		{func(path string) bool { return clusterEntry(data, path, false) }, []string{"-type", "f"}},
		// Anything else, and a file another path also names, are never a cluster's.
		{none, []string{"!", "-type", "d", "!", "-type", "f"}},
		{none, []string{"-type", "f", "-links", "+1"}},
	} {
		listing := &clusterListing{accept: check.accept, left: 64 << 20}
		err = e.containerExec(ctx, helper, listing, append(append([]string{"find", ".", "-mindepth", "1"}, check.find...), "-print0")...)
		if listing.refused || listing.left < 0 {
			return clusterRefusal("a file a cluster doesn't hold")
		}
		if err != nil {
			return err
		}
		if len(listing.partial) > 0 {
			return errors.New("restored cluster listing is incomplete")
		}
	}
	for _, name := range clusterSettingsFiles {
		path := dir + "/" + name
		if err = e.dataTool(ctx, bytes.NewReader(settings[name]), io.Discard, "exec", "-i", created.ID, "sh", "-c", `rm -f -- "$1" && umask 077 && cat > "$1"`, "sh", path); err != nil {
			return err
		}
		written, err := e.dataOutput(ctx, helper, "cat", path)
		if err != nil {
			return err
		}
		if !bytes.Equal(written, settings[name]) {
			return errors.New("restored cluster settings were not reset")
		}
	}
	return e.containerExec(ctx, helper, io.Discard, "find", ".", "-type", "f", "-name", "pg_internal.init", "-delete")
}
