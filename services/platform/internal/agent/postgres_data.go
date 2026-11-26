package agent

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// postgresData records, for a managed PostgreSQL volume, the release whose indexes its data is
// known to hold. A release whose notes say to rebuild some indexes after updating fixes only new
// entries, so the record stays at the earlier release until those indexes are rebuilt.
type postgresData struct {
	Image string `json:"image"`
}

const postgresDataBucket = "postgres-data"

func (j *Journal) postgresData(volume uuid.UUID) (postgresData, bool, error) {
	var v postgresData
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, postgresDataBucket, volume.String(), &v) })
	if errors.Is(err, os.ErrNotExist) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (j *Journal) savePostgresData(volume uuid.UUID, v postgresData) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, postgresDataBucket, volume.String(), v) })
}

// forgetPostgresData drops a deleted volume's record, so storage later made at the same identity
// starts its own.
func (j *Journal) forgetPostgresData(volume uuid.UUID) error {
	return j.db.Update(func(tx *bolt.Tx) error { return tx.Bucket([]byte(postgresDataBucket)).Delete([]byte(volume.String())) })
}

// postgresReleaseOf resolves an image to its pinned release for postgresDataOpens. Tests replace it
// to add a release of another line, which no pin comes from yet.
var postgresReleaseOf = domain.PostgresReleaseOf

// postgresDataOpens refuses a plan whose managed database on the volume runs a release that cannot
// open the data the journal records there: another major's, another line's or a later release's.
// The record is the release whose indexes the data holds, which an interrupted update leaves behind
// the one that last served it.
// The record follows the volume, not the unit's applied plan, so it also holds for a restore
// target, an upgrade's target and storage a retired environment kept, which the comparison with
// the applied plan never sees. A volume with no record, such as one an earlier agent made, is left
// to that comparison and the volume's major label.
func (j *Journal) postgresDataOpens(p deployment.Plan, volume uuid.UUID) error {
	for _, a := range p.LocalApplications() {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 || a.Mounts[0].VolumeID != volume {
			continue
		}
		record, found, err := j.postgresData(volume)
		if err != nil || !found || record.Image == *a.Source.Image {
			return err
		}
		written, known := postgresReleaseOf(record.Image)
		serving, ok := postgresReleaseOf(*a.Source.Image)
		if !known || !ok {
			return errPostgresVersionChange
		}
		if err = serving.OpensDataFrom(written); err != nil {
			return postgresDataRefusal{application: a.Name, reason: err}
		}
	}
	return nil
}

// postgresDataRefusal is a PostgreSQL version change refused for the data on a volume before any
// service starts, with the reason the owner reads.
type postgresDataRefusal struct {
	application string
	reason      error
}

func (r postgresDataRefusal) Error() string {
	return r.application + " cannot start on its volume: " + r.reason.Error() + "."
}

func (r postgresDataRefusal) Is(target error) bool { return target == errPostgresVersionChange }

// recordPostgresWriters records the release of each managed database in the plan a unit is
// leaving, for a volume with no record yet. That release wrote the data a new plan may serve with
// a later one, which is what makes the later release's reindex due. Runs inside saveUnit, before
// the new plan is the applied one.
func (j *Journal) recordPostgresWriters(tx *bolt.Tx, previous deployment.Plan) error {
	for _, a := range previous.LocalApplications() {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
			continue
		}
		if _, ok := domain.PostgresReleaseOf(*a.Source.Image); !ok {
			continue
		}
		var existing postgresData
		err := j.get(tx, postgresDataBucket, a.Mounts[0].VolumeID.String(), &existing)
		if err == nil {
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err = j.put(tx, postgresDataBucket, a.Mounts[0].VolumeID.String(), postgresData{Image: *a.Source.Image}); err != nil {
			return err
		}
	}
	return nil
}

// recordNewPostgresStorage records a data volume the agent has just created for a managed
// database: the release that will initialise it holds every index it builds.
func (j *Journal) recordNewPostgresStorage(p deployment.Plan, volume uuid.UUID) error {
	for _, a := range p.LocalApplications() {
		if a.Postgres != nil && a.Source.Image != nil && len(a.Mounts) == 1 && a.Mounts[0].VolumeID == volume {
			if _, ok := domain.PostgresReleaseOf(*a.Source.Image); ok {
				return j.savePostgresData(volume, postgresData{Image: *a.Source.Image})
			}
		}
	}
	return nil
}

// reindexBudget bounds a database's rebuild. A lock wait gives up quickly, because REINDEX
// CONCURRENTLY waits for every older transaction, and one left open would hold the host. Each
// session resolves names only in pg_catalog: it runs as the cluster's superuser in databases
// other roles may own, so a function an owner adds to public, such as a closer match for
// format(), must never run with its privileges. A client's options outrank database and role
// settings. Its client is checked every second, so removing the helper ends a waiting rebuild.
const (
	reindexBudget  = time.Hour
	reindexOptions = "PGOPTIONS=-c search_path=pg_catalog,pg_temp -c lock_timeout=10s -c statement_timeout=30min -c client_connection_check_interval=1000"
	reindexKind    = "postgres-reindex-v1"
)

// reindexPage is how many names one listing returns, well within its output limit. Tests lower it
// to page through a few indexes.
var reindexPage = 1000

// reindexPostgres rebuilds, in each running managed database of the plan, the indexes the
// releases between the one whose indexes its data holds and the one serving it say to rebuild.
// Each index is rebuilt concurrently, so the database keeps serving. The update itself has
// already happened and is never undone: a rebuild that does not finish leaves the record at the
// earlier release, so the unit's next deployment, or this one resumed after a restart, tries
// again, and the returned note says so. begin runs once before the first rebuild, to record the
// step. A journal failure is an error, and so is the agent stopping, which leaves the command to
// resume when it restarts.
func (e *Engine) reindexPostgres(ctx context.Context, p deployment.Plan, begin func() error) (string, error) {
	var unfinished, exclusions []string
	for _, a := range p.LocalApplications() {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
			continue
		}
		serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
		if !ok {
			continue
		}
		volume := a.Mounts[0].VolumeID
		record, found, err := e.Journal.postgresData(volume)
		if err != nil {
			return "", err
		}
		written, known := domain.PostgresReleaseOf(record.Image)
		if !found || !known {
			// Storage this agent neither created nor saw in an applied plan, such as a restore
			// target an earlier agent filled: assume the oldest release of its major wrote it.
			written = oldestPostgres(serving.Major())
		}
		if written.Major() == serving.Major() && written.Version < serving.Version {
			if queries := serving.ReindexSince(written); len(queries) > 0 {
				if begin != nil {
					if err = begin(); err != nil {
						return "", err
					}
					begin = nil
				}
				skipped, err := e.rebuildPostgresIndexes(ctx, p, a, serving, queries)
				if ctx.Err() != nil {
					// The agent is stopping: the command resumes when it restarts.
					return "", ctx.Err()
				}
				if err != nil {
					unfinished = append(unfinished, a.Name)
					continue
				}
				exclusions = append(exclusions, skipped...)
			}
		}
		if !found || record.Image != serving.Image {
			if err = e.Journal.savePostgresData(volume, postgresData{Image: serving.Image}); err != nil {
				return "", err
			}
		}
	}
	return reindexNote(unfinished, exclusions), nil
}

// reindexNote tells the owner what the rebuild left, within the room an operation's note has.
func reindexNote(unfinished, exclusions []string) string {
	var parts []string
	if len(unfinished) > 0 {
		parts = append(parts, "Rebuilding the indexes the PostgreSQL release notes name did not finish for "+listSome(unfinished, 240)+"; the data is served on the new release and the next deployment retries it.")
	}
	if len(exclusions) > 0 {
		parts = append(parts, "Rebuild these exclusion-constraint indexes with REINDEX INDEX when writes can pause, as the release notes advise: "+listSome(exclusions, 420)+".")
	}
	return strings.Join(parts, " ")
}

// listSome joins names up to about limit bytes, then counts the rest.
func listSome(names []string, limit int) string {
	slices.Sort(names)
	names = slices.Compact(names)
	out := ""
	for i, name := range names {
		if len(name) > 120 {
			name = name[:120] + "…"
		}
		next := name
		if out != "" {
			next = out + ", " + name
		}
		if len(next) > limit && out != "" {
			return out + " and " + strconv.Itoa(len(names)-i) + " more"
		}
		out = next
	}
	return out
}

func oldestPostgres(major int) domain.PostgresRelease {
	for _, r := range domain.PostgresReleases() {
		if r.Major() == major {
			return r
		}
	}
	return domain.PostgresRelease{}
}

// rebuildPostgresIndexes rebuilds one database's indexes, returning the exclusion-constraint
// indexes left to the owner, or why it did not finish. It runs from a helper in the database's
// network namespace, as backups do, so nothing runs in the database container and removing the
// helper ends whatever the rebuild left running.
func (e *Engine) rebuildPostgresIndexes(ctx context.Context, p deployment.Plan, a domain.Application, serving domain.PostgresRelease, queries []string) ([]string, error) {
	// The container runs the plan's resolved reference to the release's digest.
	v, err := e.Docker.ContainerInspect(ctx, p.ContainerName(a.ID), client.ContainerInspectOptions{})
	if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != a.ID.String() || v.Container.Config.Image != p.Images[a.ID.String()] || !deployment.ResolvedImage(serving.Image, v.Container.Config.Image) || !v.Container.State.Running {
		return nil, errors.New("the database container is not the plan's")
	}
	budget, cancel := context.WithTimeout(ctx, reindexBudget)
	defer cancel()
	name := "astilba-reindex-" + a.ID.String()
	if err = e.removeReindexHelper(budget, name, p); err != nil {
		return nil, err
	}
	labels := visualLabels(p, a.ID)
	labels[labelKind] = reindexKind
	helper, err := e.postgresHelper(budget, name, labels, v.Container.ID, serving.Image, postgresPassword(v.Container.Config.Env))
	if err != nil {
		return nil, err
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
// exclusion-constraint indexes the queries skip, named database.index.
func (e *Engine) runPostgresReindex(ctx context.Context, helper pgClient, a domain.Application, queries []string) ([]string, error) {
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
			if err := e.containerExec(ctx, helper, out, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", database, "-c", "SELECT encode(convert_to(v, 'UTF8'), 'hex') FROM ("+query+") AS q(v)"+after+" ORDER BY v COLLATE \"C\" LIMIT "+strconv.Itoa(reindexPage)); err != nil {
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
			if err := e.containerExec(ctx, helper, io.Discard, "psql", "-XAq", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", connection, "-c", statement); err != nil {
				return err
			}
		}
		return nil
	}
	leftovers := func(connection string) ([]string, error) {
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		return lines(connection, "SELECT format('DROP INDEX CONCURRENTLY IF EXISTS %s', i.oid::regclass) FROM ("+domain.ReindexLeftovers+") AS i(oid)")
	}
	// inline-sql: runs in a managed customer database through psql, not in the platform database
	databases, err := lines(a.Postgres.Database, "SELECT datname::text FROM pg_database WHERE datallowconn AND NOT datistemplate")
	if err != nil {
		return nil, err
	}
	var exclusions []string
	for _, database := range databases {
		// psql reads its -d value as a connection string when it holds '='; quote it as one.
		connection := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(database) + "'"
		statements, err := leftovers(connection)
		if err != nil {
			return nil, err
		}
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			rebuild, err := lines(connection, "SELECT format('REINDEX INDEX CONCURRENTLY %s', i.oid::regclass) FROM ("+query+") AS i(oid)")
			if err != nil {
				return nil, err
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
			return nil, err
		}
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			skipped, err := lines(connection, "SELECT i.oid::regclass::text FROM ("+domain.ExclusionIndexes(query)+") AS i(oid)")
			if err != nil {
				return nil, err
			}
			for _, index := range skipped {
				exclusions = append(exclusions, database+"."+index)
			}
		}
	}
	return exclusions, nil
}
