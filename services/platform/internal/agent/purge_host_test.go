package agent

import (
	"context"
	"encoding/json/jsontext"
	"os"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// A purge deletes exactly the retained volumes it names. It checks every one before deleting any,
// so a volume labelled for another environment stops it with nothing deleted, it keeps a volume a
// container still uses, it counts a volume that is already gone as deleted, and one that arrives
// after its deadline does nothing.
func TestPurgeDeletesOnlyOwnedUnusedVolumes(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker purge")
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
	if _, err = docker.ImageInspect(ctx, execution.TestImage); err != nil {
		t.Fatalf("fixture image %s is unavailable: %v", execution.TestImage, err)
	}
	server := uuid.New()
	// A purge that needs attention stays open in its journal, so each such case gets its own.
	journal := func() *Journal {
		t.Helper()
		state := t.TempDir()
		if err := os.Chmod(state, 0700); err != nil {
			t.Fatal(err)
		}
		j, err := Open(state)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { j.Close() })
		if err = j.SetConfig(Config{ServerID: server, MachineID: uuid.New(), Role: "deployment"}); err != nil {
			t.Fatal(err)
		}
		return j
	}
	empty := domain.Specification{Version: 1, Applications: []domain.Application{}, Variables: []domain.Variable{}}
	plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: empty, Images: map[string]string{}, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	other := plan
	other.EnvironmentID, other.UnitID = uuid.New(), uuid.New()

	kept, second, foreign, used, missing := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	containers := []string{}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, name := range containers {
			if v, err := docker.ContainerInspect(cleanup, name, client.ContainerInspectOptions{}); err == nil && v.Container.Config.Labels[labelServer] == server.String() {
				docker.ContainerRemove(cleanup, v.Container.ID, client.ContainerRemoveOptions{Force: true})
			}
		}
		for _, id := range []uuid.UUID{kept, second, foreign, used} {
			if v, err := docker.VolumeInspect(cleanup, deployment.VolumeName(id), client.VolumeInspectOptions{}); err == nil && v.Volume.Labels[labelServer] == server.String() {
				docker.VolumeRemove(cleanup, v.Volume.Name, client.VolumeRemoveOptions{})
			}
		}
	})
	volume := func(owner deployment.Plan, id uuid.UUID) {
		t.Helper()
		if _, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(id), Driver: "local", Labels: visualLabels(owner, id)}); err != nil {
			t.Fatal(err)
		}
	}
	volume(plan, kept)
	volume(plan, second)
	volume(other, foreign)
	volume(plan, used)
	user := "astilba-" + uuid.NewString()
	containers = append(containers, user)
	made, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: user, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: visualLabels(plan, uuid.New())}, HostConfig: &container.HostConfig{NetworkMode: "none", Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(used), Target: "/data"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = docker.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}

	generation := int64(0)
	purge := func(j *Journal, deadline time.Time, volumes ...uuid.UUID) api.AgentReport {
		t.Helper()
		engine := &Engine{Journal: j, Docker: docker}
		generation++
		payload := deployment.Payload{Action: "purge", Plan: plan, VolumeIDs: volumes}
		raw, err := stablejson.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		value := jsontext.Value(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: plan.EnvironmentID, ResourceId: plan.UnitID, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Deadline: deadline, Visual: &value}
		if err := engine.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := j.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return r.Report
	}
	exists := func(id uuid.UUID) bool {
		t.Helper()
		_, err := docker.VolumeInspect(ctx, deployment.VolumeName(id), client.VolumeInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}

	inTime := time.Now().Add(time.Hour)
	if r := purge(journal(), inTime, kept, foreign); r.State != "needs_attention" || !exists(kept) || !exists(foreign) {
		t.Fatalf("a purge naming another environment's volume: %+v", r)
	}
	if r := purge(journal(), inTime, used); r.State != "needs_attention" || !exists(used) {
		t.Fatalf("a purge of a volume in use: %+v", r)
	}
	j := journal()
	if r := purge(j, time.Now().Add(-time.Hour), kept, second); r.State == "succeeded" || !exists(kept) || !exists(second) {
		t.Fatalf("a late purge: %+v", r)
	}
	for attempt := range 2 {
		if r := purge(j, inTime, kept, second, missing); r.State != "succeeded" || exists(kept) || exists(second) {
			t.Fatalf("purge %d: %+v", attempt, r)
		}
	}
	if !exists(foreign) || !exists(used) {
		t.Fatal("a purge deleted a volume it did not name")
	}
}
