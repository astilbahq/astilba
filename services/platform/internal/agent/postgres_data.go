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
// CONCURRENTLY waits for every older transaction, and one left open would hold the host.
const (
	reindexApplication = "astilba-reindex"
	// Sessions resolve names only in pg_catalog and name themselves, so a cancellation finds them.
	reindexOptions      = "PGOPTIONS=-c search_path=pg_catalog,pg_temp"
	reindexBudget       = time.Hour
	reindexSessionLimit = "SET lock_timeout = '10s';\nSET statement_timeout = '30min';\n"
)

// reindexPostgres rebuilds, in each running managed database of the plan, the indexes the
// releases between the one whose indexes its data holds and the one serving it say to rebuild.
// Each index is rebuilt concurrently, so the database keeps serving. The update itself has
// already happened and is never undone: a rebuild that does not finish leaves the record at the
// earlier release, so the unit's next deployment, or this one resumed after a restart, tries
// again, and the returned note says so. begin runs once before the first rebuild, to record the
// step. Only a journal failure is an error.
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
// indexes left to the owner, or why it did not finish.
func (e *Engine) rebuildPostgresIndexes(ctx context.Context, p deployment.Plan, a domain.Application, serving domain.PostgresRelease, queries []string) ([]string, error) {
	// The container runs the plan's resolved reference to the release's digest.
	v, err := e.Docker.ContainerInspect(ctx, p.ContainerName(a.ID), client.ContainerInspectOptions{})
	if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != a.ID.String() || v.Container.Config.Image != p.Images[a.ID.String()] || !deployment.ResolvedImage(serving.Image, v.Container.Config.Image) || !v.Container.State.Running {
		return nil, errors.New("the database container is not the plan's")
	}
	budget, cancel := context.WithTimeout(ctx, reindexBudget)
	defer cancel()
	exclusions, err := e.runPostgresReindex(budget, v.Container.ID, a, queries)
	if err != nil && budget.Err() != nil {
		// The docker client ending does not stop psql or its backend in the container.
		e.cancelPostgresReindex(v.Container.ID, a)
	}
	return exclusions, err
}

// cancelPostgresReindex cancels the rebuild's own sessions, found by their application name.
func (e *Engine) cancelPostgresReindex(container string, a domain.Application) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// inline-sql: runs in a managed customer database through psql, not in the platform database
	_ = e.dataTool(ctx, nil, io.Discard, "exec", "-e", reindexOptions, container, "psql", "-XAt", "-U", a.Postgres.Username, "-d", a.Postgres.Database, "-c", "SELECT pg_cancel_backend(pid) FROM pg_stat_activity WHERE application_name = '"+reindexApplication+"' AND pid <> pg_backend_pid()")
}

// runPostgresReindex runs each query in every database that accepts connections, after dropping
// the leftovers of an interrupted run. A query returns index OIDs; psql turns each into REINDEX
// INDEX CONCURRENTLY, which cannot run in a transaction. It returns the exclusion-constraint
// indexes the queries skip, named database.index.
func (e *Engine) runPostgresReindex(ctx context.Context, container string, a domain.Application, queries []string) ([]string, error) {
	// Every session runs as the cluster's superuser in databases other roles may own, so it
	// resolves names only in pg_catalog: a function an owner adds to public, such as a closer
	// match for format(), must never run with these privileges. A connection's own options
	// outrank database and role settings.
	psql := func(input bool, database string, args ...string) []string {
		out := []string{"exec", "-e", reindexOptions, "-e", "PGAPPNAME=" + reindexApplication}
		if input {
			out = append(out, "-i")
		}
		return append(append(out, container, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", database), args...)
	}
	listing := &boundedOutput{limit: 65536, stopOnLimit: true}
	// Names travel hex-encoded, so a name holding a newline or an equals sign stays one name.
	// inline-sql: runs in a managed customer database through psql, not in the platform database
	if err := e.dataTool(ctx, nil, listing, psql(false, a.Postgres.Database, "-c", "SELECT encode(convert_to(datname, 'UTF8'), 'hex') FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")...); err != nil {
		return nil, err
	}
	raw := listing.b.Bytes()
	var exclusions []string
	for encoded := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		name, err := hex.DecodeString(encoded)
		if err != nil || len(name) == 0 {
			continue
		}
		database := string(name)
		// psql reads its -d value as a connection string when it holds '='; quote it as one.
		connection := "dbname='" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(database) + "'"
		// inline-sql: runs in a managed customer database through psql, not in the platform database
		script := reindexSessionLimit + "SELECT format('DROP INDEX CONCURRENTLY IF EXISTS %s', i.oid::regclass) FROM (" + domain.ReindexLeftovers + ") AS i(oid) ORDER BY 1 \\gexec\n"
		for _, query := range queries {
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			script += "SELECT format('REINDEX INDEX CONCURRENTLY %s', i.oid::regclass) FROM (" + query + ") AS i(oid) ORDER BY 1 \\gexec\n"
		}
		if err = e.dataTool(ctx, strings.NewReader(script), io.Discard, psql(true, connection, "-f", "-")...); err != nil {
			return nil, err
		}
		for _, query := range queries {
			out := &boundedOutput{limit: 4096}
			// inline-sql: runs in a managed customer database through psql, not in the platform database
			if err = e.dataTool(ctx, nil, out, psql(false, connection, "-c", "SELECT i.oid::regclass FROM ("+domain.ExclusionIndexes(query)+") AS i(oid) ORDER BY 1")...); err != nil {
				return nil, err
			}
			for index := range strings.SplitSeq(strings.TrimSpace(out.b.String()), "\n") {
				if index != "" {
					exclusions = append(exclusions, database+"."+index)
				}
			}
		}
	}
	return exclusions, nil
}
