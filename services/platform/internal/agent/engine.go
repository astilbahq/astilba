package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"io"
	"sync"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

const labelServer = "com.astilba.server"
const labelResource = "com.astilba.resource"
const labelEnvironment = "com.astilba.environment"
const labelKind = "com.astilba.kind"

type Engine struct {
	Journal     *Journal
	Docker      *client.Client
	Point       func(string)
	Update      func(context.Context, api.AgentRelease) error
	DockerCLI   string
	ACMEEmail   string
	ResticCLI   string
	BackupRoots []string
	mu          sync.Mutex
}

func (e *Engine) point(p string) {
	if e.Point != nil {
		e.Point(p)
	}
}
func Name(c api.ExecutionCommand) string { return "astilba-probe-" + c.ResourceId.String() }
func (e *Engine) checkpoint(id uuid.UUID, name string) error {
	err := e.Journal.Mutate(id, func(r *Record) {
		r.Checkpoint = name
		found := false
		for i := range r.Report.Steps {
			if r.Report.Steps[i].Name == name {
				found = true
			} else {
				r.Report.Steps[i].State = "done"
			}
		}
		if !found {
			r.Report.Steps = append(r.Report.Steps, api.ExecutionStep{Name: name, State: "intent", Detail: ""})
		}
	})
	if err == nil {
		e.point(name)
	}
	return err
}
func (e *Engine) finish(id uuid.UUID, state api.ExecutionState, note string) error {
	return e.Journal.Mutate(id, func(r *Record) {
		r.Report.State = state
		r.Report.Note = note
		if state == "succeeded" {
			for i := range r.Report.Steps {
				r.Report.Steps[i].State = "done"
			}
		}
	})
}
func (e *Engine) cancelled(id uuid.UUID) bool {
	r, err := e.Journal.Record(id)
	return err == nil && r.Command.CancelRequested
}
func (e *Engine) Execute(ctx context.Context, c api.ExecutionCommand) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	cfg, err := e.Journal.Config()
	if err != nil {
		return err
	}
	if c.ServerId != cfg.ServerID || c.Id == uuid.Nil || c.Generation < 1 || len(c.PlanHash) != 64 || (cfg.Role == "build" && c.Kind != "update" && c.Kind != "pipeline") || (cfg.Role == "deployment" && c.Kind == "pipeline") {
		return errors.New("command identity or role rejected")
	}
	if c.Kind != "test" && c.Kind != "update" && c.Kind != "visual" && c.Kind != "pipeline" && c.Kind != "backup" && c.Kind != "schedule" && c.Kind != "upgrade" {
		return errors.New("unsupported command")
	}
	preview := false
	if c.Pipeline != nil {
		var p pipeline.Command
		if json.Unmarshal(*c.Pipeline, &p) == nil {
			preview = p.Preview != nil
		}
	}
	if c.Visual != nil {
		var p deployment.Payload
		if json.Unmarshal(*c.Visual, &p) == nil {
			preview = p.Plan.Preview != nil
		}
	}
	if c.Kind != "update" && cfg.PreviewOnly != preview {
		return errors.New("preview server reservation rejected this command")
	}
	r, err := e.Journal.Accept(c)
	if err != nil {
		return err
	}
	e.point("accepted")
	if execution.Terminal(r.Report.State) || r.Report.State == "needs_attention" {
		return nil
	}
	// An already accepted command continues after its admission deadline.
	if c.Kind != "upgrade" && r.Checkpoint == "accepted" && time.Now().After(c.Deadline) {
		return e.finish(c.Id, "cancelled", "The admission deadline passed before execution.")
	}
	upgrades, err := e.Journal.upgrades()
	if err != nil {
		return err
	}
	for _, u := range upgrades {
		if u.Status.Active() && (c.Kind != "upgrade" || c.ResourceId != u.Command.Plan.ID) {
			return e.finish(c.Id, "failed", "An active upgrade blocks other host operations until recovery or cutover completes.")
		}
	}
	if c.Kind == "upgrade" {
		return e.executeUpgrade(ctx, c, r)
	}

	schedules, err := e.Journal.scheduleStates()
	if err != nil {
		return err
	}
	for _, s := range schedules {
		if len(s.Writers) > 0 || hasUncertainRun(s.Runs) {
			return e.finish(c.Id, "needs_attention", "An interrupted local schedule requires inspection and resolve-run before another host operation.")
		}
	}
	if c.Kind == "update" {
		if c.Release == nil || execution.Hash(c.Release) != c.PlanHash {
			return e.finish(c.Id, "failed", "Update manifest mismatch.")
		}
		if e.cancelled(c.Id) {
			return e.finish(c.Id, "cancelled", "Cancelled before replacing the agent.")
		}
		if e.Update == nil {
			return e.finish(c.Id, "needs_attention", "This installation needs the documented local update procedure.")
		}
		if err = e.Update(ctx, *c.Release); err != nil {
			return e.finish(c.Id, "needs_attention", "Agent update did not finish. Inspect the local update state and retry the local procedure.")
		}
		return e.finish(c.Id, "succeeded", "Agent artifact verified and installed.")
	}
	if c.Kind == "backup" || c.Kind == "schedule" {
		return e.executeBackup(ctx, c, r)
	}
	if c.Kind == "pipeline" {
		return e.executePipeline(ctx, c, r)
	}
	if c.Kind == "visual" {
		return e.executeVisual(ctx, c, r)
	}
	if c.Image != execution.TestImage || c.EnvironmentId == uuid.Nil || c.ResourceId != uuid.NewSHA1(c.ServerId, []byte(c.EnvironmentId.String()+":probe:v1")) {
		return e.finish(c.Id, "failed", "Unsupported test recipe.")
	}
	if e.Docker == nil {
		return e.finish(c.Id, "failed", "Docker is unavailable.")
	}
	if c.Action != "start" && c.Action != "inspect" && c.Action != "remove" {
		return e.finish(c.Id, "failed", "Unsupported action.")
	}
	if c.Action == "start" {
		return e.start(ctx, c, r)
	}
	return e.inspectOrRemove(ctx, c)
}
func (e *Engine) inspect(ctx context.Context, c api.ExecutionCommand) (client.ContainerInspectResult, bool, error) {
	v, err := e.Docker.ContainerInspect(ctx, Name(c), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return v, false, nil
	}
	if err != nil {
		return v, false, err
	}
	labels := v.Container.Config.Labels
	if labels[labelServer] != c.ServerId.String() || labels[labelResource] != c.ResourceId.String() || labels[labelEnvironment] != c.EnvironmentId.String() || labels[labelKind] != "probe-v1" || v.Container.Config.Image != c.Image {
		return v, true, errors.New("resource ownership mismatch")
	}
	return v, true, nil
}
func (e *Engine) start(ctx context.Context, c api.ExecutionCommand, r Record) error {
	if r.Checkpoint == "accepted" {
		if e.cancelled(c.Id) {
			return e.finish(c.Id, "cancelled", "Cancelled before host changes.")
		}
		if err := e.checkpoint(c.Id, "pull"); err != nil {
			return err
		}
	}
	// A timeout or process exit leaves the journal pending; no tool error proves absence.
	if r.Checkpoint == "accepted" || r.Checkpoint == "pull" {
		pull, err := e.Docker.ImagePull(ctx, c.Image, client.ImagePullOptions{})
		if err != nil {
			return e.finish(c.Id, "needs_attention", "Image pull failed. Check Docker connectivity and available disk, then retry this operation.")
		}
		_, err = io.Copy(io.Discard, pull)
		closeErr := pull.Close()
		if err != nil || closeErr != nil {
			return e.finish(c.Id, "needs_attention", "Image pull was interrupted; retry after inspecting Docker.")
		}
		if err = e.checkpoint(c.Id, "create"); err != nil {
			return err
		}
	}
	inspected, exists, err := e.inspect(ctx, c)
	if err != nil {
		return e.finish(c.Id, "needs_attention", "Cannot reconcile the probe's identity. Inspect the named container; no resource was removed.")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Cancelled at a safe checkpoint. Any created probe is retained; use a reviewed remove operation.")
	}
	if !exists {
		if r.Checkpoint == "start" || r.Checkpoint == "observe" {
			return e.finish(c.Id, "needs_attention", "An accepted container disappeared. Inspect the host before retrying.")
		}
		if err = e.checkpoint(c.Id, "create"); err != nil {
			return err
		}
		_, err = e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: Name(c), Config: &container.Config{Image: c.Image, Cmd: []string{"sleep", "86400"}, Labels: map[string]string{labelServer: c.ServerId.String(), labelResource: c.ResourceId.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "probe-v1"}}, HostConfig: &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: 32 << 20, PidsLimit: ptr(int64(16))}, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}}})
		e.point("created-effect")
		if err != nil {
			return e.finish(c.Id, "needs_attention", "Container creation requires reconciliation; retry after checking Docker.")
		}
		inspected, exists, err = e.inspect(ctx, c)
		if err != nil || !exists {
			return e.finish(c.Id, "needs_attention", "Created container could not be inspected.")
		}
	}
	if !inspected.Container.State.Running {
		if err = e.checkpoint(c.Id, "start"); err != nil {
			return err
		}
		// Recheck cancellation after persisting intent; never stop a running container to cancel.
		if e.cancelled(c.Id) {
			return e.finish(c.Id, "cancelled", "Cancelled before start. Created probe is retained.")
		}
		_, err = e.Docker.ContainerStart(ctx, inspected.Container.ID, client.ContainerStartOptions{})
		e.point("started-effect")
		if err != nil {
			return e.finish(c.Id, "needs_attention", "Container start requires reconciliation.")
		}
	}
	if err = e.checkpoint(c.Id, "observe"); err != nil {
		return err
	}
	inspected, exists, err = e.inspect(ctx, c)
	if err != nil || !exists || !inspected.Container.State.Running {
		return e.finish(c.Id, "needs_attention", "Probe was not observed running. Inspect Docker before retrying.")
	}
	return e.finish(c.Id, "succeeded", "Probe is running. It stays running while management or the agent is offline; remove it explicitly when finished.")
}
func ptr[T any](v T) *T { return &v }
func (e *Engine) inspectOrRemove(ctx context.Context, c api.ExecutionCommand) error {
	v, exists, err := e.inspect(ctx, c)
	if err != nil {
		return e.finish(c.Id, "needs_attention", "Ownership could not be verified. No container was changed.")
	}
	if e.cancelled(c.Id) {
		return e.finish(c.Id, "cancelled", "Cancelled before host changes.")
	}
	if c.Action == "inspect" {
		if !exists {
			return e.finish(c.Id, "failed", "No managed probe exists.")
		}
		return e.finish(c.Id, "succeeded", "Managed probe inspected; no host changes.")
	}
	if !exists {
		return e.finish(c.Id, "succeeded", "Managed probe is absent.")
	}
	if err = e.checkpoint(c.Id, "remove"); err != nil {
		return err
	}
	// Delete by inspected immutable container ID, never by a mutable name. Volumes are retained.
	_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
	e.point("removed-effect")
	if err != nil && !errdefs.IsNotFound(err) {
		return e.finish(c.Id, "needs_attention", "Removal outcome needs inspection; retry the same operation.")
	}
	return e.finish(c.Id, "succeeded", "Managed probe removed. No networks or volumes were removed.")
}

func (e *Engine) commandContext(parent context.Context, c api.ExecutionCommand) (context.Context, func()) {
	ctx, cancel := context.WithDeadline(parent, c.Deadline)
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(200 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-tick.C:
				if e.cancelled(c.Id) {
					cancel()
					return
				}
			}
		}
	}()
	return ctx, func() { close(done); cancel() }
}
