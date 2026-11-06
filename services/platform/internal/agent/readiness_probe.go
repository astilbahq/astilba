package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// errNotReady means the last probe ran and did not pass, unlike a failure to run one.
var errNotReady = errors.New("HTTP readiness check did not pass")

func (e *Engine) waitHTTPInstance(ctx context.Context, c api.ExecutionCommand, p deployment.Plan, a domain.Application, instance uuid.UUID) error {
	probe := a.ID
	if p.Recipe == deployment.ReplicaRecipe || p.Recipe == deployment.CrossHostRecipe {
		probe = instance
	}
	name := "astilba-health-" + c.Id.String() + "-" + probe.String()
	// A stopping agent may not reach this probe again, and nothing else removes it.
	cleanup := context.WithoutCancel(ctx)
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		if v.Container.Config.Labels["com.astilba.health"] != c.Id.String() || (v.Container.Config.Labels[labelServer] != p.ServerID.String() || v.Container.Config.Labels["com.astilba.unit"] != p.UnitID.String() || v.Container.Config.Labels[labelKind] != "health-v1") {
			return errors.New("health-check ownership conflict")
		}
		_, _ = e.Docker.ContainerRemove(cleanup, v.Container.ID, client.ContainerRemoveOptions{Force: true})
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	url := fmt.Sprintf("http://%s:%d%s", deployment.ServiceName(instance), a.Health.Port, a.Health.Path)
	outcome := errNotReady
	for range 20 {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"wget", "-T", "2", "-q", "--spider", url}, Labels: healthLabels(c, p)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.UnitID)), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, Resources: container.Resources{Memory: 32 << 20, PidsLimit: ptr(int64(16))}}})
		if err != nil {
			return err
		}
		_, err = e.Docker.ContainerStart(ctx, v.ID, client.ContainerStartOptions{})
		outcome = err
		if err == nil {
			wait := e.Docker.ContainerWait(ctx, v.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
			select {
			case s := <-wait.Result:
				if s.StatusCode == 0 {
					_, _ = e.Docker.ContainerRemove(cleanup, v.ID, client.ContainerRemoveOptions{})
					return nil
				}
				outcome = errNotReady
			case outcome = <-wait.Error:
			case <-ctx.Done():
			}
		}
		_, _ = e.Docker.ContainerRemove(cleanup, v.ID, client.ContainerRemoveOptions{Force: true})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return outcome
}
