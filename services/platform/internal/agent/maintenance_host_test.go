package agent

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// A writer's image can declare a health check, which restarts with the container. However
// soon completion follows resumption, it must outlast the first probe.
func TestNativeCompletionWaitsForAWritersHealthCheck(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	journal, _ := testVisualJournal(t)
	app, instance := domain.Application{ID: uuid.New(), Name: "Writer"}, uuid.New()
	plan := deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), Hash: strings.Repeat("a", 64), Recipe: deployment.CrossHostRecipe, Images: map[string]string{app.ID.String(): execution.TestImage}, Specification: domain.Specification{Version: 1, Applications: []domain.Application{app}}, Local: &deployment.LocalPlacement{Primary: true, Instances: map[string][]uuid.UUID{app.ID.String(): {instance}}}}
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(instance), Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "60"}, Labels: visualLabels(plan, app.ID), Healthcheck: &container.HealthConfig{Test: []string{"CMD", "true"}, Interval: 2 * time.Second}}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	stopped, err := docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key := instance.String()
	serving := map[string]string{key: visualFingerprint(stopped.Container, true)}
	if err = journal.saveUnit(unitState{Plan: plan, Expected: serving}); err != nil {
		t.Fatal(err)
	}
	resumeSource := false
	state := crossHostState{ParentID: uuid.New(), GroupHash: strings.Repeat("b", 64), Unit: deployment.DistributedUnit{Plan: plan}, ResumeTarget: &resumeSource, Pause: &writerPause{Serving: serving, Writers: map[string]pausedWriter{key: {ContainerID: created.ID, Running: true}}}}
	command := api.ExecutionCommand{Id: uuid.New()}
	if err = journal.db.Update(func(tx *bolt.Tx) error {
		if err := journal.put(tx, "operations", command.Id.String(), Record{Command: command}); err != nil {
			return err
		}
		return journal.put(tx, "cross-host", "active", state)
	}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Docker: docker, Journal: journal}
	if err = engine.resumeMaintenanceWriters(ctx, command, &state, plan); err != nil {
		t.Fatal(err)
	}
	if err = engine.executeMaintenancePhase(ctx, command, deployment.CrossHostCommand{ParentID: state.ParentID, GroupHash: state.GroupHash, Phase: "complete", Unit: state.Unit}, state); err != nil {
		t.Fatal(err)
	}
	record, err := journal.Record(command.Id)
	if err != nil {
		t.Fatal(err)
	}
	if record.Report.State != "succeeded" {
		t.Fatalf("completion ended %s: %s", record.Report.State, record.Report.Note)
	}
}

func nativeRequireHealthy(t *testing.T, v container.InspectResponse) {
	t.Helper()
	if v.State.Health != nil && v.State.Health.Status != container.Healthy {
		t.Fatalf("%s returned %s", v.Name, v.State.Health.Status)
	}
}

func nativeRequireValue(t *testing.T, name string, value []byte, want string, err error) {
	t.Helper()
	if err != nil || strings.TrimSpace(string(value)) != want {
		t.Fatalf("cutover did not serve %s: %s (%v)", name, value, err)
	}
}
