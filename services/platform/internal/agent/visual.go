package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/containerd/errdefs"
	"github.com/distribution/reference"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

type unitState struct {
	Plan     deployment.Plan   `json:"plan"`
	Expected map[string]string `json:"expected"`
}

func (j *Journal) unit(id uuid.UUID) (unitState, error) {
	var v unitState
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "units", id.String(), &v) })
	if errors.Is(err, os.ErrNotExist) {
		return unitState{Expected: map[string]string{}}, nil
	}
	return v, err
}
func (j *Journal) saveUnit(v unitState) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "units", v.Plan.UnitID.String(), v) })
}

func (e *Engine) visualObservation(c api.ExecutionCommand, observed deployment.Observation) error {
	for _, resource := range observed.Resources {
		observed.ResourceDrift = observed.ResourceDrift || resource.Drift
	}
	if len(observed.Resources) > 40 {
		observed.OmittedResources = len(observed.Resources) - 40
		observed.Resources = observed.Resources[:40]
	}
	raw, err := json.Marshal(observed)
	if err != nil {
		return err
	}
	if len(raw) > deployment.ObservationLimit {
		for i := range observed.Resources {
			observed.Resources[i].Logs = ""
		}
		raw, err = json.Marshal(observed)
	}
	if err != nil || len(raw) > deployment.ObservationLimit {
		return errors.New("observation limit exceeded")
	}
	return e.Journal.Mutate(c.Id, func(r *Record) { value := json.RawMessage(raw); r.Report.Observation = &value })
}
func (e *Engine) visualFinish(ctx context.Context, c api.ExecutionCommand, p deployment.Payload, state api.ExecutionState, note string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	observed, _, err := e.observeUnit(ctx, p.Plan, p.Secrets)
	if err == nil {
		observed.Applied = state == "succeeded" && p.Action == "deploy"
		err = e.visualObservation(c, observed)
	}
	if err != nil {
		if clearErr := e.Journal.Mutate(c.Id, func(r *Record) { r.Report.Observation = nil }); clearErr != nil {
			return clearErr
		}
		if state == "succeeded" {
			if p.Action == "observe" {
				state = "failed"
				note = "Runtime observations are unavailable. No resources were changed."
			} else {
				state = "needs_attention"
				note = "The effect completed, but runtime confirmation is unavailable. Inspect the host and retry to confirm."
			}
		}
	}
	return e.finish(c.Id, state, note)
}
func (e *Engine) executeVisual(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var p deployment.Payload
	if c.Visual == nil || json.Unmarshal(*c.Visual, &p) != nil || p.Digest() != c.PlanHash || p.Plan.ServerID != c.ServerId || p.Plan.EnvironmentID != c.EnvironmentId || p.Plan.UnitID != c.ResourceId {
		return e.finish(c.Id, "failed", "Visual command identity does not match its reviewed plan.")
	}
	if err := p.Plan.Validate(p.Action != "resolve"); err != nil {
		return e.finish(c.Id, "failed", "The deployment plan is invalid.")
	}
	if e.Docker == nil {
		return e.finish(c.Id, "failed", "Docker is unavailable.")
	}
	if e.cancelled(c.Id) {
		return e.visualFinish(ctx, c, p, "cancelled", "Cancelled at a safe checkpoint. Existing services and data are retained.")
	}
	if p.Action == "resolve" {
		observed, _, err := e.observeUnit(ctx, p.Plan, nil)
		if err != nil {
			return e.finish(c.Id, "failed", "Cannot inspect the deployment unit.")
		}
		observed.Images = map[string]string{}
		for _, a := range p.Plan.Specification.Applications {
			image := *a.Source.Image
			if artifact, ok := p.Plan.Artifacts[a.ID.String()]; ok && artifact.ServerID != nil {
				v, err := e.Docker.ImageInspect(ctx, image)
				if err != nil || v.ID != artifact.Digest || v.Architecture != artifact.Architecture {
					return e.finish(c.Id, "failed", "Built image is unavailable or differs from its recorded identity. Rebuild on this host.")
				}
				observed.Images[a.ID.String()] = artifact.Digest
				continue
			}
			named, err := reference.ParseNormalizedNamed(image)
			if err != nil {
				return e.finish(c.Id, "failed", "Invalid image reference.")
			}
			if d, ok := named.(reference.Digested); ok {
				image, err = deployment.DigestReference(image, d.Digest().String())
			} else {
				lookup, cancel := context.WithTimeout(ctx, 30*time.Second)
				v, x := e.Docker.DistributionInspect(lookup, image, client.DistributionInspectOptions{EncodedRegistryAuth: visualRegistryAuth(p, a.ID.String())})
				cancel()
				err = x
				if err == nil {
					image, err = deployment.DigestReference(image, v.Descriptor.Digest.String())
				}
			}
			if err != nil {
				return e.finish(c.Id, "failed", "Image resolution failed. Check the image reference and registry access.")
			}
			observed.Images[a.ID.String()] = image
		}
		observed = resolutionObservation(observed)
		if err = e.visualObservation(c, observed); err != nil {
			return e.finish(c.Id, "failed", "The resolution report exceeds its supported size.")
		}
		return e.finish(c.Id, "succeeded", "Image identities and current runtime state are ready for review.")
	}
	if p.Action == "observe" {
		return e.visualFinish(ctx, c, p, "succeeded", "Runtime observations refreshed; no resources changed.")
	}
	if p.Action == "delete-volume" {
		if p.VolumeID == nil {
			return e.finish(c.Id, "failed", "A volume identity is required.")
		}
		upgrades, err := e.Journal.upgrades()
		if err != nil {
			return err
		}
		for _, u := range upgrades {
			if (*p.VolumeID == u.Command.Plan.OldVolume() || *p.VolumeID == u.Command.Plan.TargetVolumeID) && (u.Status.Active() || time.Now().Before(u.Status.KeepUntil)) {
				return e.finish(c.Id, "failed", "Upgrade retention protects this volume.")
			}
		}
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(*p.VolumeID), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			return e.visualFinish(ctx, c, p, "succeeded", "The volume is absent.")
		}
		if err != nil || !visualOwned(v.Volume.Labels, p.Plan) || v.Volume.Labels[labelResource] != p.VolumeID.String() {
			return e.visualFinish(ctx, c, p, "needs_attention", "Volume ownership could not be verified. Nothing was deleted.")
		}
		if err = e.checkpoint(c.Id, "delete-volume"); err != nil {
			return err
		}
		_, err = e.Docker.VolumeRemove(ctx, v.Volume.Name, client.VolumeRemoveOptions{Force: false})
		if err != nil && !errdefs.IsNotFound(err) {
			return e.visualFinish(ctx, c, p, "needs_attention", "The volume could not be removed. Confirm no container uses it before retrying.")
		}
		return e.visualFinish(ctx, c, p, "succeeded", "The explicitly selected unused volume was deleted.")
	}
	if p.Action != "deploy" {
		return e.finish(c.Id, "failed", "Unsupported visual operation.")
	}
	before, _, err := e.observeUnit(ctx, p.Plan, p.Secrets)
	if err != nil {
		return e.finish(c.Id, "needs_attention", "Cannot inspect current runtime state.")
	}
	if r.Checkpoint == "accepted" {
		if before.InfrastructureDrift && !p.Plan.AcceptDrift {
			return e.visualFinish(ctx, c, p, "failed", "Ingress has changed outside Astilba. Review a reconciliation plan.")
		}
		if before.StateHash != p.Plan.ExpectedState {
			return e.visualFinish(ctx, c, p, "failed", "Runtime changed after review. Create a new plan.")
		}
		for _, v := range before.Resources {
			if v.Drift && !p.Plan.AcceptDrift {
				return e.visualFinish(ctx, c, p, "failed", "Manual changes require an explicit reconciliation plan.")
			}
		}
	}
	unit, err := e.Journal.unit(p.Plan.UnitID)
	if err != nil {
		return err
	}
	if err = deployment.PostgresVersionChange(unit.Plan.Specification, p.Plan.Specification); err != nil {
		return e.finish(c.Id, "failed", err.Error())
	}
	if err = e.checkpoint(c.Id, "preflight"); err != nil {
		return err
	}
	if _, err = e.composeVersion(ctx); err != nil {
		return e.visualFinish(ctx, c, p, "failed", "Install the supported Docker Compose 5.6.1 plugin before deploying.")
	}
	if len(p.Secrets) != len(domain.SecretReferences(p.Plan.Specification)) {
		return e.visualFinish(ctx, c, p, "failed", "The execution secret set does not match the plan.")
	}
	sourceRoot, err := e.prepareSourceFiles(p.Plan)
	if err != nil {
		return e.visualFinish(ctx, c, p, "needs_attention", "Cannot prepare the owned Compose source bundle.")
	}
	compose, err := deployment.ComposeAt(p.Plan, p.Secrets, false, sourceRoot)
	if err != nil {
		return e.visualFinish(ctx, c, p, "failed", "Cannot resolve the reviewed execution variables.")
	}
	if err = e.checkpoint(c.Id, "pull"); err != nil {
		return err
	}
	images := make([]string, 0, len(p.Plan.Images)+1)
	for _, image := range p.Plan.Images {
		images = append(images, image)
	}
	images = append(images, execution.TestImage)
	for _, image := range images {
		key := ""
		for k, v := range p.Plan.Images {
			if v == image {
				key = k
				break
			}
		}
		if a, ok := p.Plan.Artifacts[key]; ok && a.ServerID != nil {
			v, err := e.Docker.ImageInspect(ctx, image)
			if err != nil || v.ID != a.Digest || v.Architecture != a.Architecture {
				return e.visualFinish(ctx, c, p, "failed", "Local artifact identity is unavailable.")
			}
			continue
		}
		if err = e.pullImageAuth(ctx, image, visualRegistryAuth(p, key)); err != nil {
			return e.visualFinish(ctx, c, p, "needs_attention", "Image pull failed or was interrupted; inspect the host before retrying.")
		}
	}

	for key, artifact := range p.Plan.Artifacts {
		v, err := e.Docker.ImageInspect(ctx, p.Plan.Images[key])
		if err != nil || v.Architecture != artifact.Architecture {
			return e.visualFinish(ctx, c, p, "failed", "Artifact architecture differs from the reviewed target.")
		}
	}
	if err = e.checkpoint(c.Id, "storage"); err != nil {
		return err
	}
	if err = e.ensureStorage(ctx, p.Plan); err != nil {
		return e.visualFinish(ctx, c, p, "needs_attention", safeVisualError(err, p.Secrets))
	}
	if err = e.checkpoint(c.Id, "services"); err != nil {
		return err
	}
	for _, a := range serviceOrder(p.Plan.Specification) {
		if e.cancelled(c.Id) {
			return e.visualFinish(ctx, c, p, "cancelled", "Stopped at a safe checkpoint. Partially changed services and all data are retained.")
		}
		v, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(a.ID), client.ContainerInspectOptions{})
		if err == nil && (!visualOwned(v.Container.Config.Labels, p.Plan) || v.Container.Config.Labels[labelResource] != a.ID.String()) {
			return e.visualFinish(ctx, c, p, "needs_attention", "An existing container has conflicting ownership. It was not changed.")
		}
		if err != nil && !errdefs.IsNotFound(err) {
			return e.visualFinish(ctx, c, p, "needs_attention", "Cannot inspect the target container.")
		}
		if err = e.compose(ctx, p.Plan, compose, p.Secrets, "up", "--detach", "--no-deps", "--wait", "--wait-timeout", "60", deployment.ServiceName(a.ID)); err != nil {
			return e.visualFinish(ctx, c, p, "needs_attention", safeVisualError(err, p.Secrets))
		}
		e.point("visual-service-effect")
		if a.Health != nil {
			if err = e.waitHTTP(ctx, c, p.Plan, a); err != nil {
				return e.visualFinish(ctx, c, p, "needs_attention", "Application readiness failed. The new container is retained for inspection; no old image was restarted.")
			}
		}
	}
	if e.cancelled(c.Id) {
		return e.visualFinish(ctx, c, p, "cancelled", "Cancelled before ingress publication. Existing routes and all data are retained.")
	}
	if err = e.checkpoint(c.Id, "ingress"); err != nil {
		return err
	}
	if err = e.publishVisualRoutes(ctx, p.Plan); err != nil {
		return e.visualFinish(ctx, c, p, "needs_attention", safeVisualError(err, p.Secrets))
	}
	current, err := e.ownedContainers(ctx, p.Plan)
	if err != nil {
		return e.visualFinish(ctx, c, p, "needs_attention", "Cannot inspect retired services.")
	}
	keep := map[string]bool{}
	for _, a := range p.Plan.Specification.Applications {
		keep[a.ID.String()] = true
	}
	for _, v := range current {
		if !keep[v.Labels[labelResource]] {
			if _, err = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false}); err != nil && !errdefs.IsNotFound(err) {
				return e.visualFinish(ctx, c, p, "needs_attention", "A retired service could not be removed; its data was retained.")
			}
			e.point("visual-removed-effect")
		}
	}
	if err = e.checkpoint(c.Id, "observe"); err != nil {
		return err
	}
	observed, expected, err := e.observeUnit(ctx, p.Plan, p.Secrets)
	if err != nil {
		return e.visualFinish(ctx, c, p, "needs_attention", "Cannot confirm the resulting runtime state.")
	}
	currentIDs := map[uuid.UUID]bool{}
	for _, a := range p.Plan.Specification.Applications {
		currentIDs[a.ID] = true
	}
	kept := observed.Resources[:0]
	for _, resource := range observed.Resources {
		if resource.State != "missing" || currentIDs[resource.ID] {
			kept = append(kept, resource)
		}
	}
	observed.Resources = kept
	if len(observed.Resources) != len(p.Plan.Specification.Applications) {
		return e.visualFinish(ctx, c, p, "needs_attention", "The resulting resource set is incomplete.")
	}
	for _, v := range observed.Resources {
		if v.State != "running" && v.State != "healthy" {
			return e.visualFinish(ctx, c, p, "needs_attention", "A service is not running. Inspect its observations and logs.")
		}
	}
	if err = e.Journal.saveUnit(unitState{Plan: p.Plan, Expected: expected}); err != nil {
		return err
	}
	for i := range observed.Resources {
		observed.Resources[i].Drift = false
	}
	observed.Applied = true
	if err = e.visualObservation(c, observed); err != nil {
		return e.finish(c.Id, "needs_attention", "The runtime report exceeds its supported size. Inspect the host before retrying.")
	}
	return e.finish(c.Id, "succeeded", "The reviewed revision is running and its routes were verified.")
}

func visualLabels(p deployment.Plan, resource uuid.UUID) map[string]string {
	return map[string]string{labelServer: p.ServerID.String(), labelEnvironment: p.EnvironmentID.String(), "com.astilba.unit": p.UnitID.String(), labelResource: resource.String(), labelKind: "visual-v1"}
}
func visualOwned(labels map[string]string, p deployment.Plan) bool {
	return labels[labelServer] == p.ServerID.String() && labels[labelEnvironment] == p.EnvironmentID.String() && labels["com.astilba.unit"] == p.UnitID.String() && labels[labelKind] == "visual-v1"
}
func (e *Engine) ownedContainers(ctx context.Context, p deployment.Plan) ([]container.Summary, error) {
	v, err := e.Docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelServer+"="+p.ServerID.String(), "com.astilba.unit="+p.UnitID.String(), labelKind+"=visual-v1")})
	return v.Items, err
}
func (e *Engine) ensureStorage(ctx context.Context, p deployment.Plan) error {
	type desiredNetwork struct {
		name     string
		id       uuid.UUID
		internal bool
	}
	networks := []desiredNetwork{{deployment.NetworkName(p.UnitID), p.UnitID, false}}
	for _, network := range p.Specification.Networks {
		if network.Name == "default" {
			networks[0].internal = network.Internal
			continue
		}
		networks = append(networks, desiredNetwork{deployment.NetworkName(network.ID), network.ID, network.Internal})
	}
	for _, network := range networks {
		n, err := e.Docker.NetworkInspect(ctx, network.name, client.NetworkInspectOptions{})
		if errdefs.IsNotFound(err) {
			_, err = e.Docker.NetworkCreate(ctx, network.name, client.NetworkCreateOptions{Driver: "bridge", Internal: network.internal, Labels: visualLabels(p, network.id)})
			if err != nil {
				return errors.New("Cannot create the deployment network.")
			}
			n, err = e.Docker.NetworkInspect(ctx, network.name, client.NetworkInspectOptions{})
		}
		if err != nil || !visualOwned(n.Network.Labels, p) || n.Network.Driver != "bridge" || n.Network.Internal != network.internal {
			return errors.New("Deployment network ownership or settings do not match.")
		}
	}
	for _, vol := range p.Specification.Volumes {
		v, err := e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		if errdefs.IsNotFound(err) {
			_, err = e.Docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: deployment.VolumeName(vol.ID), Driver: "local", Labels: visualLabels(p, vol.ID)})
			if err != nil {
				return errors.New("Cannot create a data volume.")
			}
			v, err = e.Docker.VolumeInspect(ctx, deployment.VolumeName(vol.ID), client.VolumeInspectOptions{})
		}
		if err != nil || !visualOwned(v.Volume.Labels, p) || v.Volume.Labels[labelResource] != vol.ID.String() || v.Volume.Driver != "local" || len(v.Volume.Options) > 0 {
			return errors.New("Data volume ownership or driver does not match.")
		}
	}
	return nil
}
func (e *Engine) composeVersion(ctx context.Context) (string, error) {
	return composeVersion(ctx, e.DockerCLI)
}
func composeVersion(ctx context.Context, cli string) (string, error) {
	if cli == "" {
		cli = "docker"
	}
	out, err := exec.CommandContext(ctx, cli, "compose", "version", "--short").Output()
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "v")
	if err != nil || version != "5.6.1" {
		return version, errors.New("unsupported Compose version")
	}
	return version, nil
}

type boundedOutput struct {
	b           bytes.Buffer
	limit       int
	truncated   bool
	stopOnLimit bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := w.limit - w.b.Len()
	if remaining > 0 {
		w.b.Write(p[:min(remaining, n)])
	}
	if n > remaining {
		w.truncated = true
		if w.stopOnLimit {
			return max(remaining, 0), errors.New("output limit exceeded")
		}
	}
	return n, nil
}
func (e *Engine) compose(ctx context.Context, p deployment.Plan, body []byte, secrets map[string]string, args ...string) error {
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	host := e.Docker.DaemonHost()
	if !strings.HasPrefix(host, "unix:///") {
		return errors.New("Compose requires the selected local Docker socket")
	}
	directory := e.Journal.dir
	if p.Specification.Compose != nil {
		directory = e.sourceDirectory(p)
	}
	args = append([]string{"--host", host, "compose", "--ansi", "never", "--progress", "plain", "--env-file", "/dev/null", "--project-name", deployment.UnitName(p.UnitID), "--project-directory", directory, "--file", "-"}, args...)
	cmd := exec.CommandContext(ctx, cli, args...)
	cmd.Stdin = bytes.NewReader(body)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.Journal.dir, "DOCKER_CONFIG=" + e.Journal.dir + "/docker"}
	if p.Specification.Compose != nil {
		for _, id := range domain.SecretReferences(p.Specification) {
			if value, ok := secrets[id.String()]; ok {
				cmd.Env = append(cmd.Env, deployment.SecretEnvironmentName(id)+"="+value)
			}
		}
	}
	cmd.Dir = e.Journal.dir
	out := boundedOutput{limit: 1 << 20}
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		if out.truncated {
			return errors.New("Compose output exceeded its diagnostic limit; inspect the host")
		}
		return fmt.Errorf("Compose did not complete: %s", out.b.String())
	}
	return nil
}
func redactValues(text string, values map[string]string) string {
	patterns := map[string]bool{}
	for _, value := range values {
		if value == "" {
			continue
		}
		for _, part := range append([]string{value}, strings.Split(value, "\n")...) {
			if part == "" {
				continue
			}
			patterns[part] = true
			patterns[url.QueryEscape(part)] = true
			patterns[strings.TrimPrefix(url.UserPassword("", part).String(), ":")] = true
		}
	}
	// Mark every matching interval before replacing. Consuming one match first can
	// hide the start of another secret and expose its suffix.
	coverage := make([]int, len(text)+1)
	for value := range patterns {
		for start := 0; start < len(text); {
			index := strings.Index(text[start:], value)
			if index < 0 {
				break
			}
			index += start
			coverage[index]++
			coverage[index+len(value)]--
			start = index + 1
		}
	}
	var out strings.Builder
	depth := 0
	redacting := false
	for i := 0; i < len(text); i++ {
		depth += coverage[i]
		if depth > 0 {
			if !redacting {
				out.WriteString("[redacted]")
			}
			redacting = true
		} else {
			out.WriteByte(text[i])
			redacting = false
		}
	}
	return out.String()
}
func safeVisualError(err error, secrets map[string]string) string {
	text := redactValues(err.Error(), secrets)
	if len(text) > 900 {
		text = text[:900]
	}
	return text
}
func readVisualLog(reader io.Reader, tty bool, values map[string]string, limit int) (string, error) {
	// Discard an incomplete sample; redacting a cut secret cannot protect its prefix.
	out := boundedOutput{limit: 1 << 20, stopOnLimit: true}
	var err error
	if tty {
		_, err = io.Copy(&out, reader)
	} else {
		_, err = stdcopy.StdCopy(&out, &out, reader)
	}
	if err != nil || out.truncated {
		return "", errors.New("log sample incomplete or too large")
	}
	text := redactValues(out.b.String(), values)
	if len(text) > limit {
		text = text[:limit]
	}
	return text, nil
}
func serviceOrder(spec domain.Specification) []domain.Application {
	apps := map[uuid.UUID]domain.Application{}
	for _, a := range spec.Applications {
		apps[a.ID] = a
	}
	out := []domain.Application{}
	done := map[uuid.UUID]bool{}
	var visit func(uuid.UUID)
	visit = func(id uuid.UUID) {
		if done[id] {
			return
		}
		done[id] = true
		a := apps[id]
		for _, dep := range domain.Dependencies(spec, a) {
			visit(dep)
		}
		out = append(out, a)
	}
	for _, a := range spec.Applications {
		visit(a.ID)
	}
	return out
}

func (e *Engine) observeUnit(ctx context.Context, p deployment.Plan, secrets map[string]string) (deployment.Observation, map[string]string, error) {
	out := deployment.Observation{ObservedAt: time.Now().UTC(), Resources: []deployment.Resource{}}
	stored, err := e.Journal.unit(p.UnitID)
	if err != nil {
		return out, nil, err
	}
	list, err := e.ownedContainers(ctx, p)
	if err != nil {
		return out, nil, err
	}
	actual := map[string]string{}
	seen := map[string]bool{}
	logBudget := 2048
	wanted := map[string]bool{}
	for _, app := range p.Specification.Applications {
		wanted[app.ID.String()] = true
	}
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i].Labels[labelResource], list[j].Labels[labelResource]
		if wanted[a] != wanted[b] {
			return wanted[a]
		}
		return a < b
	})
	for _, item := range list {
		id, err := uuid.Parse(item.Labels[labelResource])
		if err != nil || !visualOwned(item.Labels, p) {
			return out, nil, errors.New("invalid owned resource labels")
		}
		v, err := e.Docker.ContainerInspect(ctx, item.ID, client.ContainerInspectOptions{})
		if err != nil {
			return out, nil, err
		}
		inspect := v.Container
		attachments := map[string][]string{}
		if inspect.NetworkSettings != nil {
			for name, endpoint := range inspect.NetworkSettings.Networks {
				aliases := append([]string(nil), endpoint.Aliases...)
				sort.Strings(aliases)
				attachments[name] = aliases
			}
		}
		sort.Slice(inspect.Mounts, func(i, j int) bool {
			if inspect.Mounts[i].Destination == inspect.Mounts[j].Destination {
				return inspect.Mounts[i].Source < inspect.Mounts[j].Source
			}
			return inspect.Mounts[i].Destination < inspect.Mounts[j].Destination
		})
		fingerprint := deployment.Hash([]any{inspect.Image, inspect.Config, inspect.HostConfig, inspect.Mounts, inspect.State.Running, attachments})
		actual[id.String()] = fingerprint
		seen[id.String()] = true
		state := string(inspect.State.Status)
		if inspect.State.Running {
			state = "running"
		}
		if inspect.State.Health != nil {
			state = string(inspect.State.Health.Status)
		}
		resource := deployment.Resource{ID: id, Kind: "service", State: state, Image: inspect.Config.Image, Drift: stored.Expected[id.String()] != "" && stored.Expected[id.String()] != fingerprint}
		if stored.Expected[id.String()] == "" && stored.Plan.ID != uuid.Nil {
			resource.Drift = true
		}
		logsSafe := true
		if p.Specification.Compose != nil {
			logsSafe = inspect.Config.Labels["com.astilba.plan"] == p.Hash
			for _, ref := range domain.SecretReferences(p.Specification) {
				if _, ok := secrets[ref.String()]; !ok {
					logsSafe = false
				}
			}
			if !logsSafe {
				resource.Note = "Logs withheld: this container needs its own revision's secret values for redaction."
			}
		}
		if logsSafe && logBudget > 0 && len(out.Resources) < 40 {
			logctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			logs, err := e.Docker.ContainerLogs(logctx, item.ID, client.ContainerLogsOptions{ShowStdout: true, ShowStderr: true, Tail: "10"})
			if err == nil {
				redactions := map[string]string{}
				for key, value := range secrets {
					redactions[key] = value
				}
				for _, variable := range inspect.Config.Env {
					if _, value, ok := strings.Cut(variable, "="); ok && value != "" {
						redactions[variable] = value
					}
				}
				resource.Logs, _ = readVisualLog(logs, inspect.Config.Tty, redactions, min(logBudget, 512))
				logs.Close()
				logBudget -= len(resource.Logs)
			}
			cancel()
		}
		if len(out.Resources) < 40 {
			statsCtx, stop := context.WithTimeout(ctx, time.Second)
			stats, statsErr := e.Docker.ContainerStats(statsCtx, item.ID, client.ContainerStatsOptions{})
			if statsErr == nil {
				var sample struct {
					MemoryStats struct {
						Usage uint64 `json:"usage"`
					} `json:"memory_stats"`
				}
				if json.NewDecoder(io.LimitReader(stats.Body, 65536)).Decode(&sample) == nil {
					resource.MemoryBytes = int64(sample.MemoryStats.Usage)
				}
				stats.Body.Close()
			}
			stop()
		}
		out.Resources = append(out.Resources, resource)
	}
	for id := range stored.Expected {
		if !seen[id] {
			parsed, _ := uuid.Parse(id)
			out.Resources = append(out.Resources, deployment.Resource{ID: parsed, Kind: "service", State: "missing", Drift: true, Note: "A previously observed service is absent."})
		}
	}
	routes, err := e.Journal.routing()
	if err != nil {
		return out, nil, err
	}
	out.InfrastructureDrift, err = e.ingressDrift(ctx, routes)
	if err != nil {
		return out, nil, err
	}
	out.StateHash = deployment.Hash([]any{actual, routes.Current.Sets[p.UnitID.String()], out.InfrastructureDrift})
	return out, actual, nil
}

func (e *Engine) waitHTTP(ctx context.Context, c api.ExecutionCommand, p deployment.Plan, a domain.Application) error {
	name := "astilba-health-" + c.Id.String() + "-" + a.ID.String()
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if err == nil {
		if v.Container.Config.Labels["com.astilba.health"] != c.Id.String() || (v.Container.Config.Labels[labelServer] != p.ServerID.String() || v.Container.Config.Labels["com.astilba.unit"] != p.UnitID.String() || v.Container.Config.Labels[labelKind] != "health-v1") {
			return errors.New("health-check ownership conflict")
		}
		_, _ = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
	} else if !errdefs.IsNotFound(err) {
		return err
	}
	url := fmt.Sprintf("http://%s:%d%s", deployment.ServiceName(a.ID), a.Health.Port, a.Health.Path)
	for range 20 {
		if err := ctx.Err(); err != nil {
			return err
		}
		v, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"wget", "-T", "2", "-q", "--spider", url}, Labels: healthLabels(c, p)}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(deployment.NetworkName(p.UnitID)), ReadonlyRootfs: true, CapDrop: []string{"ALL"}, Resources: container.Resources{Memory: 32 << 20, PidsLimit: ptr(int64(16))}}})
		if err != nil {
			return err
		}
		_, err = e.Docker.ContainerStart(ctx, v.ID, client.ContainerStartOptions{})
		if err == nil {
			wait := e.Docker.ContainerWait(ctx, v.ID, client.ContainerWaitOptions{Condition: container.WaitConditionNotRunning})
			select {
			case s := <-wait.Result:
				if s.StatusCode == 0 {
					_, _ = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{})
					return nil
				}
			case <-wait.Error:
			case <-ctx.Done():
			}
		}
		_, _ = e.Docker.ContainerRemove(ctx, v.ID, client.ContainerRemoveOptions{Force: true})
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return errors.New("HTTP readiness check did not pass")
}

func healthLabels(c api.ExecutionCommand, p deployment.Plan) map[string]string {
	labels := visualLabels(p, c.ResourceId)
	labels["com.astilba.health"] = c.Id.String()
	labels[labelKind] = "health-v1"
	return labels
}

func (e *Engine) pullImage(ctx context.Context, image string) error {
	return e.pullImageAuth(ctx, image, "")
}
func (e *Engine) pullImageAuth(ctx context.Context, image, auth string) error {
	stream, err := e.Docker.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return err
	}
	defer stream.Close()
	decoder := json.NewDecoder(stream)
	for {
		var message struct {
			Error string `json:"error"`
		}
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if message.Error != "" {
			return errors.New("registry rejected the image pull")
		}
	}
}

func resolutionObservation(observed deployment.Observation) deployment.Observation {
	for _, resource := range observed.Resources {
		observed.ResourceDrift = observed.ResourceDrift || resource.Drift
	}
	observed.Resources = []deployment.Resource{}
	return observed
}

func (e *Engine) sourceDirectory(p deployment.Plan) string {
	return filepath.Join(e.Journal.dir, "bundles", p.UnitID.String(), p.ID.String(), "files")
}
func (e *Engine) prepareSourceFiles(p deployment.Plan) (string, error) {
	if p.Specification.Compose == nil {
		return e.Journal.dir, nil
	}
	directory := e.sourceDirectory(p)
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	for _, file := range p.Specification.Compose.Files {
		if err := root.MkdirAll(filepath.Dir(file.Path), 0700); err != nil {
			return "", err
		}
		existing, err := root.Lstat(file.Path)
		if err == nil {
			if !existing.Mode().IsRegular() {
				return "", errors.New("bundle entry is not a regular file")
			}
			data, err := root.ReadFile(file.Path)
			if err != nil || string(data) != file.Content {
				return "", errors.New("bundle file differs from the frozen plan")
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		temporary := filepath.Join(filepath.Dir(file.Path), ".astilba-"+uuid.NewString()+".tmp")
		f, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return "", err
		}
		_, writeErr := f.WriteString(file.Content)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
			_ = root.Remove(temporary)
			return "", err
		}
		e.point("compose-source-written")
		if err := root.Rename(temporary, file.Path); err != nil {
			_ = root.Remove(temporary)
			return "", err
		}
		parent, err := root.Open(filepath.Dir(file.Path))
		if err != nil {
			return "", err
		}
		syncErr = parent.Sync()
		closeErr = parent.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return "", err
		}
	}
	return directory, nil
}

func visualRegistryAuth(p deployment.Payload, key string) string {
	c, ok := p.RegistryAccess[key]
	if !ok {
		return ""
	}
	raw, _ := json.Marshal(map[string]string{"username": c.Username, "password": c.Password})
	return base64.URLEncoding.EncodeToString(raw)
}
