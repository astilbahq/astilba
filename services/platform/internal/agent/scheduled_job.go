package agent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

var errJobCleanup = errors.New("job cleanup unconfirmed")

// errElevatedJob refuses a job whose source is privileged, or shares a volume or network with
// a service that is: its command could drive that service and so reach the host.
var errElevatedJob = errors.New("scheduled jobs cannot run in a privileged service or one that shares a volume or network with it")

func (e *Engine) runContainerJob(ctx context.Context, s *scheduleState, r *backup.Run) (result error) {
	p := s.Installation.Policy
	if err := e.checkBackupSource(ctx, p); err != nil {
		return err
	}
	a, err := sourceApp(p)
	if err != nil || a.ServiceKind() != "worker" {
		return errors.New("scheduled jobs require a worker")
	}
	source, err := e.sourceContainer(ctx, p, a.ID)
	if err != nil {
		return err
	}
	if hostAuthority(source.HostConfig) {
		return errElevatedJob
	}
	peer, err := e.hostPeer(ctx, source)
	if err != nil {
		return err
	}
	if peer {
		return errElevatedJob
	}
	// The job reaches exactly what its source does: the same networks, or none.
	networks, err := jobNetworks(p.Plan, source)
	if err != nil {
		return err
	}
	mode, endpoints := container.NetworkMode("none"), map[string]*network.EndpointSettings{}
	for _, name := range networks {
		endpoints[name] = &network.EndpointSettings{}
	}
	if len(networks) > 0 {
		mode = container.NetworkMode(networks[0])
	}
	labels := visualLabels(p.Plan, a.ID)
	labels[labelKind] = "scheduled-job-v1"
	labels["com.astilba.run"] = r.ID.String()
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: "astilba-job-" + r.ID.String(), Config: &container.Config{Image: p.Plan.Images[a.ID.String()], Cmd: p.Command, Env: source.Config.Env, Labels: labels, WorkingDir: source.Config.WorkingDir, User: source.Config.User}, HostConfig: &container.HostConfig{NetworkMode: mode, ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Mounts: source.HostConfig.Mounts, Resources: container.Resources{Memory: 256 << 20, PidsLimit: ptr(int64(64))}}, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: endpoints}})
	if err != nil {
		return err
	}
	defer func() {
		clean, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		current, cleanupErr := e.Docker.ContainerInspect(clean, created.ID, client.ContainerInspectOptions{})
		if cleanupErr == nil {
			owned := true
			for key, want := range labels {
				if current.Container.Config.Labels[key] != want {
					owned = false
					break
				}
			}
			if !owned {
				cleanupErr = errors.New("job cleanup ownership changed")
			} else {
				if current.Container.State.Running {
					_, cleanupErr = e.Docker.ContainerStop(clean, created.ID, client.ContainerStopOptions{Timeout: ptr(5)})
					if cleanupErr == nil {
						current, cleanupErr = e.Docker.ContainerInspect(clean, created.ID, client.ContainerInspectOptions{})
					}
				}
				if cleanupErr == nil {
					if current.Container.State.Running {
						cleanupErr = errors.New("job remains running")
					} else {
						code := current.Container.State.ExitCode
						r.ExitCode = &code
						_, cleanupErr = e.Docker.ContainerRemove(clean, created.ID, client.ContainerRemoveOptions{})
					}
				}
			}
		}
		if cleanupErr != nil {
			result = errors.Join(result, errJobCleanup, cleanupErr)
		}
	}()
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	for {
		v, err := e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if !v.Container.State.Running {
			code := v.Container.State.ExitCode
			r.ExitCode = &code
			if code != 0 {
				return fmt.Errorf("job exit %d", code)
			}
			return nil
		}
		if !wait(ctx, time.Second) {
			return ctx.Err()
		}
	}
}
