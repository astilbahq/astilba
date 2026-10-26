package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
	"os"
	"slices"
	"strings"
	"time"
)

type retainedInstance struct {
	ApplicationID uuid.UUID
	Name, Image   string
}
type releaseState struct {
	OperationID              uuid.UUID
	Plan                     deployment.Plan
	Old                      unitState
	Phase                    string
	AbortStatus              api.ExecutionState
	AbortNote                string
	RestoreRoutes            bool
	HookDeadline, DrainUntil time.Time
	Retained                 []retainedInstance
}

func (j *Journal) release(unit uuid.UUID) (releaseState, error) {
	var s releaseState
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "releases", unit.String(), &s) })
	if errors.Is(err, os.ErrNotExist) {
		err = nil
	}
	return s, err
}
func (j *Journal) saveRelease(s releaseState) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "releases", s.Plan.UnitID.String(), s) })
}
func (j *Journal) switchRelease(s releaseState, expected map[string]string) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		if err := j.put(tx, "units", s.Plan.UnitID.String(), unitState{Plan: s.Plan, Expected: expected}); err != nil {
			return err
		}
		return j.put(tx, "releases", s.Plan.UnitID.String(), s)
	})
}
func application(p deployment.Plan, id uuid.UUID) domain.Application {
	for _, a := range p.Specification.Applications {
		if a.ID == id {
			return a
		}
	}
	return domain.Application{}
}
func (e *Engine) executeRelease(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, r Record) error {
	s, err := e.Journal.release(p.Plan.UnitID)
	if err != nil {
		return err
	}
	if s.OperationID != c.Id {
		if s.OperationID != uuid.Nil && s.Phase != "complete" && s.Phase != "aborted" {
			return e.finish(c.Id, "needs_attention", "An earlier release requires recovery before another host operation.")
		}
		old, err := e.Journal.unit(p.Plan.UnitID)
		if err != nil {
			return err
		}
		app, err := deployment.ReleaseApplication(old.Plan, p.Plan)
		if err != nil || app.ID != p.Plan.Release.ApplicationID || old.Plan.ID != p.Plan.Release.PreviousPlanID {
			return e.finish(c.Id, "failed", "The serving revision no longer matches this protected release.")
		}
		for _, a := range p.Plan.Specification.Applications {
			if a.ID != app.ID && !slices.Equal(p.Plan.InstanceIDs(a.ID), old.Plan.InstanceIDs(a.ID)) {
				return e.finish(c.Id, "failed", "An unchanged application's runtime identity differs.")
			}
		}
		oldIDs := old.Plan.InstanceIDs(app.ID)
		for _, instance := range p.Plan.InstanceIDs(app.ID) {
			if slices.Contains(oldIDs, instance) {
				return e.finish(c.Id, "failed", "A protected release needs a completely isolated candidate group.")
			}
		}
		before, _, err := e.observeUnit(ctx, old.Plan, p.Secrets)
		if err != nil {
			return err
		}
		if before.StateHash != p.Plan.ExpectedState || before.InfrastructureDrift || before.ResourceDrift {
			return e.visualFinish(ctx, c, p, "failed", "The serving runtime changed after review. Prepare a new plan.")
		}
		if err = e.waitHTTP(ctx, c, old.Plan, application(old.Plan, app.ID)); err != nil {
			return e.visualFinish(ctx, c, p, "failed", "The serving revision is not ready; a protected release cannot start.")
		}
		if err = e.checkpoint(c.Id, "release-preflight"); err != nil {
			return err
		}
		s = releaseState{OperationID: c.Id, Plan: p.Plan, Old: old, Phase: "prepared", Retained: s.Retained}
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
		e.point("release-prepared")
	} else if s.Plan.Hash != p.Plan.Hash {
		return e.finish(c.Id, "failed", "Release journal identity mismatch.")
	}
	if s.Phase == "aborting" {
		return e.abortRelease(ctx, c, p, &s, s.AbortStatus, s.AbortNote)
	}
	if s.Phase == "aborted" {
		return e.visualFinish(ctx, c, p, s.AbortStatus, s.AbortNote)
	}
	if s.Phase == "complete" {
		return e.visualFinish(ctx, c, p, "succeeded", "The protected release is serving; the old instance was drained and retained.")
	}
	app := application(p.Plan, p.Plan.Release.ApplicationID)
	if s.Phase == "prepared" {
		if e.cancelled(c.Id) {
			return e.abortRelease(ctx, c, p, &s, "cancelled", "Cancelled before candidate startup.")
		}

		if _, err = e.composeVersion(ctx); err != nil {
			return e.abortRelease(ctx, c, p, &s, "failed", "The supported Compose plugin is unavailable.")
		}
		if len(p.Secrets) != len(domain.SecretReferences(p.Plan.Specification)) {
			return e.abortRelease(ctx, c, p, &s, "failed", "The execution secret set differs from the reviewed plan.")
		}
		if err = e.pullVisualImages(ctx, p); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.abortRelease(ctx, c, p, &s, "failed", "Candidate image is unavailable; the serving revision is retained.")
		}
		for _, plan := range []deployment.Plan{s.Old.Plan, p.Plan} {
			image, err := e.Docker.ImageInspect(ctx, plan.Images[app.ID.String()])
			if err != nil || image.Config == nil || len(image.Config.Volumes) > 0 {
				return e.abortRelease(ctx, c, p, &s, "failed", "Image-declared storage is incompatible with a stateless release.")
			}
		}
		if err = e.ensureStorage(ctx, p.Plan); err != nil {
			return e.abortRelease(ctx, c, p, &s, "failed", "The owned network or storage differs from the plan.")
		}
		if _, err = e.candidateContainers(ctx, p.Plan, app.ID); err != nil {
			return e.finish(c.Id, "needs_attention", "Candidate ownership could not be established; nothing was removed.")
		}
		s.Phase = "ready"
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
	}
	if s.Phase == "ready" || s.Phase == "command-starting" || s.Phase == "command-done" {
		if e.cancelled(c.Id) {
			return e.abortRelease(ctx, c, p, &s, "cancelled", "Cancelled before the traffic switch; the serving revision is retained. Release-command external writes may remain.")
		}
		if len(p.Plan.Release.Options.Command) > 0 && s.Phase != "command-done" {
			if err = e.checkpoint(c.Id, "release-command"); err != nil {
				return err
			}
			outcome, err := e.releaseCommand(ctx, c, p, &s)
			if err != nil {
				return err
			}
			if outcome == "uncertain" {
				return e.finish(c.Id, "needs_attention", "The release command may have executed. Inspect its external effects; it will not be replayed automatically. Previous traffic is retained.")
			}
			if outcome != "succeeded" {
				return e.abortRelease(ctx, c, p, &s, "failed", "Release command failed or exceeded its budget. Previous traffic is retained; external writes are not undone.")
			}
		}
		if e.cancelled(c.Id) {
			return e.abortRelease(ctx, c, p, &s, "cancelled", "Cancelled before the traffic switch; external command writes remain.")
		}
		if err = e.checkpoint(c.Id, "release-candidate"); err != nil {
			return err
		}
		body, err := deployment.Compose(p.Plan, p.Secrets, false)
		if err != nil {
			return err
		}
		if err = e.compose(ctx, p.Plan, body, p.Secrets, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(app.ID)); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.abortRelease(ctx, c, p, &s, "failed", "Candidate startup failed; previous traffic is retained.")
		}
		e.point("release-candidate-started")
		s.Phase = "candidate"
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
	}
	if s.Phase == "candidate" {
		if err = e.checkpoint(c.Id, "release-readiness"); err != nil {
			return err
		}
		if _, err = e.candidateContainers(ctx, p.Plan, app.ID); err != nil {
			return e.finish(c.Id, "needs_attention", "Candidate ownership changed; the serving revision is retained.")
		}
		if err = e.waitHTTP(ctx, c, p.Plan, app); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.abortRelease(ctx, c, p, &s, "failed", "Candidate readiness failed. The previous healthy revision remains serving.")
		}
		before, _, err := e.observeUnit(ctx, s.Old.Plan, p.Secrets)
		if err != nil {
			return err
		}
		if before.StateHash != p.Plan.ExpectedState || before.InfrastructureDrift || before.ResourceDrift {
			return e.abortRelease(ctx, c, p, &s, "failed", "The serving runtime changed during candidate preparation. Traffic was not switched.")
		}
		if e.cancelled(c.Id) {
			return e.abortRelease(ctx, c, p, &s, "cancelled", "Cancelled before the traffic switch.")
		}
		s.Phase = "switching"
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
	}
	if s.Phase == "switching" {
		// Durable switch intent makes cancellation a reconciliation request. Both
		// instances survive until routing and active runtime identity are committed.
		if err = e.checkpoint(c.Id, "release-switch"); err != nil {
			return err
		}
		if _, err = e.candidateContainers(ctx, p.Plan, app.ID); err != nil {
			return e.finish(c.Id, "needs_attention", "Candidate ownership is uncertain. Both revisions are retained.")
		}
		if err = e.waitHTTP(ctx, c, p.Plan, app); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.abortRelease(ctx, c, p, &s, "failed", "Candidate lost readiness before the switch was committed.")
		}
		if err = e.publishReleaseRoutes(ctx, p.Plan); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.abortRelease(ctx, c, p, &s, "failed", "The proxy rejected the candidate. Previous routing was restored.")
		}
		e.point("release-route-acknowledged")
		observed, expected, err := e.observeVisualPlan(ctx, p.Plan, p.Secrets, true)
		if err != nil {
			return err
		}
		if len(observed.Resources) != len(p.Plan.Specification.Applications) || observed.InfrastructureDrift {
			return e.finish(c.Id, "needs_attention", "Traffic was switched but runtime confirmation is incomplete. Both instances are retained.")
		}
		for _, res := range observed.Resources {
			if res.State != "running" && res.State != "healthy" {
				return e.finish(c.Id, "needs_attention", "Traffic needs inspection; both revisions are retained.")
			}
		}
		s.Phase = "switched"
		s.DrainUntil = time.Now().UTC().Add(time.Duration(p.Plan.Release.Options.DrainSeconds) * time.Second)
		if err = e.Journal.switchRelease(s, expected); err != nil {
			return err
		}
		e.point("release-switch-committed")
	}
	if s.Phase == "switched" {
		if err = e.checkpoint(c.Id, "release-drain"); err != nil {
			return err
		}
		if wait := time.Until(s.DrainUntil); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err = e.stopReleaseInstance(ctx, s.Old.Plan, app.ID); err != nil {
			return e.finish(c.Id, "needs_attention", "The new revision is serving; old-instance retirement needs inspection.")
		}
		if err = e.removeRetiredReplicas(ctx, s.Old.Plan, app.ID); err != nil {
			return e.finish(c.Id, "needs_attention", "The new revision is serving; old replica cleanup needs inspection.")
		}
		name := s.Old.Plan.ContainerName(app.ID)
		found := false
		for _, item := range s.Retained {
			found = found || item.Name == name
		}
		if !found {
			s.Retained = append(s.Retained, retainedInstance{app.ID, name, s.Old.Plan.Images[app.ID.String()]})
		}
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
		if err = e.trimReleaseInstances(ctx, p.Plan, &s); err != nil {
			return e.finish(c.Id, "needs_attention", "The new revision is serving; retained-instance cleanup needs inspection.")
		}
		if err = e.removeReleaseCommand(ctx, c, p); err != nil {
			return e.finish(c.Id, "needs_attention", "The new revision is serving; command cleanup needs inspection.")
		}
		s.Phase = "complete"
		if err = e.Journal.saveRelease(s); err != nil {
			return err
		}
	}
	return e.visualFinish(ctx, c, p, "succeeded", "The protected release is serving; readiness and proxy switching passed, and the previous instance was drained.")
}
func (e *Engine) candidateContainers(ctx context.Context, p deployment.Plan, id uuid.UUID) ([]string, error) {
	containers := []string{}
	for _, instance := range p.InstanceIDs(id) {
		v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != id.String() || v.Container.Config.Labels["com.astilba.plan"] != p.Hash || v.Container.Config.Image != p.Images[id.String()] {
			return nil, errors.New("candidate identity mismatch")
		}
		containers = append(containers, v.Container.ID)
	}
	return containers, nil
}
func (e *Engine) stopReleaseInstance(ctx context.Context, p deployment.Plan, id uuid.UUID) error {
	for i, instance := range p.InstanceIDs(id) {
		v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
		if i > 0 && errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != id.String() || v.Container.Config.Image != p.Images[id.String()] {
			return errors.New("old instance identity mismatch")
		}
		if v.Container.State.Running {
			seconds := 10
			if _, err = e.Docker.ContainerStop(ctx, v.Container.ID, client.ContainerStopOptions{Timeout: &seconds}); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e *Engine) removeRetiredReplicas(ctx context.Context, p deployment.Plan, id uuid.UUID) error {
	for _, instance := range p.ReplicaInstances[id.String()] {
		v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instance), client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != id.String() || v.Container.Config.Image != p.Images[id.String()] || v.Container.State.Running {
			return errors.New("retired replica ownership or stopped state mismatch")
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	return nil
}
func (e *Engine) abortRelease(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, s *releaseState, status api.ExecutionState, note string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if s.Phase != "aborting" {
		if status == "failed" {
			note += e.releaseDiagnostics(ctx, c, p)
		}
		s.RestoreRoutes = s.Phase == "switching"
		s.AbortStatus, s.AbortNote = status, note
		s.Phase = "aborting"
		if err := e.Journal.saveRelease(*s); err != nil {
			return err
		}
		e.point("release-abort-recorded")
	}
	if s.RestoreRoutes {
		old := s.Old.Plan
		old.HostChanges = false
		if err := e.waitHTTP(ctx, c, old, application(old, p.Plan.Release.ApplicationID)); err != nil {
			return e.finish(c.Id, "needs_attention", "Cannot confirm previous readiness. Both revisions are retained.")
		}
		if err := e.publishReleaseRoutes(ctx, old); err != nil {
			return e.finish(c.Id, "needs_attention", "Routing recovery is uncertain. Both revisions are retained.")
		}
	}
	candidates, err := e.candidateContainers(ctx, p.Plan, p.Plan.Release.ApplicationID)
	if err != nil {
		return e.finish(c.Id, "needs_attention", "Candidate cleanup ownership is uncertain. Existing traffic is retained.")
	}
	for _, containerID := range candidates {
		_, err := e.Docker.ContainerRemove(ctx, containerID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
		if err != nil && !errdefs.IsNotFound(err) {
			return err
		}
	}
	if err := e.removeReleaseCommand(ctx, c, p); err != nil {
		return err
	}
	e.point("release-abort-cleaned")
	s.Phase = "aborted"
	if err := e.Journal.saveRelease(*s); err != nil {
		return err
	}
	return e.visualFinish(ctx, c, p, s.AbortStatus, s.AbortNote)
}
func (e *Engine) trimReleaseInstances(ctx context.Context, p deployment.Plan, s *releaseState) error {
	counts := map[uuid.UUID]int{}
	keep := make([]retainedInstance, 0, len(s.Retained))
	for i := len(s.Retained) - 1; i >= 0; i-- {
		item := s.Retained[i]
		counts[item.ApplicationID]++
		if counts[item.ApplicationID] <= 2 {
			keep = append([]retainedInstance{item}, keep...)
			continue
		}
		v, err := e.Docker.ContainerInspect(ctx, item.Name, client.ContainerInspectOptions{})
		if errdefs.IsNotFound(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !visualOwned(v.Container.Config.Labels, p) || v.Container.Config.Labels[labelResource] != item.ApplicationID.String() || v.Container.Config.Image != item.Image || v.Container.State.Running {
			return errors.New("retention ownership or stopped state mismatch")
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{RemoveVolumes: false}); err != nil {
			return err
		}
	}
	s.Retained = keep
	return nil
}
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
	name := releaseCommandName(c)
	options := p.Plan.Release.Options
	id := p.Plan.Release.ApplicationID
	if s.Phase == "ready" {
		if _, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			return "uncertain", nil
		}
		body, err := deployment.Compose(p.Plan, p.Secrets, false)
		if err != nil {
			return "", err
		}
		var rendered struct {
			Services map[string]struct{ Environment map[string]string }
		}
		if err = json.Unmarshal(body, &rendered); err != nil {
			return "", err
		}
		env := []string{}
		for key, value := range rendered.Services[p.Plan.HostName(id)].Environment {
			env = append(env, key+"="+strings.ReplaceAll(value, "$$", "$"))
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
		if err = e.Journal.saveRelease(*s); err != nil {
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
	if err = e.Journal.saveRelease(*s); err != nil {
		return "", err
	}
	return "succeeded", nil
}

func (e *Engine) releaseDiagnostics(ctx context.Context, c api.ExecutionCommand, p deployment.Payload) string {
	out := ""
	type diagnosticTarget struct{ name, kind, title string }
	items := []diagnosticTarget{}
	for _, instance := range p.Plan.InstanceIDs(p.Plan.Release.ApplicationID) {
		items = append(items, diagnosticTarget{deployment.UnitName(instance), "visual-v1", "Candidate " + instance.String()[:8]})
	}
	items = append(items, diagnosticTarget{releaseCommandName(c), "release-command-v1", "Release command"})
	for _, item := range items {
		v, err := e.Docker.ContainerInspect(ctx, item.name, client.ContainerInspectOptions{})
		if err != nil {
			continue
		}
		labels := v.Container.Config.Labels
		if labels[labelServer] != p.Plan.ServerID.String() || labels[labelEnvironment] != p.Plan.EnvironmentID.String() || labels["com.astilba.unit"] != p.Plan.UnitID.String() || labels[labelResource] != p.Plan.Release.ApplicationID.String() || labels[labelKind] != item.kind || labels["com.astilba.plan"] != p.Plan.Hash {
			continue
		}
		status := " " + item.title + " state=" + string(v.Container.State.Status) + ", exit=" + fmt.Sprint(v.Container.State.ExitCode) + "."
		if len(out)+len(status) > 512 {
			break
		}
		out += status
		budget := min(280, 512-len(out)-len(" Output: "))
		if budget <= 0 {
			break
		}
		values := map[string]string{}
		for key, value := range p.Secrets {
			values[key] = value
		}
		for _, entry := range v.Container.Config.Env {
			if _, value, ok := strings.Cut(entry, "="); ok {
				values[entry] = value
			}
		}
		logctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		logs, err := e.Docker.ContainerLogs(logctx, v.Container.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "20"})
		if err != nil {
			cancel()
			continue
		}
		sample, err := readVisualLog(logs, v.Container.Config.Tty, values, budget)
		logs.Close()
		cancel()
		if err == nil && sample != "" {
			out += " Output: " + sample
		}
	}
	return out
}
