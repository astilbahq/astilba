package agent

import (
	"context"
	"encoding/csv"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
)

// A restore never runs a dump as a superuser. The superuser creates the database user, the
// manifest's roles and an empty database the user owns, and pg_restore then runs as that user,
// without --create, so it skips the entries that describe the database itself. Those come from a
// rendering of the archive, held to the few statements pg_dump writes for a database and run one at
// a time, as the user. So nothing in an archive, even one from a compromised server, can do more
// than the deployment's own database user could.

// restoreList is where a restore writes the list of archive entries pg_restore reads, inside the
// isolated server's container. Nothing else runs there but the server.
const restoreList = "/tmp/astilba-restore.list"

// tocEntry is one line of an archive's table of contents, as pg_restore lists it.
type tocEntry struct {
	line string
	kind tocKind
}

type tocKind int

const (
	tocOther tocKind = iota
	tocSchema
	tocACL
	tocMaterializedData
	tocDatabase
)

var tocLine = regexp.MustCompile(`^[0-9]+; [0-9]+ [0-9]+ (.+)$`)

// readTOC reads the entries pg_restore lists for an archive, with --create so the database's own
// are among them, and sorts them into the passes a restore runs them in.
func readTOC(listing []byte) ([]tocEntry, error) {
	var entries []tocEntry
	for line := range strings.Lines(string(listing)) {
		line = strings.TrimSuffix(line, "\n")
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		match := tocLine.FindStringSubmatch(line)
		if match == nil {
			return nil, errors.New("the archive's contents are unreadable")
		}
		rest, kind := match[1], tocOther
		switch {
		case strings.HasPrefix(rest, "DATABASE - "), strings.HasPrefix(rest, "DATABASE PROPERTIES - "), strings.HasPrefix(rest, "COMMENT - DATABASE "), strings.HasPrefix(rest, "ACL - DATABASE "), strings.HasPrefix(rest, "SECURITY LABEL - DATABASE "):
			kind = tocDatabase
		case strings.HasPrefix(rest, "SCHEMA - "):
			kind = tocSchema
		case strings.HasPrefix(rest, "ACL "), strings.HasPrefix(rest, "DEFAULT ACL "):
			kind = tocACL
		case strings.HasPrefix(rest, "MATERIALIZED VIEW DATA "):
			kind = tocMaterializedData
		}
		entries = append(entries, tocEntry{line: line, kind: kind})
	}
	return entries, nil
}

// tocList is the list file for the entries keep accepts, in the archive's order.
func tocList(entries []tocEntry, keep func(tocKind) bool) string {
	var b strings.Builder
	for _, e := range entries {
		if keep(e.kind) {
			b.WriteString(e.line + "\n")
		}
	}
	return b.String()
}

// databaseStatementKind is which of the statements pg_dump writes for a database one is.
type databaseStatementKind int

const (
	statementCreate databaseStatementKind = iota
	statementOwner
	statementProperty
	statementSetting
	statementComment
	statementPrivilege
)

// databaseStatement is a statement from the entries that describe an archive's database.
type databaseStatement struct {
	kind databaseStatementKind
	text string
	// setting is the setting a statementSetting sets, for the database or one role in it.
	setting clusterSetting
	// owner is the role a statementOwner makes the database's owner.
	owner string
}

const (
	sqlIdentifier = `("(?:[^"]|"")+"|[a-z_][a-z0-9_]*)`
	// pg_dump writes a setting's value that holds a backslash as an escape string, whose only
	// escapes are its doubled backslashes and quotes.
	sqlLiteral    = `(?:'(?:[^']|'')*'|E'(?:[^'\\]|''|\\\\)*')`
	sqlValues     = sqlLiteral + `(?:, ` + sqlLiteral + `)*`
	sqlPrivileges = `(?:ALL|CREATE|CONNECT|TEMPORARY)(?:,(?:CREATE|CONNECT|TEMPORARY))*`
	sqlGrantee    = `(PUBLIC|` + sqlIdentifier + `)`
)

// databaseShapes are the statements pg_dump writes for a database, and nothing more.
var databaseShapes = []struct {
	kind databaseStatementKind
	re   *regexp.Regexp
}{
	// inline-sql: a pattern pg_dump's statements are held to, which runs nothing.
	{statementCreate, regexp.MustCompile(`^CREATE DATABASE ` + sqlIdentifier + ` WITH TEMPLATE = template0(?: [A-Z_]+ = (?:` + sqlLiteral + `|[a-z0-9_]+))*$`)},
	{statementOwner, regexp.MustCompile(`^ALTER DATABASE ` + sqlIdentifier + ` OWNER TO ` + sqlIdentifier + `$`)},
	{statementProperty, regexp.MustCompile(`^ALTER DATABASE ` + sqlIdentifier + ` (?:CONNECTION LIMIT = -?[0-9]{1,10}|IS_TEMPLATE = (?:true|false)|ALLOW_CONNECTIONS = (?:true|false))$`)},
	{statementSetting, regexp.MustCompile(`^ALTER DATABASE ` + sqlIdentifier + ` SET ` + sqlIdentifier + ` TO ` + sqlValues + `$`)},
	{statementSetting, regexp.MustCompile(`^ALTER ROLE ` + sqlIdentifier + ` IN DATABASE ` + sqlIdentifier + ` SET ` + sqlIdentifier + ` TO ` + sqlValues + `$`)},
	{statementComment, regexp.MustCompile(`^COMMENT ON DATABASE ` + sqlIdentifier + ` IS ` + sqlLiteral + `$`)},
	{statementPrivilege, regexp.MustCompile(`^GRANT ` + sqlPrivileges + ` ON DATABASE ` + sqlIdentifier + ` TO ` + sqlGrantee + `(?: WITH GRANT OPTION)?$`)},
	{statementPrivilege, regexp.MustCompile(`^REVOKE (?:GRANT OPTION FOR )?` + sqlPrivileges + ` ON DATABASE ` + sqlIdentifier + ` FROM ` + sqlGrantee + `$`)},
}

// restoreSessionSettings are what pg_restore sets at the start of each connection of a rendering,
// which the statements of a database's entries follow.
var restoreSessionSettings = regexp.MustCompile(`^(?:SET (?:statement_timeout|lock_timeout|idle_in_transaction_session_timeout|transaction_timeout|client_encoding|standard_conforming_strings|check_function_bodies|xmloption|client_min_messages|row_security|default_tablespace|default_table_access_method) = [A-Za-z0-9_']+|SELECT pg_catalog\.set_config\('search_path', '', false\))$`)

// restoreMetaCommand is a psql command a rendering holds between its connections. None is run.
var restoreMetaCommand = regexp.MustCompile(`^\\(?:(?:restrict|unrestrict) [A-Za-z0-9]+|connect .+)$`)

func unquoteIdentifier(v string) string {
	if inner, ok := strings.CutPrefix(v, `"`); ok {
		return strings.ReplaceAll(strings.TrimSuffix(inner, `"`), `""`, `"`)
	}
	return v
}

// sqlStatements splits a rendering into its statements, without their semicolons, and refuses a
// psql command other than the ones pg_restore writes between connections. Comments are dropped.
// Quoted text is kept whole, so a semicolon in a comment on the database ends nothing.
func sqlStatements(script string) ([]string, error) {
	var statements []string
	var current strings.Builder
	for i := 0; i < len(script); i++ {
		c := script[i]
		if strings.TrimSpace(current.String()) == "" {
			switch {
			case c == '\n' || c == ' ' || c == '\t' || c == '\r':
				continue
			case strings.HasPrefix(script[i:], "--"):
				end := strings.IndexByte(script[i:], '\n')
				if end < 0 {
					return statements, nil
				}
				i += end
				continue
			case c == '\\':
				end := strings.IndexByte(script[i:], '\n')
				if end < 0 {
					end = len(script) - i
				}
				if !restoreMetaCommand.MatchString(script[i : i+end]) {
					return nil, errors.New("the archive's database entries hold a command a dump doesn't write")
				}
				i += end
				continue
			}
			current.Reset()
		}
		switch c {
		case '\'', '"':
			end := i + 1
			for ; end < len(script); end++ {
				if script[end] == c {
					if end+1 < len(script) && script[end+1] == c {
						end++
						continue
					}
					break
				}
			}
			if end >= len(script) {
				return nil, errors.New("the archive's database entries are unreadable")
			}
			current.WriteString(script[i : end+1])
			i = end
		case ';':
			statements = append(statements, strings.TrimSpace(current.String()))
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	if strings.TrimSpace(current.String()) != "" {
		return nil, errors.New("the archive's database entries are unreadable")
	}
	return statements, nil
}

// databaseStatements reads the statements in a rendering of m's database entries. Each must be
// one pg_dump writes for m's database.
func databaseStatements(script string, m backup.Manifest) ([]databaseStatement, error) {
	statements, err := sqlStatements(script)
	if err != nil {
		return nil, err
	}
	var out []databaseStatement
	for _, text := range statements {
		if restoreSessionSettings.MatchString(text) {
			continue
		}
		i := slices.IndexFunc(databaseShapes, func(s struct {
			kind databaseStatementKind
			re   *regexp.Regexp
		}) bool {
			return s.re.MatchString(text)
		})
		if i < 0 {
			return nil, clusterRefusal("a database statement a dump doesn't write")
		}
		shape := databaseShapes[i]
		match := shape.re.FindStringSubmatch(text)
		s := databaseStatement{kind: shape.kind, text: text}
		database := match[1]
		switch {
		// inline-sql: tells a role's setting in a dump from the database's, and runs nothing.
		case shape.kind == statementSetting && strings.HasPrefix(text, "ALTER ROLE "):
			database = match[2]
			s.setting = clusterSetting{role: unquoteIdentifier(match[1]), name: unquoteIdentifier(match[3])}
		case shape.kind == statementSetting:
			s.setting = clusterSetting{name: unquoteIdentifier(match[2])}
		case shape.kind == statementOwner:
			s.owner = unquoteIdentifier(match[2])
		}
		if unquoteIdentifier(database) != m.Database {
			return nil, clusterRefusal("a database the backup doesn't hold")
		}
		out = append(out, s)
	}
	if len(out) == 0 || out[0].kind != statementCreate || slices.ContainsFunc(out[1:], func(s databaseStatement) bool { return s.kind == statementCreate }) {
		return nil, errors.New("the archive's database entries are unreadable")
	}
	return out, nil
}

// restorable reports whether the database's user may restore setting s: one the server lets any
// user choose, for the database or for the user itself. Others, such as one only an administrator
// may choose, a module's, or another role's, are left out of the restored database and named in
// its note. A module's setting has a dot in its name, and the user's session can set one only while
// the module is loaded, which nothing in a restore decides. A name the server doesn't know and no
// module could define is refused. hiddenSettings are known but not listed with the others.
func restorable(s clusterSetting, m backup.Manifest, contexts map[string]string) (bool, error) {
	name := settingName(s.name)
	if strings.Contains(name, ".") || slices.Contains(hiddenSettings, name) {
		return false, nil
	}
	context, known := contexts[name]
	if !known {
		return false, clusterRefusal("a setting the server doesn't know")
	}
	return context == "user" && (s.role == "" || s.role == m.Username), nil
}

// hiddenSettings are the settings a database's user may choose that pg_settings doesn't list. A
// backup lets a function set them, as its user may. The restore leaves them out of the database's
// own settings: who a session runs as would decide who the restore's own sessions run as, the
// random seed is no setting a database needs, and PostgreSQL only still accepts the others and
// ignores them.
var hiddenSettings = []string{"default_with_oids", "role", "seed", "session_authorization", "ssl_renegotiation_limit"}

// renderDatabase renders the archive's entries that describe its database, from container id.
func (e *Engine) renderDatabase(ctx context.Context, id, archive string, entries []tocEntry) (string, error) {
	if err := e.writeRestoreList(ctx, id, tocList(entries, func(k tocKind) bool { return k == tocDatabase })); err != nil {
		return "", err
	}
	out := &boundedOutput{limit: 4 << 20, stopOnLimit: true}
	if err := e.archiveTool(ctx, id, archive, out, "pg_restore", "--create", "-L", restoreList, "--file=-", "--restrict-key=AstilbaRestoreDatabase"); err != nil {
		return "", err
	}
	return out.b.String(), nil
}

// archiveTool runs command in container id with the archive file on its standard input.
func (e *Engine) archiveTool(ctx context.Context, id, archive string, out io.Writer, command ...string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	return e.dataTool(ctx, f, out, append([]string{"exec", "-i", id}, command...)...)
}

func (e *Engine) writeRestoreList(ctx context.Context, id, list string) error {
	return e.dataTool(ctx, strings.NewReader(list), io.Discard, "exec", "-i", id, "sh", "-c", `rm -f -- "$1" && umask 077 && cat > "$1"`, "sh", restoreList)
}

// asDatabaseUser runs validated statements in order in m's database, in one session as its user,
// each in its own transaction. The first that fails stops the rest and fails the call. A
// connection limit or another property of the database applies only to later sessions.
func (e *Engine) asDatabaseUser(ctx context.Context, id string, m backup.Manifest, statements []databaseStatement) error {
	if len(statements) == 0 {
		return nil
	}
	command := []string{"exec", id, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", m.Username, "-d", m.Database}
	for _, s := range statements {
		command = append(command, "-c", s.text)
	}
	return e.dataTool(ctx, nil, io.Discard, command...)
}

// archiveRestore is what a restore reads from an archive before anything from it runs: its
// entries, the statements about its database, and the role that owns the database, which is the
// database user or, from a server whose user was the superuser, one of the manifest's roles, of
// which the user is a member.
type archiveRestore struct {
	archive    string
	entries    []tocEntry
	statements []databaseStatement
	owner      string
}

// readArchive reads dir's data.dump, which m describes, from container id. The archive's own tools
// read it without a server.
func (e *Engine) readArchive(ctx context.Context, id, dir string, m backup.Manifest, roles []string) (archiveRestore, error) {
	r := archiveRestore{archive: filepath.Join(dir, "data.dump"), owner: m.Username}
	listing := &boundedOutput{limit: 64 << 20, stopOnLimit: true}
	if err := e.archiveTool(ctx, id, r.archive, listing, "pg_restore", "-l", "--create"); err != nil {
		return archiveRestore{}, err
	}
	var err error
	if r.entries, err = readTOC(listing.b.Bytes()); err != nil {
		return archiveRestore{}, err
	}
	script, err := e.renderDatabase(ctx, id, r.archive, r.entries)
	if err != nil {
		return archiveRestore{}, err
	}
	if r.statements, err = databaseStatements(script, m); err != nil {
		return archiveRestore{}, err
	}
	for _, s := range r.statements {
		if s.kind == statementOwner {
			r.owner = s.owner
		}
	}
	if r.owner != m.Username && !slices.Contains(roles, r.owner) {
		return archiveRestore{}, clusterRefusal("a database owned by a role the backup doesn't hold")
	}
	return r, nil
}

// restoreAsOwner restores r's archive, which m describes, into m's empty database in the isolated
// cluster running in container id, as m's user, once the superuser has created the user, roles and
// database. It returns the database's settings it left out.
func (e *Engine) restoreAsOwner(ctx context.Context, id string, r archiveRestore, m backup.Manifest, roles []string, b clusterBaseline) ([]clusterSetting, error) {
	// Everything is checked before anything from the archive runs.
	var left []clusterSetting
	var settings, after, properties []databaseStatement
	for _, s := range r.statements {
		switch s.kind {
		case statementCreate, statementOwner:
			// The superuser created the database, with its owner.
		case statementSetting:
			ok, err := restorable(s.setting, m, b.contexts)
			if err != nil {
				return nil, err
			}
			if !ok {
				left = append(left, s.setting)
				continue
			}
			settings = append(settings, s)
		case statementProperty:
			properties = append(properties, s)
		default:
			after = append(after, s)
		}
	}
	// pg_restore applies a database's settings before its contents, so they apply to the restore.
	if err := e.asDatabaseUser(ctx, id, m, settings); err != nil {
		return nil, err
	}
	pass := func(keep func(tocKind) bool) error {
		if err := e.writeRestoreList(ctx, id, tocList(r.entries, keep)); err != nil {
			return err
		}
		return e.archiveTool(ctx, id, r.archive, io.Discard, "pg_restore", "--exit-on-error", "-U", m.Username, "-d", m.Database, "-L", restoreList)
	}
	if len(roles) == 0 {
		if err := pass(func(k tocKind) bool { return k != tocDatabase }); err != nil {
			return nil, err
		}
	} else if err := e.restoreWithRoles(ctx, id, m, roles, pass); err != nil {
		return nil, err
	}
	// A connection limit can refuse the user's next session, so the database's properties come
	// last, in the session that applies the rest.
	if err := e.asDatabaseUser(ctx, id, m, append(after, properties...)); err != nil {
		return nil, err
	}
	return left, nil
}

// restoreWithRoles runs a restore's passes when the archive's objects may belong to the manifest's
// other roles. Making one of them an object's owner needs it to have CREATE on the database or the
// object's schema, which the dump grants only once its objects exist, so the superuser grants that
// for as long as the owners are set, and takes back exactly what it granted before the dump's own
// grants are restored. Materialized views are filled last, with their final privileges, as
// pg_restore does.
func (e *Engine) restoreWithRoles(ctx context.Context, id string, m backup.Manifest, roles []string, pass func(func(tocKind) bool) error) error {
	var revoke strings.Builder
	// grant gives each role that lacks it CREATE on the database or a schema, and keeps how to
	// take it back. The database holds only schemas until the second call, so reading it as the
	// superuser runs nothing of the dump's.
	grant := func(object string) error {
		missing, err := e.lackingCreate(ctx, id, m, roles, object)
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, g := range missing {
			target := "DATABASE " + quoteIdentifier(m.Database)
			if object == "SCHEMA" {
				target = "SCHEMA " + quoteIdentifier(g[1])
			}
			b.WriteString("GRANT CREATE ON " + target + " TO " + quoteIdentifier(g[0]) + ";\n")
			revoke.WriteString("REVOKE CREATE ON " + target + " FROM " + quoteIdentifier(g[0]) + ";\n")
		}
		return e.superuserSQL(ctx, id, m.Database, b.String())
	}
	if err := grant("DATABASE"); err != nil {
		return err
	}
	if err := pass(func(k tocKind) bool { return k == tocSchema }); err != nil {
		return err
	}
	if err := grant("SCHEMA"); err != nil {
		return err
	}
	if err := pass(func(k tocKind) bool { return k == tocOther }); err != nil {
		return err
	}
	if err := e.superuserSQL(ctx, id, m.Database, revoke.String()); err != nil {
		return err
	}
	if err := pass(func(k tocKind) bool { return k == tocACL }); err != nil {
		return err
	}
	return pass(func(k tocKind) bool { return k == tocMaterializedData })
}

// lackingCreate lists each role, and for SCHEMA each schema of m's database other than the
// system's, where the role lacks CREATE on the database or the schema.
func (e *Engine) lackingCreate(ctx context.Context, id string, m backup.Manifest, roles []string, object string) ([][]string, error) {
	quoted := make([]string, len(roles))
	for i, r := range roles {
		quoted[i] = quoteLiteral(r)
	}
	in := "r.rolname IN (" + strings.Join(quoted, ", ") + ")"
	// inline-sql: reads the fresh cluster the restore created, not the platform database.
	query := "SELECT r.rolname, '' FROM pg_catalog.pg_roles r WHERE " + in + " AND NOT pg_catalog.has_database_privilege(r.oid, " + quoteLiteral(m.Database) + ", 'CREATE') ORDER BY 1"
	if object == "SCHEMA" {
		// inline-sql: reads the fresh cluster the restore created, not the platform database.
		query = "SELECT r.rolname, n.nspname FROM pg_catalog.pg_roles r CROSS JOIN pg_catalog.pg_namespace n WHERE " + in + " AND n.nspname NOT LIKE 'pg\\_%' AND n.nspname <> 'information_schema' AND NOT pg_catalog.has_schema_privilege(r.oid, n.oid, 'CREATE') ORDER BY 1, 2"
	}
	out := &boundedOutput{limit: 4 << 20, stopOnLimit: true}
	if err := e.containerExec(ctx, pgClient{container: id}, out, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", domain.PostgresSuperuser, "-d", m.Database, "-c", "COPY ("+query+") TO STDOUT (FORMAT csv)"); err != nil {
		return nil, err
	}
	rows, err := csv.NewReader(&out.b).ReadAll()
	if err != nil || slices.ContainsFunc(rows, func(r []string) bool { return len(r) != 2 }) {
		return nil, errors.New("restored cluster is unreadable")
	}
	return rows, nil
}
