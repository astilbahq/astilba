package agent

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// A data volume records the PostgreSQL major that first ran on it, so a host whose journal no
// longer knows the unit, as after retirement, still refuses to start another major on it. A restore
// target records the major of the archive restored into it in the same way.
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
	journal, identity := testVisualJournal(t)
	e := &Engine{Journal: journal, Docker: docker}
	plan := func(image string, volume uuid.UUID) deployment.Plan {
		p := identity
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{},
			Applications: []domain.Application{{ID: uuid.New(), Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(image)}}}},
			Volumes:      []domain.Volume{{ID: volume, Name: "Data"}}}
		return p
	}
	cleanup := func(name string, remove func(context.Context) error) {
		t.Cleanup(func() {
			clean, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			if err := remove(clean); err != nil {
				t.Errorf("remove %s: %v", name, err)
			}
		})
	}
	network := deployment.NetworkName(identity.UnitID)
	cleanup(network, func(ctx context.Context) error {
		n, err := docker.NetworkInspect(ctx, network, client.NetworkInspectOptions{})
		if err != nil || n.Network.Labels[labelServer] != identity.ServerID.String() {
			return nil
		}
		_, err = docker.NetworkRemove(ctx, network, client.NetworkRemoveOptions{})
		return err
	})
	for _, tc := range []struct {
		name   string
		create func(volume uuid.UUID) map[string]string
	}{
		{"created by a deployment", func(volume uuid.UUID) map[string]string {
			return volumeLabels(plan(domain.Postgres17Image, volume), volume)
		}},
		{"restored from an archive", func(volume uuid.UUID) map[string]string {
			return restoreTargetLabels(identity, volume, backup.Manifest{Method: "postgres-logical", Image: domain.Postgres17Image})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			volume := uuid.New()
			name := deployment.VolumeName(volume)
			if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: tc.create(volume)}); err != nil {
				t.Fatal(err)
			}
			cleanup(name, func(ctx context.Context) error {
				if v, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil || v.Volume.Labels[labelServer] != identity.ServerID.String() {
					return nil
				}
				_, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
				return err
			})
			if err := e.ensureStorage(ctx, plan(domain.Postgres17Image, volume)); err != nil {
				t.Fatal("the same major was refused", err)
			}
			if err := e.ensureStorage(ctx, plan(domain.PostgresImage, volume)); !errors.Is(err, errPostgresVersionChange) || storageState(err) != "failed" {
				t.Fatal("another major was not refused", err)
			}
		})
	}
}
