package agent

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// PLAT-337: a restore target, or any volume the unit's applied plan does not mount, holds data a
// release the journal records wrote. A deployment that starts a release on it that cannot open that
// data, from another major or another line or an earlier release, is refused before any service
// starts, with the reason, and the record stays. The volumes carry no major label, as one an
// earlier agent made, so the record alone refuses them.
func TestPostgresVolumeRefusesAReleaseThatCannotOpenItsData(t *testing.T) {
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

	pg17, _ := domain.CurrentPostgres(17)
	pg18, _ := domain.CurrentPostgres(18)
	earlier := oldestPostgres(18)
	if !earlier.Retired() {
		t.Fatal("PostgreSQL 18 has no earlier release to move back to")
	}
	// No pin comes from another base image line yet. This later minor's line sorts text with
	// another ICU; each release has its own version.
	otherLine := pg18
	otherLine.Version++
	otherLine.Image, otherLine.Line = "postgres:"+otherLine.Name()+"-alpine3.24@sha256:"+strings.Repeat("a", 64), "alpine3.24"
	postgresReleaseOf = func(image string) (domain.PostgresRelease, bool) {
		if image == otherLine.Image {
			return otherLine, true
		}
		return domain.PostgresReleaseOf(image)
	}
	t.Cleanup(func() { postgresReleaseOf = domain.PostgresReleaseOf })

	plan := func(serving domain.PostgresRelease, volume uuid.UUID) deployment.Plan {
		p := identity
		image := serving.Image
		p.Specification = domain.Specification{Version: 1, Variables: []domain.Variable{},
			Applications: []domain.Application{{ID: uuid.New(), Name: "Database", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app"}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: serving.Mount}}}},
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
		name             string
		written, serving domain.PostgresRelease
		refused          string
	}{
		{name: "no record", serving: pg18},
		{name: "the same release", written: pg18, serving: pg18},
		{name: "a later release of the line", written: earlier, serving: pg18},
		{name: "another major", written: pg17, serving: pg18, refused: "Database cannot start on its volume: PostgreSQL " + pg18.Name() + " cannot open data PostgreSQL " + pg17.Name() + " wrote."},
		{name: "another line", written: pg18, serving: otherLine, refused: "their sorting libraries differ"},
		{name: "an earlier release", written: pg18, serving: earlier, refused: "a database never moves to an earlier release"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			volume := uuid.New()
			p := plan(tc.serving, volume)
			name := deployment.VolumeName(volume)
			if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: name, Driver: "local", Labels: visualLabels(p, volume)}); err != nil {
				t.Fatal(err)
			}
			cleanup(name, func(ctx context.Context) error {
				if v, err := docker.VolumeInspect(ctx, name, client.VolumeInspectOptions{}); err != nil || v.Volume.Labels[labelServer] != identity.ServerID.String() {
					return nil
				}
				_, err := docker.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
				return err
			})
			if tc.written.Image != "" {
				if err := journal.savePostgresData(volume, postgresData{Image: tc.written.Image}); err != nil {
					t.Fatal(err)
				}
			}
			err := e.ensureStorage(ctx, p)
			if tc.refused == "" {
				if err != nil {
					t.Fatal("a release that opens the data was refused:", err)
				}
				return
			}
			if !errors.Is(err, errPostgresVersionChange) || storageState(err) != "failed" || !strings.Contains(err.Error(), tc.refused) {
				t.Fatalf("got %v, want a refusal that says %q", err, tc.refused)
			}
			if record, found, err := journal.postgresData(volume); err != nil || !found || record.Image != tc.written.Image {
				t.Fatalf("the refusal changed the record of what wrote the data: %+v, %v", record, err)
			}
		})
	}
}
