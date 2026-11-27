package agent

import (
	"cmp"
	"context"
	"errors"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// recordPostgresServing records, just before each managed database of the plan that starts
// does, that its release serves the volume's data: PostgreSQL writes the data from its first
// start, so a deployment that fails after that still refuses a release that cannot open what
// this one wrote. The record keeps the release whose indexes the data holds, so the rebuild
// reindexPostgres finishes stays due; storage with no record gets the one assumePostgresData
// would give it. A release the record refuses is refused again here, so nothing starts on it.
func (j *Journal) recordPostgresServing(p deployment.Plan, starts func(uuid.UUID) bool) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		for _, a := range p.LocalApplications() {
			if !starts(a.ID) || a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
				continue
			}
			serving, ok := domain.PostgresReleaseOf(*a.Source.Image)
			if !ok {
				continue
			}
			volume := a.Mounts[0].VolumeID.String()
			var record postgresData
			err := j.get(tx, postgresDataBucket, volume, &record)
			if errors.Is(err, os.ErrNotExist) {
				if err = j.put(tx, postgresDataBucket, volume, assumedPostgresData(serving)); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if cmp.Or(record.Served, record.Image) == serving.Image {
				continue
			}
			if err = postgresDataRefuses(a, record); err != nil {
				return err
			}
			if err = j.put(tx, postgresDataBucket, volume, postgresData{Image: record.Image, Served: serving.Image}); err != nil {
				return err
			}
		}
		return nil
	})
}

// startServingPostgres starts a managed database's container outside Compose, once the record
// says its release serves the volume's data, as a Compose run that starts one does.
func (e *Engine) startServingPostgres(ctx context.Context, p deployment.Plan, app uuid.UUID, container string) error {
	if err := e.Journal.recordPostgresServing(p, func(id uuid.UUID) bool { return id == app }); err != nil {
		return err
	}
	_, err := e.Docker.ContainerStart(ctx, container, client.ContainerStartOptions{})
	return err
}
