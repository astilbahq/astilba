package agent

import (
	"context"
	"errors"
	"io"
	"os"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// Eligibility and the runtime floor read real daemon output, so this catches any field
// the checks rely on going missing.
func TestNativeTroubleshootingTarget(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	version, err := docker.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !troubleshootingRuntime(version.Components) {
		t.Fatalf("runtime below the floor: %+v", version.Components)
	}
	pull, err := docker.ImagePull(ctx, execution.TestImage, client.ImagePullOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(io.Discard, pull); err != nil {
		t.Fatal(err)
	}
	pull.Close()

	j, plan := testVisualJournal(t)
	if err = j.saveUnit(unitState{Plan: plan, Expected: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Journal: j, Docker: docker}
	for name, test := range map[string]struct {
		host container.HostConfig
		want bool
	}{
		"plain":      {container.HostConfig{}, true},
		"privileged": {container.HostConfig{Privileged: true}, false},
		"capability": {container.HostConfig{CapAdd: []string{"SYS_ADMIN"}}, false},
		"host PID":   {container.HostConfig{PidMode: "host"}, false},
		"host path":  {container.HostConfig{Binds: []string{t.TempDir() + ":/mounted:ro"}}, false},
	} {
		t.Run(name, func(t *testing.T) {
			app := uuid.New()
			created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(app), Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "60"}, Labels: visualLabels(plan, app)}, HostConfig: &test.host})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
			})
			if _, err = engine.TroubleshootingTarget(ctx, plan.UnitID, app, 1); !errors.Is(err, ErrTroubleshootingIneligible) {
				t.Fatal("a stopped container must be refused", err)
			}
			if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			got, err := engine.TroubleshootingTarget(ctx, plan.UnitID, app, 1)
			if test.want && (err != nil || got != created.ID) {
				t.Fatal("eligible container refused", got, err)
			}
			if !test.want && !errors.Is(err, ErrTroubleshootingIneligible) {
				t.Fatal("ineligible container accepted", got, err)
			}
			if _, err = engine.TroubleshootingTarget(ctx, plan.UnitID, app, 2); !errors.Is(err, ErrTroubleshootingIneligible) {
				t.Fatal("an instance beyond the plan must be refused", err)
			}
		})
	}
}
