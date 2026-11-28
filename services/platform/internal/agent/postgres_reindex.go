package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"maps"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// reindexBudget bounds a database's rebuild. A lock wait gives up quickly, because REINDEX
// CONCURRENTLY waits for every older transaction, and one left open would hold the host. Each
// session runs as the cluster's superuser in databases other roles may own, with the session
// options every client has, so a function an owner adds to public, such as a closer match for
// format(), never runs with its privileges. Its client is checked every second, so removing the
// helper ends a waiting rebuild. Its client encoding is SQL_ASCII, so the server sends text as the
// bytes the catalog holds and takes statements as they are, in a database of any encoding: a
// statement built in the database from a name that isn't UTF-8, which a SQL_ASCII database can
// hold, reaches the server unchanged, where a UTF-8 client's would fail its check.
const (
	reindexBudget = time.Hour
	reindexKind   = "postgres-reindex-v1"
)

var reindexOptions = pgOptions("lock_timeout=10s", "statement_timeout=30min", "client_connection_check_interval=1000", "client_encoding=SQL_ASCII")

// reindexPage is how many rows one listing returns, well within its output limit. Tests lower it
// to page through a few indexes.
var reindexPage = 1000

// reindexLeft is what a rebuild left to the database's owner. Databases are named as PostgreSQL
// quotes an identifier, as their indexes are.
type reindexLeft struct {
	// exclusions are the exclusion-constraint indexes the queries skip, named database.index.
	exclusions []string
	// closed are the databases that do not accept connections, so that nothing could rebuild
	// their indexes.
	closed []string
	// invalid are the databases an interrupted DROP DATABASE left, which nothing can connect to
	// until they are dropped.
	invalid []string
}

// rebuildPostgresIndexes rebuilds one database's indexes, returning what it left to the owner,
// or why it did not finish. It runs from a helper in the database's
// network namespace, as backups do, so nothing runs in the database container and removing the
// helper ends whatever the rebuild left running. It connects as the cluster's superuser, but
// REINDEX runs an index's functions as its table's owner, so in the owner model the database's
// own code never runs with the superuser's rights.
func (e *Engine) rebuildPostgresIndexes(ctx context.Context, p deployment.Plan, a domain.Application, serving domain.PostgresRelease, queries []string) (reindexLeft, error) {
	// The container runs the plan's resolved reference to the release's digest.
	v, err := e.Docker.ContainerInspect(ctx, p.ContainerName(a.ID), client.ContainerInspectOptions{})
	if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != a.ID.String() || v.Container.Config.Image != p.Images[a.ID.String()] || !deployment.ResolvedImage(serving.Image, v.Container.Config.Image) || !v.Container.State.Running {
		return reindexLeft{}, errors.New("the database container is not the plan's")
	}
	budget, cancel := context.WithTimeout(ctx, reindexBudget)
	defer cancel()
	name := "astilba-reindex-" + a.ID.String()
	if err = e.removeReindexHelper(budget, name, p); err != nil {
		return reindexLeft{}, err
	}
	labels := visualLabels(p, a.ID)
	labels[labelKind] = reindexKind
	helper, err := e.postgresHelper(budget, name, labels, v.Container.ID, serving.Image)
	if err != nil {
		return reindexLeft{}, err
	}
	defer e.removeHelper(helper)
	helper.env = append(slices.DeleteFunc(slices.Clone(helper.env), func(v string) bool { return strings.HasPrefix(v, "PGOPTIONS=") }), reindexOptions)
	return e.runPostgresReindex(budget, helper, a, queries)
}

// removeReindexHelper removes the helper an earlier rebuild of this unit left when its agent
// stopped, which would otherwise hold the name. A container of that name that isn't one refuses
// the rebuild.
func (e *Engine) removeReindexHelper(ctx context.Context, name string, p deployment.Plan) error {
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !upgradeOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelKind] != reindexKind {
		return errors.New("a container that isn't this unit's index rebuild holds its name")
	}
	e.removeHelper(pgClient{container: v.Container.ID})
	return nil
}

// reindexRounds bounds how often a rebuild lists the databases again for ones that appeared or
// moved during it.
const reindexRounds = 5

// reindexDatabase is a database a rebuild session connects to. One listed by oid is checked to be
// that database at the start of every session: a database can't be renamed while a session is
// connected to it, so the check holds for the whole session.
type reindexDatabase struct{ connection, oid string }

// listedRow is a row of a rebuild's listing: an oid and the bytes of a value it lists beside it.
type listedRow struct {
	oid   string
	value []byte
}

// listedIndex is an index a rebuild listed: its oid and the query that selected it.
type listedIndex struct{ oid, query string }

// listedDatabase is a database a rebuild found by its oid: whether it can be rebuilt (invalid after
// a DROP DATABASE interrupted part-way, closed, or open), its name, its name as PostgreSQL quotes
// an identifier, for notes, so a name with commas or parentheses can't read as several databases
// or another application's, and its oid and catalog row's xmin, as a listing keys it.
type listedDatabase struct{ state, name, quoted, key string }

// databaseURI names a database to psql by its name's bytes, each percent-encoded but letters,
// digits and -._~, as libpq decodes a URI's path. A name another database's encoding wrote needn't
// be valid UTF-8, which a command's arguments can't carry, and no byte can read as part of a
// connection string. The rest of the connection comes from the helper's environment.
func databaseURI(name string) string {
	var b strings.Builder
	b.WriteString("postgresql:///")
	for _, c := range []byte(name) {
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || strings.IndexByte("-._~", c) >= 0 {
			b.WriteByte(c)
		} else {
			b.WriteString("%" + strings.ToUpper(hex.EncodeToString([]byte{c})))
		}
	}
	return b.String()
}

// runPostgresReindex runs each query in every database that accepts connections, after dropping
// the leftovers of an interrupted run. A query returns index oids, each rebuilt with REINDEX INDEX
// CONCURRENTLY in its own session, since it cannot run in a transaction. Indexes are carried by oid,
// never by name, and named only in the session that rebuilds them. A rebuild that fails drops what
// it left, so a half-built index does not slow every write until the next run. It returns the
// exclusion-constraint indexes the queries skip and the databases it could not connect to.
func (e *Engine) runPostgresReindex(ctx context.Context, helper pgClient, a domain.Application, queries []string) (reindexLeft, error) {
	// psql's command connects to the database, checks it is the one listed, and then runs args.
	psql := func(flags string, database reindexDatabase, args ...string) []string {
		command := []string{"psql", flags, "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.MaintenanceUser(), "-d", database.connection}
		if database.oid != "" {
			// Fails, dividing by zero, in any database but the one listed, and returns one empty
			// line, which a listing skips.
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			command = append(command, "-c", "SELECT '' WHERE 1 / (SELECT count(*)::int FROM pg_database WHERE oid = "+database.oid+"::oid AND datname = current_database()) = 1")
		}
		return append(command, args...)
	}
	// list returns the rows a query selects, each an oid and a bytea value, in pages by oid, so no
	// one listing outgrows its output limit however many indexes a database has. A value travels
	// hex-encoded, so one holding a newline, or bytes that aren't UTF-8, stays one row.
	list := func(database reindexDatabase, query string) ([]listedRow, error) {
		var rows []listedRow
		after := "0"
		for {
			out := &boundedOutput{limit: 1 << 20, stopOnLimit: true}
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			if err := e.containerExec(ctx, helper, out, psql("-XAt", database, "-c", "SELECT q.oid::text || ' ' || coalesce(encode(q.value, 'hex'), '') FROM ("+query+") AS q(oid, value) WHERE q.oid > "+after+"::oid ORDER BY q.oid LIMIT "+strconv.Itoa(reindexPage))...); err != nil {
				return nil, err
			}
			page := 0
			for line := range strings.SplitSeq(out.b.String(), "\n") {
				if line == "" {
					continue
				}
				oid, encoded, _ := strings.Cut(line, " ")
				value, err := hex.DecodeString(encoded)
				// An oid goes into later sessions' SQL as text, so only a number is taken.
				if _, parseErr := strconv.ParseUint(oid, 10, 32); err != nil || parseErr != nil {
					return nil, errors.New("a listing returned an unreadable row")
				}
				rows = append(rows, listedRow{oid: oid, value: value})
				after = oid
				page++
			}
			if page < reindexPage {
				return rows, nil
			}
		}
	}
	// indexes lists the indexes a query selects.
	indexes := func(database reindexDatabase, query string) ([]listedIndex, error) {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		rows, err := list(database, "SELECT i.oid, ''::bytea FROM ("+query+") AS i(oid)")
		var out []listedIndex
		for _, row := range rows {
			out = append(out, listedIndex{oid: row.oid, query: query})
		}
		return out, err
	}
	// index runs verb on the index with the oid in a session of its own, since neither REINDEX INDEX
	// CONCURRENTLY nor DROP INDEX CONCURRENTLY runs in a transaction. psql's \gexec runs the
	// statement the session builds from the oid as it starts, so the index's name never passes
	// through the agent or a command's arguments: one renamed since the listing is still the one
	// rebuilt, and a name that isn't UTF-8 reaches the statement as its bytes. psql runs \gexec only
	// from a script, which goes in on standard input. The statement is built only while query, the
	// listing's own, still selects the index, so one that has since become invalid, such as the
	// _ccold an interrupted REINDEX of the owner's own leaves under the oid, isn't made valid.
	// Either statement leaves no relation with the oid, a rebuilt index taking a new one, so one
	// still there means the index no longer qualified, or was renamed and another made under its
	// name in the instant before its statement ran: the session fails, and the step is left
	// unfinished, for the next one to drop or rebuild what it finds then. The query, one of the
	// domain's constants, passes through psql's script parser, so it is one statement and holds no
	// backslash.
	index := func(database reindexDatabase, verb, query, oid string) error {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		script := "SELECT format('" + verb + " %s', q.oid::regclass) FROM (" + query + ") AS q(oid) WHERE q.oid = " + oid + "::oid \\gexec\n" +
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			"SELECT '' WHERE 1 / (SELECT 1 - count(*)::int FROM pg_class WHERE oid = " + oid + "::oid) = 1;\n\\q\n"
		return e.containerExecInput(ctx, helper, strings.NewReader(script), io.Discard, psql("-XAq", database, "-f", "-")...)
	}
	run := func(database reindexDatabase, verb string, indexes []listedIndex) error {
		for _, listed := range indexes {
			if err := index(database, verb, listed.query, listed.oid); err != nil {
				return err
			}
		}
		return nil
	}
	// inline-sql: runs in a managed customer database through psql, not in the platform database
	const drop, reindex = "DROP INDEX CONCURRENTLY IF EXISTS", "REINDEX INDEX CONCURRENTLY"
	own := reindexDatabase{connection: a.Postgres.Database}
	// current reads a listed database by its oid, which a rename keeps; found is false once it is
	// dropped. Its name and quoted name are the bytes the catalog holds, in the encoding of the
	// database it was made from, which needn't be this one's: with the client's encoding SQL_ASCII
	// the server sends text unconverted, so textsend gives those bytes whatever this database's
	// encoding, and the note escapes what isn't UTF-8.
	current := func(oid string) (now listedDatabase, found bool, err error) {
		out := &boundedOutput{limit: 1 << 12, stopOnLimit: true}
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		if err = e.containerExec(ctx, helper, out, psql("-XAtq", own, "-c", "SELECT CASE WHEN datconnlimit = -2 THEN 'invalid' WHEN datallowconn THEN 'open' ELSE 'closed' END || ' ' || encode(textsend(datname::text), 'hex') || ' ' || encode(textsend(quote_ident(datname)), 'hex') || ' ' || oid::text || ':' || xmin::text FROM pg_database WHERE oid = "+oid+"::oid")...); err != nil {
			return now, false, err
		}
		read := strings.Fields(out.b.String())
		if len(read) == 0 {
			return now, false, nil
		}
		if len(read) != 4 {
			return now, false, errors.New("the listing returned an unreadable database")
		}
		name, nameErr := hex.DecodeString(read[1])
		quoted, quotedErr := hex.DecodeString(read[2])
		if nameErr != nil || quotedErr != nil {
			return now, false, errors.New("the listing returned an unreadable database")
		}
		return listedDatabase{state: read[0], name: string(name), quoted: string(quoted), key: read[3]}, true, nil
	}
	// rebuild returns the exclusion-constraint indexes it skipped, named as PostgreSQL quotes them,
	// in UTF-8 where they convert to it. No conversion exists from every encoding, nor for every
	// character, nor for bytes a SQL_ASCII database holds that aren't UTF-8, so where a listing's
	// names don't all convert, they are listed again as the bytes the database holds, and the note
	// escapes what isn't UTF-8, as it does databases' names.
	rebuild := func(database reindexDatabase, name string) ([]string, error) {
		leftovers, err := indexes(database, domain.ReindexLeftovers)
		if err != nil {
			return nil, err
		}
		// An index two queries select is rebuilt once.
		var due []listedIndex
		listed := map[string]bool{}
		for _, query := range queries {
			selected, err := indexes(database, query)
			if err != nil {
				return nil, err
			}
			for _, index := range selected {
				if !listed[index.oid] {
					listed[index.oid] = true
					due = append(due, index)
				}
			}
		}
		e.point("postgres-reindex-indexes " + name)
		if err = run(database, drop, leftovers); err == nil {
			err = run(database, reindex, due)
		}
		if err != nil {
			// Best effort: a drop that fails as well, or a stopping agent, leaves it to the next run.
			if ctx.Err() == nil {
				if dropped, listErr := indexes(database, domain.ReindexLeftovers); listErr == nil {
					_ = run(database, drop, dropped)
				}
			}
			return nil, err
		}
		var skipped []string
		for _, query := range queries {
			named := func(encoding string) ([]listedRow, error) {
				// inline-sql: runs in a managed customer database through psql, not in the platform database
				return list(database, "SELECT i.oid, convert_to(i.oid::regclass::text, '"+encoding+"') FROM ("+domain.ExclusionIndexes(query)+") AS i(oid)")
			}
			rows, err := named("UTF8")
			if err != nil && ctx.Err() == nil {
				rows, err = named("SQL_ASCII")
			}
			if err != nil {
				return nil, err
			}
			for _, row := range rows {
				skipped = append(skipped, string(row.value))
			}
		}
		return skipped, nil
	}
	// A database marked as a template is rebuilt like any other: its owner may mark their own
	// database IS_TEMPLATE, and template1's indexes are what each new database copies. A copy
	// made from a template while its rebuild is connected fails, as one made while any session
	// is; one made before then copies indexes not yet rebuilt, so once every listed database is
	// rebuilt the databases are listed again, until a listing finds none it has not seen. Before
	// the owner model the database user is a superuser, so it can create, rename and close any
	// database during the step. Databases are listed and read by oid, so a renamed one is
	// rebuilt under its new name, in the next round if it moved while its own rebuild ran.
	// A database is seen by its oid and its catalog row's xmin, which any change to the row
	// replaces, so one dropped and made again under the same oid, which CREATE DATABASE lets a
	// role that may create databases choose, from a template not yet rebuilt, is rebuilt again.
	// A database that does not accept connections, which even the superuser then can't make, is
	// named instead. One the deployment connects to fails the listing, so it is left unfinished.
	// template0, which is always oid 4, never accepts connections and holds only the catalogs initdb
	// made, which the queries leave alone; another database a superuser names template0 is named
	// if closed, as any other is. A DROP DATABASE interrupted part-way leaves the database invalid,
	// datconnlimit -2, still marked as accepting connections, though no one can connect to it;
	// it is named apart, since only dropping it finishes the rebuild.
	named := map[string]reindexLeft{}
	seen := map[string]bool{}
	for round := 0; ; round++ {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		rows, err := list(own, "SELECT oid, convert_to(xmin::text, 'UTF8') FROM pg_database WHERE datallowconn OR oid <> 4")
		if err != nil {
			return reindexLeft{}, err
		}
		var listed []string
		for _, row := range rows {
			listed = append(listed, row.oid+":"+string(row.value))
		}
		// A database dropped since it was handled leaves nothing to name, and template0 closed
		// again is skipped as it is when closed from the start.
		present := map[string]bool{}
		for _, key := range listed {
			oid, _, _ := strings.Cut(key, ":")
			present[oid] = true
		}
		maps.DeleteFunc(named, func(oid string, _ reindexLeft) bool { return !present[oid] })
		listed = slices.DeleteFunc(listed, func(key string) bool { return seen[key] })
		if len(listed) == 0 {
			var left reindexLeft
			for _, each := range named {
				left.exclusions = append(left.exclusions, each.exclusions...)
				left.closed = append(left.closed, each.closed...)
				left.invalid = append(left.invalid, each.invalid...)
			}
			return left, nil
		}
		if round == reindexRounds {
			return reindexLeft{}, errors.New("databases kept appearing or moving during the rebuild")
		}
		if round == 0 {
			e.point("postgres-reindex-listed")
		}
		for _, key := range listed {
			oid, _, _ := strings.Cut(key, ":")
			database, found, err := current(oid)
			if err != nil {
				return reindexLeft{}, err
			}
			// A database handled again, as its row changed, is named only as it is now.
			delete(named, oid)
			switch {
			case !found:
				// Dropped since it was listed.
			case database.state == "invalid":
				named[oid] = reindexLeft{invalid: []string{database.quoted}}
			case database.state == "closed":
				named[oid] = reindexLeft{closed: []string{database.quoted}}
			default:
				e.point("postgres-reindex-read " + database.name)
				skipped, err := rebuild(reindexDatabase{connection: databaseURI(database.name), oid: oid}, database.name)
				if err != nil {
					if now, found, lookup := current(oid); ctx.Err() == nil && lookup == nil && (!found || now.key != database.key || now.state != database.state) {
						// Dropped, renamed or otherwise changed during its rebuild: the next round
						// finds it as it is then, or doesn't. A DROP DATABASE marks the row invalid
						// in place, keeping its key, so the state is compared too.
						continue
					}
					return reindexLeft{}, err
				}
				var exclusions []string
				for _, index := range skipped {
					exclusions = append(exclusions, database.quoted+"."+index)
				}
				named[oid] = reindexLeft{exclusions: exclusions}
			}
			seen[key] = true
			if found {
				// It was handled as read, which may be after its row changed again.
				seen[database.key] = true
			}
		}
	}
}
