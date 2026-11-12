package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// A data volume records the PostgreSQL major that first ran on it, so a host whose journal no
// longer knows the unit, as after retirement, still refuses to start another major on it.
func TestRetiredPostgresVolumeRefusesAnotherMajor(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker volume")
	}
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	if host == "" {
		t.Fatal("PLAT_APP_DOCKER_HOST must explicitly select a disposable daemon")
	}
	ctx := t.Context()
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	plan := func(image string, volume uuid.UUID) deployment.Plan {
		return deployment.Plan{ServerID: uuid.Nil, EnvironmentID: uuid.Nil, UnitID: uuid.Nil, Specification: domain.Specification{Version: 1,
			Applications: []domain.Application{{ID: uuid.New(), Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}}}}
	}
	volume := uuid.New()
	initialised := plan(domain.Postgres17Image, volume)
	initialised.ServerID, initialised.EnvironmentID, initialised.UnitID = uuid.New(), uuid.New(), uuid.New()
	name := deployment.VolumeName(volume)
	if _, err = docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: volumeLabels(initialised, volume)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if v, err := docker.VolumeInspect(cleanup, name, client.VolumeInspectOptions{}); err == nil && v.Volume.Labels[labelServer] == initialised.ServerID.String() {
			docker.VolumeRemove(cleanup, name, client.VolumeRemoveOptions{})
		}
	})
	engine := &Engine{Docker: docker}
	if err = engine.postgresVolumesMatch(ctx, initialised); err != nil {
		t.Fatal("the same major was refused", err)
	}
	upgraded := initialised
	upgraded.Specification = plan(domain.PostgresImage, volume).Specification
	if err = engine.postgresVolumesMatch(ctx, upgraded); !errors.Is(err, errPostgresVersionChange) {
		t.Fatal("another major was not refused", err)
	}
}
