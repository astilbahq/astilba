package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
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
// helper ends a waiting rebuild.
const (
	reindexBudget = time.Hour
	reindexKind   = "postgres-reindex-v1"
)

var reindexOptions = pgOptions("lock_timeout=10s", "statement_timeout=30min", "client_connection_check_interval=1000")

// reindexPage is how many names one listing returns, well within its output limit. Tests lower it
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
// the leftovers of an interrupted run. A query returns index OIDs, each rebuilt with REINDEX INDEX
// CONCURRENTLY in its own session, since it cannot run in a transaction. A rebuild that fails drops
// what it left, so a half-built index does not slow every write until the next run. It returns the
// exclusion-constraint indexes the queries skip and the databases it could not connect to.
func (e *Engine) runPostgresReindex(ctx context.Context, helper pgClient, a domain.Application, queries []string) (reindexLeft, error) {
	psql := func(flags string, database reindexDatabase, statements ...string) []string {
		args := []string{"psql", flags, "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.MaintenanceUser(), "-d", database.connection}
		if database.oid != "" {
			// Fails, dividing by zero, in any database but the one listed, and returns one empty
			// value, which a listing skips.
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			args = append(args, "-c", "SELECT '' WHERE 1 / (SELECT count(*)::int FROM pg_database WHERE oid = "+database.oid+" AND datname = current_database()) = 1")
		}
		for _, statement := range statements {
			args = append(args, "-c", statement)
		}
		return args
	}
	// lines lists a query's text values in pages, so no one listing outgrows its output limit
	// however many indexes a database has. Values travel hex-encoded, one per line, so one holding a
	// newline stays one, and the last value of a page comes back the same way. Paging is by value:
	// a rebuilt index keeps its name but not its OID.
	lines := func(database reindexDatabase, query string) ([]string, error) {
		var values []string
		for {
			after := ""
			if len(values) > 0 {
				after = " WHERE v > convert_from(decode('" + hex.EncodeToString([]byte(values[len(values)-1])) + "', 'hex'), 'UTF8') COLLATE \"C\""
			}
			out := &boundedOutput{limit: 1 << 20, stopOnLimit: true}
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			if err := e.containerExec(ctx, helper, out, psql("-XAt", database, "SELECT encode(convert_to(v, 'UTF8'), 'hex') FROM ("+query+") AS q(v)"+after+" ORDER BY v COLLATE \"C\" LIMIT "+strconv.Itoa(reindexPage))...); err != nil {
				return nil, err
			}
			page := 0
			for encoded := range strings.SplitSeq(strings.TrimSpace(out.b.String()), "\n") {
				if v, err := hex.DecodeString(encoded); err == nil && len(v) > 0 {
					values = append(values, string(v))
					page++
				}
			}
			if page < reindexPage {
				return values, nil
			}
		}
	}
	run := func(database reindexDatabase, statements []string) error {
		for _, statement := range statements {
			if err := e.containerExec(ctx, helper, io.Discard, psql("-XAq", database, statement)...); err != nil {
				return err
			}
		}
		return nil
	}
	leftovers := func(database reindexDatabase) ([]string, error) {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		return lines(database, "SELECT format('DROP INDEX CONCURRENTLY IF EXISTS %s', i.oid::regclass) FROM ("+domain.ReindexLeftovers+") AS i(oid)")
	}
	own := reindexDatabase{connection: a.Postgres.Database}
	// current reads a listed database by its oid, which a rename keeps; found is false once it is
	// dropped. Its name and quoted name are the bytes the catalog holds, in the encoding of the
	// database it was made from, which needn't be this one's: with the client's encoding SQL_ASCII
	// the server sends text unconverted, so textsend gives those bytes whatever this database's
	// encoding, and the note escapes what isn't UTF-8.
	current := func(oid string) (now listedDatabase, found bool, err error) {
		// The oid goes into each session's SQL as text, so only a number is taken.
		if _, err = strconv.ParseUint(oid, 10, 32); err != nil {
			return now, false, errors.New("the listing returned something other than a database oid")
		}
		out := &boundedOutput{limit: 1 << 12, stopOnLimit: true}
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		if err = e.containerExec(ctx, helper, out, psql("-XAtq", own, "SET client_encoding = 'SQL_ASCII'", "SELECT CASE WHEN datconnlimit = -2 THEN 'invalid' WHEN datallowconn THEN 'open' ELSE 'closed' END || ' ' || encode(textsend(datname::text), 'hex') || ' ' || encode(textsend(quote_ident(datname)), 'hex') || ' ' || oid::text || ':' || xmin::text FROM pg_database WHERE oid = "+oid)...); err != nil {
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
	// rebuild returns the exclusion-constraint indexes it skipped.
	rebuild := func(database reindexDatabase) ([]string, error) {
		statements, err := leftovers(database)
		if err != nil {
			return nil, err
		}
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			rebuilt, err := lines(database, "SELECT format('REINDEX INDEX CONCURRENTLY %s', i.oid::regclass) FROM ("+query+") AS i(oid)")
			if err != nil {
				return nil, err
			}
			statements = append(statements, rebuilt...)
		}
		if err = run(database, statements); err != nil {
			// Best effort: a drop that fails as well, or a stopping agent, leaves it to the next run.
			if ctx.Err() == nil {
				if dropped, listed := leftovers(database); listed == nil {
					_ = run(database, dropped)
				}
			}
			return nil, err
		}
		var skipped []string
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			indexes, err := lines(database, "SELECT i.oid::regclass::text FROM ("+domain.ExclusionIndexes(query)+") AS i(oid)")
			if err != nil {
				return nil, err
			}
			skipped = append(skipped, indexes...)
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
		listed, err := lines(own, "SELECT oid::text || ':' || xmin::text FROM pg_database WHERE datallowconn OR oid <> 4")
		if err != nil {
			return reindexLeft{}, err
		}
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
				skipped, err := rebuild(reindexDatabase{connection: databaseURI(database.name), oid: oid})
				if err != nil {
					if now, found, lookup := current(oid); ctx.Err() == nil && lookup == nil && (!found || now.key != database.key) {
						// Dropped, renamed or otherwise changed during its rebuild: the next round
						// finds it as it is then, or doesn't.
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
