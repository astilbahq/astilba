package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

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

// errPostgresReindex is a database that serves on its new release before the indexes that
// release's notes name were rebuilt. The record keeps the earlier release, so the next
// deployment of the unit tries again.
var errPostgresReindex = errors.New("PostgreSQL serves its data on the new release, but rebuilding the indexes the release notes name did not finish. The next deployment retries it")

// reindexPostgres rebuilds, in each running managed database of the plan, the indexes the
// releases between the one that wrote its data and the one serving it say to rebuild. Each index
// is rebuilt concurrently, so the database keeps serving reads and writes.
func (e *Engine) reindexPostgres(ctx context.Context, p deployment.Plan) error {
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
			return err
		}
		written, known := domain.PostgresReleaseOf(record.Image)
		if found && known && written.Major() == serving.Major() && written.Version < serving.Version {
			if queries := serving.ReindexSince(written); len(queries) > 0 {
				v, err := e.Docker.ContainerInspect(ctx, p.ContainerName(a.ID), client.ContainerInspectOptions{})
				if err != nil || !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != a.ID.String() || v.Container.Config.Image != serving.Image || !v.Container.State.Running {
					return errPostgresReindex
				}
				if err = e.runPostgresReindex(ctx, v.Container.ID, a, queries); err != nil {
					return errors.Join(errPostgresReindex, err)
				}
			}
		}
		// New storage starts its record at the release that initialised it.
		if !found || record.Image != serving.Image {
			if err = e.Journal.savePostgresData(volume, postgresData{Image: serving.Image}); err != nil {
				return err
			}
		}
	}
	return nil
}

// runPostgresReindex runs each query in every database that accepts connections. A query returns
// index OIDs; psql turns each into REINDEX INDEX CONCURRENTLY, which cannot run in a transaction.
func (e *Engine) runPostgresReindex(ctx context.Context, container string, a domain.Application, queries []string) error {
	raw, err := e.pgQuery(ctx, container, a, "SELECT datname FROM pg_database WHERE datallowconn AND NOT datistemplate ORDER BY datname")
	if err != nil {
		return err
	}
	for database := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if database == "" {
			continue
		}
		for _, query := range queries {
			script := "SELECT format('REINDEX INDEX CONCURRENTLY %s', i.oid::regclass) FROM (" + query + ") AS i(oid) ORDER BY 1 \\gexec\n"
			if err = e.dataTool(ctx, strings.NewReader(script), io.Discard, "exec", "-i", container, "psql", "-XAt", "-v", "ON_ERROR_STOP=1", "-U", a.Postgres.Username, "-d", database, "-f", "-"); err != nil {
				return err
			}
		}
	}
	return nil
}
