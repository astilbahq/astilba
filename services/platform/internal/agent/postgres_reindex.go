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

// reindexLeft is what a rebuild left to the database's owner.
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

// runPostgresReindex runs each query in every database that accepts connections, after dropping
// the leftovers of an interrupted run. A query returns index OIDs, each rebuilt with REINDEX INDEX
// CONCURRENTLY in its own session, since it cannot run in a transaction. A rebuild that fails drops
// what it left, so a half-built index does not slow every write until the next run. It returns the
// exclusion-constraint indexes the queries skip and the databases it could not connect to.
func (e *Engine) runPostgresReindex(ctx context.Context, helper pgClient, a domain.Application, queries []string) (reindexLeft, error) {
	// lines lists a query's text values in pages, so no one listing outgrows its output limit
	// however many indexes a database has. Values travel hex-encoded, one per line, so one holding a
	// newline stays one, and the last value of a page comes back the same way. Paging is by value:
	// a rebuilt index keeps its name but not its OID.
	lines := func(database, query string) ([]string, error) {
		var values []string
		for {
			after := ""
			if len(values) > 0 {
				after = " WHERE v > convert_from(decode('" + hex.EncodeToString([]byte(values[len(values)-1])) + "', 'hex'), 'UTF8') COLLATE \"C\""
			}
			out := &boundedOutput{limit: 1 << 20, stopOnLimit: true}
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			if err := e.containerExec(ctx, helper, out, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.MaintenanceUser(), "-d", database, "-c", "SELECT encode(convert_to(v, 'UTF8'), 'hex') FROM ("+query+") AS q(v)"+after+" ORDER BY v COLLATE \"C\" LIMIT "+strconv.Itoa(reindexPage)); err != nil {
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
	run := func(connection string, statements []string) error {
		for _, statement := range statements {
			if err := e.containerExec(ctx, helper, io.Discard, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.MaintenanceUser(), "-d", connection, "-c", statement); err != nil {
				return err
			}
		}
		return nil
	}
	leftovers := func(connection string) ([]string, error) {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		return lines(connection, "SELECT format('DROP INDEX CONCURRENTLY IF EXISTS %s', i.oid::regclass) FROM ("+domain.ReindexLeftovers+") AS i(oid)")
	}
	// A database marked as a template is rebuilt like any other: its owner may mark their own
	// database IS_TEMPLATE, and template1's indexes are what each new database copies. A copy
	// made from a template while its rebuild is connected fails, as one made while any session
	// is. A database that does not accept connections, which even the superuser then can't make,
	// is named instead; before the owner model the database user is a superuser and can close
	// any. One the deployment connects to fails the listing, so it is left unfinished. template0
	// never accepts connections and holds only the catalogs initdb made, which the queries leave
	// alone. Names are listed apart from whether each accepts connections, which is read just
	// before its rebuild: a name stays in its place in the listing's pages whatever the database
	// does, and one closed after its check fails to connect, so the step is left unfinished.
	// A DROP DATABASE interrupted part-way leaves the database invalid, datconnlimit -2, still
	// listed and still marked as accepting connections, though no one can connect to it; it is
	// named apart, since only dropping it finishes the rebuild.
	// inline-sql: runs in a managed customer database through psql, not in the platform database
	databases, err := lines(a.Postgres.Database, "SELECT datname::text FROM pg_database WHERE datallowconn OR datname <> 'template0'")
	if err != nil {
		return reindexLeft{}, err
	}
	var left reindexLeft
	for _, database := range databases {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		state, err := lines(a.Postgres.Database, "SELECT CASE WHEN datconnlimit = -2 THEN 'invalid' WHEN datallowconn THEN 'open' ELSE 'closed' END FROM pg_database WHERE datname = convert_from(decode('"+hex.EncodeToString([]byte(database))+"', 'hex'), 'UTF8')")
		if err != nil {
			return reindexLeft{}, err
		}
		if len(state) == 0 {
			// Dropped since it was listed.
			continue
		}
		switch state[0] {
		case "invalid":
			left.invalid = append(left.invalid, database)
			continue
		case "closed":
			left.closed = append(left.closed, database)
			continue
		}
		// psql reads its -d value as a connection string when it holds '='; quote it as one.
		connection := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(database) + "'"
		statements, err := leftovers(connection)
		if err != nil {
			return reindexLeft{}, err
		}
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			rebuild, err := lines(connection, "SELECT format('REINDEX INDEX CONCURRENTLY %s', i.oid::regclass) FROM ("+query+") AS i(oid)")
			if err != nil {
				return reindexLeft{}, err
			}
			statements = append(statements, rebuild...)
		}
		if err = run(connection, statements); err != nil {
			// Best effort: a drop that fails as well, or a stopping agent, leaves it to the next run.
			if ctx.Err() == nil {
				if dropped, listed := leftovers(connection); listed == nil {
					_ = run(connection, dropped)
				}
			}
			return reindexLeft{}, err
		}
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			skipped, err := lines(connection, "SELECT i.oid::regclass::text FROM ("+domain.ExclusionIndexes(query)+") AS i(oid)")
			if err != nil {
				return reindexLeft{}, err
			}
			for _, index := range skipped {
				left.exclusions = append(left.exclusions, database+"."+index)
			}
		}
	}
	return left, nil
}
