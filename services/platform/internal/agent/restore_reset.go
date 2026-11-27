package agent

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json/v2"
	"errors"
	"fmt"
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

// A restored cluster ran SQL from its dump as the deployment's database user, an ordinary role, so
// before anything may start the deployment's database on it, the restore holds it to what a dump
// of one database can legitimately leave: it refuses anything more, and puts back the settings
// files the pinned image wrote when it created the cluster. It never repairs what it finds. Its
// files are checked while no server runs, and its catalogs by a server started after that check,
// so neither sees what a server the dump ran in kept in memory rather than on disk.
//
// The dump never ran as a superuser, so it could not rewrite the catalogs the check reads, write
// the cluster's files or change its roles. The checks hold the cluster to that all the same. Data
// from a compromised server is still that server's data: the restore keeps it from reaching
// beyond the database, not from being wrong.

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

// postgresDataDirectory is where a pinned release's image keeps its cluster, and the volume path to
// it from where the image mounts its volume.
func postgresDataDirectory(image string) (absolute, relative string, err error) {
	release, ok := domain.PostgresReleaseOf(image)
	if !ok {
		return "", "", errUnsupportedPostgresImage
	}
	relative = strings.TrimPrefix(strings.TrimPrefix(release.DataDirectory, release.Mount), "/")
	return release.DataDirectory, relative, nil
}

// clusterBaseline is what a cluster the pinned image has just created holds, in the owner model
// with the manifest's roles, before any SQL from a dump runs in it.
type clusterBaseline struct {
	// files are its settings files: what the deployment's database would have had if it had
	// created the cluster itself.
	files map[string][]byte
	// roles is its roles and their memberships, which a dump of one database never changes.
	roles [][][]string
	// contexts is who may choose each setting the server lists.
	contexts map[string]string
}

// clusterRoles are the catalogs that hold a cluster's roles, with their passwords, and memberships.
var clusterRoles = []string{"pg_authid", "pg_auth_members"}

// baseline reads a cluster the restoring image has just created, running in container id.
func (e *Engine) baseline(ctx context.Context, id, image string, m backup.Manifest) (clusterBaseline, error) {
	dir, _, err := postgresDataDirectory(image)
	if err != nil {
		return clusterBaseline{}, err
	}
	b := clusterBaseline{files: map[string][]byte{}}
	for _, name := range clusterSettingsFiles {
		if b.files[name], err = e.dataOutput(ctx, pgClient{container: id}, "cat", dir+"/"+name); err != nil {
			return clusterBaseline{}, err
		}
	}
	for _, table := range clusterRoles {
		rows, err := e.catalog(ctx, id, m, m.Database, table)
		if err != nil {
			return clusterBaseline{}, err
		}
		b.roles = append(b.roles, rows)
	}
	// inline-sql: reads the fresh cluster the restore created, not the platform database.
	raw, err := e.dataOutput(ctx, pgClient{container: id}, "psql", "-XAqt", "-v", "ON_ERROR_STOP=1", "-U", domain.PostgresSuperuser, "-d", m.Database, "-c", "SELECT pg_catalog.json_object_agg(name, context) FROM pg_catalog.pg_settings")
	if err != nil {
		return clusterBaseline{}, err
	}
	var contexts map[string]string
	if json.Unmarshal(bytes.TrimSpace(raw), &contexts) != nil {
		return clusterBaseline{}, errors.New("cluster settings are unreadable")
	}
	// The server spells a few names in capitals, such as TimeZone, and matches them in any case.
	b.contexts = make(map[string]string, len(contexts))
	for name, context := range contexts {
		b.contexts[settingName(name)] = context
	}
	return b, nil
}

// catalog reads the rows of a catalog table, and of only the columns named after it, in order, as
// the superuser, since only it may read pg_authid. COPY reads the table itself, so no rule the
// dump wrote can stand in for it.
func (e *Engine) catalog(ctx context.Context, id string, m backup.Manifest, database, table string) ([][]string, error) {
	// A database's settings, which its dump carries, can be larger than other helpers' output.
	out := &boundedOutput{limit: 4 << 20, stopOnLimit: true}
	if err := e.containerExec(ctx, pgClient{container: id}, out, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", domain.PostgresSuperuser, "-d", database, "-c", "COPY "+table+" TO STDOUT (FORMAT csv)"); err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(&out.b).ReadAll()
	if err != nil {
		return nil, errors.New("restored cluster is unreadable")
	}
	slices.SortFunc(rows, slices.Compare[[]string])
	return rows, nil
}

// clusterSetting is a database setting, for every role or for one.
type clusterSetting struct{ role, name string }

// checkRestoredCluster refuses a restored cluster, running in container id with event triggers
// off for its clients, that holds what a dump of m's database can't leave in the cluster b was
// read from: a changed role, membership or password, another database, a setting outside the
// database, one the server doesn't know or one only an administrator may choose, which the
// restore leaves out, or an enabled subscription, which a dump restores disabled. Tablespaces,
// replication slots and prepared transactions keep files a cluster's check refuses.
func (e *Engine) checkRestoredCluster(ctx context.Context, id string, m backup.Manifest, b clusterBaseline) error {
	for i, table := range clusterRoles {
		rows, err := e.catalog(ctx, id, m, m.Database, table)
		if err != nil {
			return err
		}
		if !slices.EqualFunc(rows, b.roles[i], slices.Equal) {
			return clusterRefusal("a role or membership the restore didn't create")
		}
	}
	databases, err := e.catalog(ctx, id, m, m.Database, "pg_database (datname, oid)")
	if err != nil {
		return err
	}
	// The cluster's maintenance database goes when the deployment's database takes its name.
	names := []string{"template0", "template1", m.Database}
	if m.Database != "postgres" {
		names = append(names, "postgres")
	}
	slices.Sort(names)
	oid := ""
	for i, d := range databases {
		if i >= len(names) || d[0] != names[i] {
			return clusterRefusal("a database the backup doesn't hold")
		}
		if d[0] == m.Database {
			oid = d[1]
		}
	}
	if len(databases) != len(names) {
		return clusterRefusal("a database the backup doesn't hold")
	}
	// The roles are the baseline's, so they name each setting's role.
	roles := map[string]string{"0": ""}
	for _, r := range b.roles[0] {
		roles[r[0]] = r[1]
	}
	settings, err := e.catalog(ctx, id, m, m.Database, "pg_db_role_setting (setdatabase, setrole, setconfig)")
	if err != nil {
		return err
	}
	for _, s := range settings {
		// Settings in the database, its own or the user's, are part of its dump, and the restore
		// sets none it leaves out.
		set, ok := settingNames(s[2])
		role, known := roles[s[1]]
		if !ok || !known || s[0] != oid || role != "" && role != m.Username {
			return clusterRefusal("a setting outside the database")
		}
		for _, name := range set {
			if ok, err := restorable(clusterSetting{role: role, name: name}, m, b.contexts); err != nil || !ok {
				return clusterRefusal("a setting only an administrator may choose")
			}
		}
	}
	subscriptions, err := e.catalog(ctx, id, m, m.Database, "pg_subscription (subenabled)")
	if err != nil {
		return err
	}
	if slices.ContainsFunc(subscriptions, func(s []string) bool { return s[0] != "f" }) {
		return clusterRefusal("an enabled subscription")
	}
	return nil
}

// clearedNoteLimit keeps the note clearedNote adds within a report's 1024 bytes, after the longest
// note it ends, however long the names it holds are.
const clearedNoteLimit = 512

// clearedNote tells an owner which of a backup's database settings the restored database doesn't
// have.
func clearedNote(settings []clusterSetting) string {
	if len(settings) == 0 {
		return ""
	}
	const prefix, suffix = " Database settings its user can't choose weren't restored: ", ". The restored database runs without them."
	named := ""
	for i, s := range settings {
		name := fmt.Sprintf("%q", s.name)
		if s.role != "" {
			name += fmt.Sprintf(" for role %q", s.role)
		}
		if i > 0 {
			name = ", " + name
		}
		// Room is kept for the count of the settings left unnamed.
		if len(prefix)+len(named)+len(name)+len(suffix)+len(", and 99999999 more") > clearedNoteLimit {
			named += fmt.Sprintf(", and %d more", len(settings)-i)
			if i == 0 {
				named = "1 setting with a long name"
				if len(settings) > 1 {
					named = fmt.Sprintf("%d settings", len(settings))
				}
			}
			break
		}
		named += name
	}
	return prefix + named + suffix
}

// settingName is name as the server matches it to a setting: ASCII letters in any case.
func settingName(name string) string {
	out := []byte(name)
	for i, c := range out {
		if 'A' <= c && c <= 'Z' {
			out[i] = c + 'a' - 'A'
		}
	}
	return string(out)
}

// settingNames reads the setting names from a name=value text array as PostgreSQL writes it.
func settingNames(literal string) ([]string, bool) {
	inner, open := strings.CutPrefix(literal, "{")
	inner, closed := strings.CutSuffix(inner, "}")
	if !open || !closed || inner == "" {
		return nil, false
	}
	var names []string
	for len(inner) > 0 {
		var element strings.Builder
		if inner[0] == '"' {
			i := 1
			for ; i < len(inner) && inner[i] != '"'; i++ {
				if inner[i] == '\\' {
					i++
				}
				if i < len(inner) {
					element.WriteByte(inner[i])
				}
			}
			if i >= len(inner) {
				return nil, false
			}
			inner = inner[i+1:]
		} else {
			i := strings.IndexByte(inner, ',')
			if i < 0 {
				i = len(inner)
			}
			element.WriteString(inner[:i])
			inner = inner[i:]
		}
		name, _, ok := strings.Cut(element.String(), "=")
		if !ok {
			return nil, false
		}
		names = append(names, name)
		if inner, ok = strings.CutPrefix(inner, ","); ok && inner == "" {
			return nil, false
		}
		if !ok && inner != "" {
			return nil, false
		}
	}
	return names, true
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
	"pg_commit_ts":         segmentFile.MatchString,
	"pg_dynshmem":          nil,
	"pg_logical":           func(n string) bool { return n == "replorigin_checkpoint" },
	"pg_logical/mappings":  nil,
	"pg_logical/snapshots": nil,
	"pg_multixact":         nil,
	"pg_multixact/members": segmentFile.MatchString,
	"pg_multixact/offsets": segmentFile.MatchString,
	"pg_notify":            segmentFile.MatchString,
	"pg_replslot":          nil,
	"pg_serial":            segmentFile.MatchString,
	"pg_snapshots":         nil,
	"pg_stat":              func(n string) bool { return n == "pgstat.stat" },
	// A server keeps the directory its temporary files went in, but not the files.
	"base/pgsql_tmp":        nil,
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
	refusal error
}

func (l *clusterListing) Write(p []byte) (int, error) {
	if l.left -= len(p); l.left < 0 {
		l.refusal = clusterRefusal("more files than a cluster holds")
		return 0, l.refusal
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
			l.refusal = clusterRefusal("a file a cluster doesn't hold")
			return 0, l.refusal
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
		if listing.refusal != nil {
			return listing.refusal
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
