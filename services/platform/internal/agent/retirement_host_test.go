package agent

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
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

// Retirement removes a unit's containers, including release and job helpers, and every network it
// made, including Compose networks, while its named volume and other units stay. It can run again,
// and it refuses before any effect when something labelled as the unit belongs to someone else.
func TestRetirementRemovesUnitAndKeepsVolumes(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker retirement")
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
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { j.Close() })
	server, env, app, volume, composeNetwork := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	if err = j.SetConfig(Config{ServerID: server, MachineID: uuid.New(), Role: "deployment"}); err != nil {
		t.Fatal(err)
	}
	image := execution.TestImage
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}},
		Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/data"}}}}}
	plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, Specification: spec, Images: map[string]string{app.String(): image}, ExpiresAt: time.Now().Add(time.Hour)}.WithHash()
	other := plan
	other.EnvironmentID, other.UnitID = uuid.New(), uuid.New()

	// Everything is removed by name at the end, after checking it carries this test's identities.
	containers, networks := []string{}, []string{}
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for _, name := range containers {
			if v, err := docker.ContainerInspect(cleanup, name, client.ContainerInspectOptions{}); err == nil && (v.Container.Config.Labels[labelServer] == server.String()) {
				docker.ContainerRemove(cleanup, v.Container.ID, client.ContainerRemoveOptions{Force: true})
			}
		}
		for _, name := range networks {
			if v, err := docker.NetworkInspect(cleanup, name, client.NetworkInspectOptions{}); err == nil && v.Network.Labels[labelServer] == server.String() {
				docker.NetworkRemove(cleanup, v.Network.ID, client.NetworkRemoveOptions{})
			}
		}
		if v, err := docker.VolumeInspect(cleanup, deployment.VolumeName(volume), client.VolumeInspectOptions{}); err == nil && v.Volume.Labels[labelServer] == server.String() {
			docker.VolumeRemove(cleanup, v.Volume.Name, client.VolumeRemoveOptions{})
		}
	})
	network := func(p deployment.Plan, id uuid.UUID) string {
		t.Helper()
		name := deployment.NetworkName(id)
		networks = append(networks, name)
		if _, err := docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: visualLabels(p, id)}); err != nil {
			t.Fatal(err)
		}
		return name
	}
	start := func(name string, labels map[string]string, attached string, mounts []mount.Mount) {
		t.Helper()
		containers = append(containers, name)
		made, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: image, Cmd: []string{"sleep", "3600"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(attached), Mounts: mounts}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = docker.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	unitNetwork := network(plan, plan.UnitID)
	composeName := network(plan, composeNetwork)
	if _, err = docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(volume), Driver: "local", Labels: visualLabels(plan, volume)}); err != nil {
		t.Fatal(err)
	}
	service := "astilba-" + uuid.NewString()
	start(service, visualLabels(plan, app), unitNetwork, []mount.Mount{{Type: mount.TypeVolume, Source: deployment.VolumeName(volume), Target: "/data"}})
	release := visualLabels(plan, app)
	release[labelKind] = "release-command-v1"
	start("astilba-release-"+uuid.NewString(), release, composeName, nil)
	neighbour := "astilba-" + uuid.NewString()
	start(neighbour, visualLabels(other, uuid.New()), "none", nil)

	if err = j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	schedule := func(environment uuid.UUID) uuid.UUID {
		t.Helper()
		policy := backup.Policy{ID: uuid.New(), WorkspaceID: uuid.New(), EnvironmentID: environment, ServerID: server, Generation: 1, Method: "volume", Schedule: backup.Schedule{Hour: 1, Timezone: "UTC"}, Retain: 1}
		if err := j.saveSchedule(scheduleState{Installation: backup.Installation{Policy: policy}}); err != nil {
			t.Fatal(err)
		}
		return policy.ID
	}
	retiredSchedule, keptSchedule := schedule(env), schedule(other.EnvironmentID)

	engine := &Engine{Journal: j, Docker: docker}
	generation := int64(0)
	retire := func() Record {
		t.Helper()
		generation++
		payload := deployment.Payload{Action: "retire", Plan: plan}
		raw, err := stablejson.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		value := jsontext.Value(raw)
		// A deadline long past: retirement waits for a server that was offline.
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: plan.UnitID, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Deadline: time.Now().Add(-time.Hour), Visual: &value}
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
		return r
	}
	present := func(name string) bool {
		t.Helper()
		_, err := docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil && !errdefs.IsNotFound(err) {
			t.Fatal(err)
		}
		return err == nil
	}

	for attempt := range 2 {
		if r := retire(); r.Report.State != "succeeded" {
			t.Fatalf("retirement %d: %+v", attempt, r.Report)
		}
		for _, name := range containers[:2] {
			if present(name) {
				t.Fatalf("container %s survived retirement", name)
			}
		}
		for _, name := range []string{unitNetwork, composeName} {
			if _, err := docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
				t.Fatalf("network %s survived retirement: %v", name, err)
			}
		}
		if _, err := docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{}); err != nil {
			t.Fatal("the named volume was not kept", err)
		}
		if !present(neighbour) {
			t.Fatal("another unit's container was removed")
		}
		if _, err := j.schedule(retiredSchedule); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("the environment's schedule was kept", err)
		}
		if _, err := j.schedule(keptSchedule); err != nil {
			t.Fatal("another environment's schedule was removed", err)
		}
		if u, err := j.unit(plan.UnitID); err != nil || u.Plan.Hash != "" {
			t.Fatal("the unit's journal record was kept", err)
		}
	}

	// A container labelled as this unit but for another environment stops everything before an effect.
	service = "astilba-" + uuid.NewString()
	start(service, visualLabels(plan, app), "none", nil)
	foreign := visualLabels(plan, app)
	foreign[labelEnvironment] = uuid.NewString()
	intruder := "astilba-" + uuid.NewString()
	start(intruder, foreign, "none", nil)
	if r := retire(); r.Report.State != "needs_attention" || !present(service) || !strings.Contains(r.Report.Note, "Nothing was removed") {
		t.Fatalf("an ownership mismatch was not refused before effects: %+v", r.Report)
	}
}
