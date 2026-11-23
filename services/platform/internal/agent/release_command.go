package agent

import (
	"context"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	"time"
)

func releaseCommandName(c api.ExecutionCommand) string {
	return "astilba-release-command-" + c.Id.String()
}
func (e *Engine) removeReleaseCommand(ctx context.Context, c api.ExecutionCommand, p deployment.Payload) error {
	v, err := e.Docker.ContainerInspect(ctx, releaseCommandName(c), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if v.Container.Config.Labels[labelKind] != "release-command-v1" || v.Container.Config.Labels["com.astilba.operation"] != c.Id.String() || v.Container.Config.Labels[labelServer] != p.Plan.ServerID.String() || v.Container.Config.Labels[labelEnvironment] != p.Plan.EnvironmentID.String() || v.Container.Config.Labels["com.astilba.unit"] != p.Plan.UnitID.String() || v.Container.Config.Labels[labelResource] != p.Plan.Release.ApplicationID.String() || v.Container.Config.Labels["com.astilba.plan"] != p.Plan.Hash || v.Container.Config.Image != p.Plan.Images[p.Plan.Release.ApplicationID.String()] {
		return errors.New("release command ownership conflict")
	}
	_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	return err
}
func (e *Engine) releaseCommand(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, s *releaseState) (string, error) {
	return e.runReleaseCommand(ctx, c, p, s, e.Journal.saveRelease)
}
func (e *Engine) runReleaseCommand(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, s *releaseState, save func(releaseState) error) (string, error) {
	name := releaseCommandName(c)
	options := p.Plan.Release.Options
	id := p.Plan.Release.ApplicationID
	if s.Phase == "ready" {
		if _, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			return "uncertain", nil
		}
		env, err := releaseCommandEnvironment(p, p.Plan.HostName(id))
		if err != nil {
			return "", err
		}
		labels := visualLabels(p.Plan, id)
		labels[labelKind] = "release-command-v1"
		labels["com.astilba.operation"] = c.Id.String()
		labels["com.astilba.plan"] = p.Plan.Hash
		_, err = e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: p.Plan.Images[id.String()], Entrypoint: options.Command, Cmd: []string{}, Env: env, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.Plan.UnitID)), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=64m"}, LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}}, Resources: container.Resources{Memory: 256 << 20, NanoCPUs: 1e9, PidsLimit: ptr(int64(64))}}})
		if err != nil {
			return "", err
		}
		s.Phase = "command-starting"
		s.HookDeadline = time.Now().UTC().Add(time.Duration(options.CommandTimeoutSeconds) * time.Second)
		if err = save(*s); err != nil {
			return "", err
		}
		e.point("release-command-intent")
		// A lost start response must not abandon a running command's deadline.
		// Inspect the same container below; never replay its start intent.
		if _, err = e.Docker.ContainerStart(ctx, name, client.ContainerStartOptions{}); err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		e.point("release-command-started")
	}
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err != nil {
		if errdefs.IsNotFound(err) {
			return "uncertain", nil
		}
		return "", err
	}
	if v.Container.Config.Labels[labelKind] != "release-command-v1" || v.Container.Config.Labels["com.astilba.operation"] != c.Id.String() || v.Container.Config.Labels["com.astilba.plan"] != p.Plan.Hash || v.Container.Config.Image != p.Plan.Images[id.String()] {
		return "uncertain", nil
	}
	if v.Container.State.Status == "created" {
		return "uncertain", nil
	}
	if v.Container.State.Running {
		hookCtx, cancel := context.WithDeadline(ctx, s.HookDeadline)
		defer cancel()
		wait := e.Docker.ContainerWait(hookCtx, name, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-wait.Result:
		case <-wait.Error:
		case <-hookCtx.Done():
		}
		v, err = e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
		if err != nil {
			if errdefs.IsNotFound(err) {
				return "uncertain", nil
			}
			return "", err
		}
		if v.Container.State.Running {
			seconds := 1
			if _, err = e.Docker.ContainerStop(ctx, name, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
				return "", err
			}
			return "failed", nil
		}
	}
	if v.Container.State.Status != "exited" {
		return "uncertain", nil
	}
	finished, err := time.Parse(time.RFC3339Nano, v.Container.State.FinishedAt)
	if err != nil {
		return "uncertain", nil
	}
	if finished.After(s.HookDeadline) || v.Container.State.ExitCode != 0 || v.Container.State.OOMKilled || e.cancelled(c.Id) {
		return "failed", nil
	}
	s.Phase = "command-done"
	if err = save(*s); err != nil {
		return "", err
	}
	return "succeeded", nil
}
