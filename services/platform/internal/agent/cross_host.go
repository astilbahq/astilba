package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"slices"
	"strconv"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func (e *Engine) executeCrossHost(ctx context.Context, c api.ExecutionCommand, r Record) error {
	command, err := crossHostCommand(c)
	if err != nil || command.Unit.Plan.Local == nil || command.Unit.Plan.Validate(command.Phase != "resolve") != nil {
		return e.finish(c.Id, "failed", "The assigned cross-host plan is invalid.")
	}
	p := command.Unit.Plan
	if command.Maintenance != nil && !validMaintenance(command.Maintenance, p) {
		return e.finish(c.Id, "failed", "The coordinated maintenance source or target is invalid.")
	}
	cfg, err := e.Journal.Config()
	if err != nil {
		return err
	}
	// Maintenance uses the current command's controller authority, checked by
	// the journal. Its applied source may contain a historical network manifest;
	// no new link authority is installed by maintenance phases.
	if cfg.PreviewOnly || cfg.Role == "build" || command.Maintenance == nil && command.Phase != "observe" && p.Local.Network.ControllerGeneration != cfg.ControllerGeneration && !e.Journal.recoveredRoot(c, p) || e.Docker == nil {
		return e.finish(c.Id, "failed", "Cross-host execution requires the approved deployment host.")
	}
	if r.Checkpoint == "accepted" {
		if err := e.checkpoint(c.Id, "cross-host-"+command.Phase); err != nil {
			return err
		}
	}
	if command.Phase == "checkpoint" {
		return e.executeRecoveryCheckpoint(ctx, c, command)
	}
	if command.Phase == "resolve" {
		retained := map[string]string{}
		for _, app := range p.LocalApplications() {
			if _, replacing := p.Local.Requests[app.ID.String()]; !replacing && command.Unit.Previous.Images[app.ID.String()] != "" {
				retained[app.ID.String()] = command.Unit.Previous.Images[app.ID.String()]
			}
		}
		o, err := e.resolveVisualImages(ctx, deployment.Payload{Plan: p, RegistryAccess: command.RegistryAccess}, retained)
		if err != nil {
			return e.finish(c.Id, "failed", err.Error())
		}
		if o.InfrastructureDrift || o.ResourceDrift {
			return e.finish(c.Id, "failed", "Host drift requires reconciliation before a cross-host plan can be reviewed.")
		}
		return e.finishCrossHost(c, command, o, "Image identities and this host's state are ready for review.", false)
	}
	if command.Phase == "observe" {
		current, err := e.Journal.unit(p.UnitID)
		if err != nil || current.Plan.Hash != p.Hash {
			return e.finish(c.Id, "failed", "The applied local plan changed before inspection.")
		}
		o, _, err := e.observeUnit(ctx, p, command.Secrets)
		if err != nil {
			return e.finish(c.Id, "failed", "The local runtime could not be inspected; its previous snapshot is retained.")
		}
		return e.finishCrossHost(c, command, o, "Runtime inspected without changing resources or the applied revision.", false)
	}
	if err := e.Journal.requireRecoveryCheckpoint(c, p.UnitID, p.Local.Primary); err != nil {
		return e.finish(c.Id, "needs_attention", "The frozen recovery checkpoint must be retained before this host can change resources.")
	}
	state, err := e.Journal.crossHost()
	if err != nil {
		return err
	}
	if command.Phase == "reserve" && state.ParentID == uuid.Nil {
		if e.cancelled(c.Id) {
			return e.finish(c.Id, "cancelled", "Cancelled before reserving this host.")
		}
		o, _, err := e.observeUnit(ctx, p, command.Secrets)
		expected := p.ExpectedState
		if command.Maintenance != nil {
			expected = command.Maintenance.ExpectedState
		}
		if err != nil || o.StateHash != expected || o.InfrastructureDrift || o.ResourceDrift {
			return e.finish(c.Id, "failed", "This host changed after review. Create a new plan.")
		}
		if command.Maintenance == nil {
			inventory, err := capacityInventory(ctx, e.Docker)
			if err != nil || inventory.UnboundedInventoryHash != command.Unit.InventoryHash {
				return e.finish(c.Id, "failed", "The acknowledged unbounded-workload inventory changed. Review capacity again.")
			}
		}
		state, err = e.Journal.reserveCrossHost(c, command)
		if err != nil {
			return err
		}
		e.point("cross-host-reserved")
	}
	if command.Phase == "abort" && state.ParentID == uuid.Nil {
		// A host that never acknowledged reservation has performed no candidate effects.
		return e.finishCrossHost(c, command, deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}}, "No candidate reservation was installed on this host.", false)
	}
	if command.Maintenance != nil && state.ParentID == uuid.Nil && (command.Phase == "resume-source" || command.Phase == "complete") {
		current, err := e.Journal.unit(p.UnitID)
		if err != nil || current.Plan.Hash != p.Hash {
			return e.finish(c.Id, "needs_attention", "An unreserved maintenance source changed; reconciliation is required.")
		}
		o, _, err := e.observeUnit(ctx, p, command.Secrets)
		if err != nil {
			return err
		}
		if command.Phase == "complete" {
			if command.UseTarget || o.InfrastructureDrift || o.ResourceDrift || slices.ContainsFunc(o.Resources, func(r deployment.Resource) bool { return r.Drift }) || p.ValidateLocalObservation(o) != nil {
				return e.finish(c.Id, "needs_attention", "An unreserved maintenance source is not ready for completion.")
			}
			o.Applied = true
		}
		return e.finishCrossHost(c, command, o, "This host never installed a maintenance reservation; its source was retained.", false)
	}
	if state.ParentID != command.ParentID || state.GroupHash != command.GroupHash || state.Unit.Plan.Hash != p.Hash {
		return e.finish(c.Id, "needs_attention", "The frozen host reservation requires reconciliation.")
	}
	if command.Maintenance != nil {
		if e.cancelled(c.Id) && command.Phase != "restore" && command.Phase != "resume-source" && command.Phase != "resume-target" && command.Phase != "complete" {
			return e.finish(c.Id, "cancelled", "Cancelled at a maintenance checkpoint; known writers remain reserved for recovery.")
		}
		return e.executeMaintenancePhase(ctx, c, command, state)
	}
	if e.cancelled(c.Id) && command.Phase != "abort" && command.Phase != "complete" && command.Phase != "retire" && command.Phase != "publish" {
		return e.finish(c.Id, "cancelled", "Cancelled at a coordinated checkpoint; the host reservation remains until owned cleanup is confirmed.")
	}
	payload := deployment.Payload{Plan: p, Secrets: command.Secrets, RegistryAccess: command.RegistryAccess}
	switch command.Phase {
	case "reserve":
		if err = e.ensureStorage(ctx, p); err != nil {
			return e.finish(c.Id, "needs_attention", safeVisualError(err, command.Secrets))
		}
	case "prepare":
		if state.Phase != "reserved" && state.Phase != "prepared" {
			return e.finish(c.Id, "needs_attention", "Candidate preparation is out of phase.")
		}
		snapshot, err := e.networkHost().Snapshot(p.Local.Network.NetworkID)
		if err != nil || snapshot.AppliedHash != hostnetwork.Hash(p.Local.Network) {
			return e.finish(c.Id, "failed", "The reviewed private-link configuration has not been acknowledged on this host.")
		}
		if err = e.prepareCrossHost(ctx, c, payload, command.Unit.Previous); err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return e.finish(c.Id, "needs_attention", safeVisualError(err, command.Secrets))
		}
		for _, app := range p.LocalApplications() {
			if app.Health != nil {
				if err = e.waitHTTP(ctx, c, p, app); err != nil {
					return e.finish(c.Id, "failed", "Candidate readiness failed; previous routes and pools are retained for coordinated cleanup.")
				}
			}
		}
		state.Phase = "prepared"
		if err = e.Journal.saveCrossHost(state); err != nil {
			return err
		}
	case "hook":
		if p.Release == nil || state.Phase != "prepared" && state.Phase != "hook" {
			return e.finish(c.Id, "failed", "The release command has no prepared candidate on this host.")
		}
		if state.Hook.OperationID == uuid.Nil {
			state.Hook = releaseState{OperationID: c.Id, Plan: p, Phase: "ready"}
			state.Phase = "hook"
			if err = e.Journal.saveCrossHost(state); err != nil {
				return err
			}
		}
		result, err := e.runReleaseCommand(ctx, c, payload, &state.Hook, func(h releaseState) error { state.Hook = h; return e.Journal.saveCrossHost(state) })
		if err != nil {
			return err
		}
		if result == "uncertain" {
			return e.finish(c.Id, "needs_attention", "The release command's outcome is unknown. It will not be replayed.")
		}
		if result != "succeeded" {
			return e.finish(c.Id, "failed", "The release command failed; old routing is retained.")
		}
	case "probe":
		if state.Phase != "prepared" && state.Phase != "hook" && state.Phase != "probed" {
			return e.finish(c.Id, "needs_attention", "Backend probing requires a prepared pool.")
		}
		if !p.Local.Primary {
			return e.finish(c.Id, "failed", "Only the reviewed ingress host probes the complete backend vector.")
		}
		for _, app := range p.Specification.Applications {
			if app.Health == nil {
				continue
			}
			for _, endpoint := range p.Local.Backends[app.ID.String()] {
				url := "http://" + net.JoinHostPort(endpoint.Address, strconv.Itoa(endpoint.Port)) + app.Health.Path
				if err = e.crossHostProbe(ctx, c, p, endpoint.InstanceID, []string{"wget", "-T", "3", "-q", "--spider", url}); err != nil {
					return e.finish(c.Id, "failed", "A reviewed backend is unreachable from the ingress unit. Old routes remain installed.")
				}
			}
		}
		state.Phase = "probed"
		if err = e.Journal.saveCrossHost(state); err != nil {
			return err
		}
	case "publish":
		if state.Phase != "probed" && state.Phase != "publishing" && state.Phase != "published" {
			return e.finish(c.Id, "needs_attention", "Publication requires confirmed backend probes.")
		}
		if !p.Local.Primary {
			return e.finish(c.Id, "failed", "Only the primary unit owns public ingress.")
		}
		if !state.Publication {
			state.Phase = "publishing"
			if err = e.Journal.saveCrossHost(state); err != nil {
				return err
			}
			if err = e.publishVisualRoutes(ctx, p); err != nil {
				return e.finish(c.Id, "needs_attention", "Ingress publication requires reconciliation; no old pool may be retired.")
			}
			state.Publication = true
			state.Phase = "published"
			if err = e.Journal.saveCrossHost(state); err != nil {
				return err
			}
		} else {
			routing, err := e.Journal.routing()
			if err != nil {
				return err
			}
			if err = e.verifyRoutes(ctx, routing.Current); err != nil {
				return e.finish(c.Id, "needs_attention", "The acknowledged ingress revision can no longer be confirmed.")
			}
		}
	case "retire":
		if p.Local.Primary && !state.Publication {
			return e.finish(c.Id, "needs_attention", "Ingress acknowledgement is required before retirement.")
		}
		if state.DrainUntil.IsZero() {
			seconds := 10
			if p.ReleaseOptions != nil {
				seconds = p.ReleaseOptions.DrainSeconds
			}
			state.DrainUntil = time.Now().UTC().Add(time.Duration(seconds) * time.Second)
			if err = e.Journal.saveCrossHost(state); err != nil {
				return err
			}
		}
		if delay := time.Until(state.DrainUntil); delay > 0 {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err = e.retainCrossHostPool(ctx, p, &state); err != nil {
			return e.finish(c.Id, "needs_attention", "An old instance could not be retired with exact ownership. Retained data and the reservation require inspection.")
		}
		state.Phase = "retired"
		if err = e.Journal.saveCrossHost(state); err != nil {
			return err
		}
	case "complete":
		if state.Phase != "retired" {
			return e.finish(c.Id, "needs_attention", "Retirement must be confirmed before releasing the host reservation.")
		}
		o, expected, err := e.observeVisualPlan(ctx, p, command.Secrets, true)
		if err != nil {
			return err
		}
		if err = p.ValidateLocalObservation(o); err != nil {
			return e.finish(c.Id, "needs_attention", "The selected local instance inventory is incomplete; the reservation is retained.")
		}
		if err = e.Journal.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
			return err
		}
		o.Applied = true
		o.ResourceDrift = false
		for i := range o.Resources {
			o.Resources[i].Drift = false
		}
		return e.finishCrossHost(c, command, o, "The selected local pool is recorded and retirement is confirmed.", true)
	case "abort":
		if state.Publication || state.Phase == "publishing" || state.Phase == "published" || state.Phase == "retired" {
			return e.finish(c.Id, "needs_attention", "Publication may have happened. Reconcile ingress before any candidate cleanup.")
		}
		if p.Release != nil && state.Hook.OperationID != uuid.Nil {
			hookCommand := c
			hookCommand.Id = state.Hook.OperationID
			if err = e.removeReleaseCommand(ctx, hookCommand, payload); err != nil {
				return e.finish(c.Id, "needs_attention", "Release command cleanup requires inspection.")
			}
		}
		if err = e.removeCrossHostCandidates(ctx, p, command.Unit.Previous); err != nil {
			return e.finish(c.Id, "needs_attention", "Candidate cleanup requires ownership reconciliation. Previous pools and data are retained.")
		}
		o, _, err := e.observeUnit(ctx, p, command.Secrets)
		if err != nil {
			return err
		}
		return e.finishCrossHost(c, command, o, "Owned candidates were removed; previous serving pools and data remain.", true)
	default:
		return e.finish(c.Id, "failed", "Unsupported coordinated phase.")
	}
	o, _, err := e.observeVisualPlan(ctx, p, command.Secrets, command.Phase == "prepare" || command.Phase == "retire")
	if err != nil {
		return err
	}
	return e.finishCrossHost(c, command, o, "Coordinated phase acknowledged: "+command.Phase+".", false)
}
func (e *Engine) prepareCrossHost(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, previous deployment.Plan) error {
	if _, err := e.composeVersion(ctx); err != nil {
		return err
	}
	if len(p.Secrets) != len(p.Plan.SecretReferences()) {
		return errors.New("the execution secret set differs from this unit's references")
	}
	if err := e.pullVisualImages(ctx, p); err != nil {
		return err
	}
	for _, a := range p.Plan.LocalApplications() {
		if _, distributed := p.Plan.Local.Requests[a.ID.String()]; distributed {
			image, err := e.Docker.ImageInspect(ctx, p.Plan.Images[a.ID.String()])
			if err != nil || image.Config == nil || len(image.Config.Volumes) > 0 {
				return errors.New("distributed applications cannot declare image volumes")
			}
		}
	}
	body, err := deployment.Compose(p.Plan, p.Secrets, false)
	if err != nil {
		return err
	}
	if err = e.requireOwnedComposeServices(ctx, p.Plan, p.Plan.LocalApplications()); err != nil {
		return err
	}
	unchanged, err := unchangedCrossHostServices(p, previous, body)
	if err != nil {
		return err
	}
	for _, app := range serviceOrder(p.Plan.Specification) {
		if !p.Plan.Runs(app.ID) {
			continue
		}
		if e.cancelled(c.Id) {
			return errors.New("candidate preparation cancelled")
		}
		if unchanged[app.ID] {
			continue
		}
		if err = e.compose(ctx, p.Plan, body, p.Secrets, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(app.ID)); err != nil {
			return err
		}
		e.point("cross-host-candidate")
	}
	for key, endpoint := range p.Plan.Local.Outputs {
		if endpoint.ServerID == p.Plan.ServerID {
			continue
		}
		if err = e.crossHostProbe(ctx, c, p.Plan, uuid.NewSHA1(p.Plan.ID, []byte(key)), []string{"nc", "-z", "-w", "3", endpoint.Address, strconv.Itoa(endpoint.Port)}); err != nil {
			return errors.New("a private binding is unreachable from its assigned unit")
		}
	}
	return nil
}

// Shared primary services keep their existing containers when only the new
// plan label differs. The initial private-port publication still reconciles.
func unchangedCrossHostServices(p deployment.Payload, previous deployment.Plan, body []byte) (map[uuid.UUID]bool, error) {
	out := map[uuid.UUID]bool{}
	if previous.ID == uuid.Nil || previous.Local == nil {
		return out, nil
	}
	shared := []uuid.UUID{}
	for _, app := range p.Plan.LocalApplications() {
		if _, replacing := p.Plan.Local.Requests[app.ID.String()]; !replacing {
			if ids := previous.Local.Instances[app.ID.String()]; len(ids) > 0 {
				shared = append(shared, app.ID)
			}
		}
	}
	old, err := deployment.ComposeApplications(previous, p.Secrets, shared)
	if err != nil {
		return nil, err
	}
	type composeServices struct {
		Services map[string]map[string]any `json:"services"`
	}
	var before, after composeServices
	if err := json.Unmarshal(old, &before); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &after); err != nil {
		return nil, err
	}
	for _, services := range []composeServices{before, after} {
		for _, service := range services.Services {
			if labels, ok := service["labels"].(map[string]any); ok {
				delete(labels, "com.astilba.plan")
			}
		}
	}
	for _, app := range p.Plan.LocalApplications() {
		if _, replacing := p.Plan.Local.Requests[app.ID.String()]; replacing {
			continue
		}
		identical := true
		for _, id := range p.Plan.InstanceIDs(app.ID) {
			key := deployment.ServiceName(id)
			identical = identical && before.Services[key] != nil && deployment.Hash(before.Services[key]) == deployment.Hash(after.Services[key])
		}
		out[app.ID] = identical
	}
	return out, nil
}
func (e *Engine) crossHostProbe(ctx context.Context, c api.ExecutionCommand, p deployment.Plan, id uuid.UUID, command []string) error {
	name := "astilba-link-probe-" + c.Id.String() + "-" + id.String()
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		if v.Container.Config.Labels["com.astilba.health"] != c.Id.String() || !visualOwnedProbe(v.Container.Config.Labels, p) {
			return errors.New("probe ownership conflict")
		}
		if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
			return err
		}
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: command, Labels: healthLabels(c, p)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.UnitID)), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 32 << 20, PidsLimit: ptr(int64(16))}}})
	if err != nil {
		return err
	}
	defer e.Docker.ContainerRemove(context.WithoutCancel(ctx), created.ID, client.ContainerRemoveOptions{Force: true})
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return err
	}
	wait := e.Docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-wait.Error:
		return err
	case status := <-wait.Result:
		if status.StatusCode != 0 {
			return fmt.Errorf("private endpoint probe failed")
		}
		return nil
	}
}
func visualOwnedProbe(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels["com.astilba.unit"] == p.UnitID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels[labelKind] == "health-v1"
}
