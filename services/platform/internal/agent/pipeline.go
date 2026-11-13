package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func pipelineName(c api.ExecutionCommand) string { return "astilba-pipeline-" + c.Id.String() }
func (e *Engine) pipelineCLI(ctx context.Context, name string, env []string, input []byte, limit int, args ...string) ([]byte, error) {
	cmd := e.pipelineExec(ctx, name, env, args...)
	cmd.Stdin = bytes.NewReader(input)
	output := &boundedOutput{limit: limit, stopOnLimit: true}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, errors.New("runner tool failed")
	}
	return output.b.Bytes(), nil
}

func (e *Engine) pipelineExec(ctx context.Context, name string, env []string, args ...string) *exec.Cmd {
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	command := []string{"exec", "--interactive"}
	environment := []string{"PATH=" + os.Getenv("PATH"), "HOME=" + e.Journal.dir, "DOCKER_HOST=" + e.Docker.DaemonHost()}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		environment = append(environment, "DOCKER_CONFIG="+v)
	}
	for _, v := range env {
		k, _, _ := strings.Cut(v, "=")
		// Docker uses this on the host too; keep container auth independent of CLI wrappers.
		if k == "DOCKER_CONFIG" {
			command = append(command, "--env", v)
			continue
		}
		command = append(command, "--env", k)
		environment = append(environment, v)
	}
	command = append(command, name)
	command = append(command, args...)
	cmd := exec.CommandContext(ctx, cli, command...)
	cmd.Env = environment
	return cmd
}
func (e *Engine) removePipeline(ctx context.Context, c api.ExecutionCommand) error {
	for _, v := range []struct{ name, image, kind string }{{pipelineName(c), pipeline.BuilderImage, "pipeline-v1"}, {pipelineName(c) + "-planner", pipeline.RailpackFrontend, "pipeline-planner-v1"}} {
		if err := e.removePipelineContainer(ctx, c, v.name, v.image, v.kind); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) removePipelineContainer(ctx context.Context, c api.ExecutionCommand, name, image, kind string) error {
	v, err := e.Docker.ContainerInspect(ctx, name, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	labels := v.Container.Config.Labels
	if labels[labelServer] != c.ServerId.String() || labels[labelResource] != c.Id.String() || labels[labelEnvironment] != c.EnvironmentId.String() || labels[labelKind] != kind || labels["com.astilba.plan"] != c.PlanHash || v.Container.Config.Image != image {
		return errors.New("runner container ownership differs")
	}
	_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	return err
}
func (e *Engine) pipelineContainer(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) error {
	if dockerFreeBytes(ctx, e.Docker) < 4<<30 {
		return errors.New("build requires at least 4 GiB of free host storage")
	}
	// A previous process may still be working after management or agent interruption.
	// Remove only this exact operation before replaying its frozen inputs.
	if err := e.removePipeline(ctx, c); err != nil {
		return err
	}
	if _, err := e.Docker.ImageInspect(ctx, pipeline.BuilderImage); err != nil {
		if err = e.pullImage(ctx, pipeline.BuilderImage); err != nil {
			return errors.New("cannot obtain the pinned builder")
		}
	}
	mounts, err := e.builderMounts()
	if err != nil {
		return errors.New("cannot prepare the builder's accounts")
	}
	if e.builderCAs != "" {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: e.builderCAs, Target: "/etc/ssl/certs/ca-certificates.crt", ReadOnly: true})
	}
	// Every runner container runs as the builder's unprivileged user. Its writable places are
	// its own, and source, secrets and BuildKit's state stay in memory.
	owned := fmt.Sprintf(",uid=%d,gid=%d", builderUID, builderUID)
	user := fmt.Sprintf("%d:%d", builderUID, builderUID)
	config := &container.Config{Image: pipeline.BuilderImage, User: user, Entrypoint: []string{"sleep"}, Cmd: []string{"infinity"}, Env: []string{"HOME=/workspace", "TMPDIR=/tmp", "XDG_RUNTIME_DIR=/run"}, Labels: map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-v1", "com.astilba.plan": c.PlanHash}}
	build := p.IsBuild()
	memory := int64(512 << 20)
	if build {
		memory = int64(p.Build.Limits().MemoryMiB) << 20
	}
	host := &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "host", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Mounts: mounts, LogConfig: container.LogConfig{Type: "json-file", Config: map[string]string{"max-size": "1m", "max-file": "1"}}, Tmpfs: map[string]string{"/workspace": "rw,exec,nosuid,nodev,size=256m,mode=0700" + owned, "/tmp": "rw,nosuid,nodev,size=32m,mode=0700" + owned, "/run": "rw,nosuid,nodev,size=16m,mode=0700" + owned, "/var/lib/buildkit": "rw,exec,nosuid,nodev,size=1024m,mode=0700" + owned}, Resources: container.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: 2_000_000_000, PidsLimit: int64Pointer(256)}}
	if build {
		limits := p.Build.Limits()
		if p.CacheScope != "" {
			host.Tmpfs[cacheContainerDir] = "rw,nosuid,nodev,noexec,size=256m,mode=0700" + owned
		}
		host.Resources.NanoCPUs = int64(limits.CPUs) * 1_000_000_000
		host.Tmpfs["/workspace"] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm,mode=0700", limits.SourceMiB) + owned
		host.Tmpfs["/var/lib/buildkit"] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm,mode=0700", limits.StateMiB) + owned
		// Rootless BuildKit, see builder.go: no host capability but the two newuidmap and
		// newgidmap use to map its user namespace, which is also why no-new-privileges is off.
		// BuildKit mounts a fresh /proc and /sys for each build step, which the kernel
		// refuses over the ones Docker masks, so the AppArmor profile hides those instead.
		security, err := builderSecurity(ctx)
		if err != nil {
			return err
		}
		host.SecurityOpt = security
		host.CapAdd = []string{"CAP_SETUID", "CAP_SETGID"}
		host.MaskedPaths = []string{}
		host.ReadonlyPaths = []string{}
		// The policy is in place before the build container exists, so nothing in it runs
		// on a network without one.
		resolvers, err := e.prepareBuildNetwork(ctx, c, p)
		if err != nil {
			return err
		}
		host.NetworkMode = container.NetworkMode(buildNetworkName)
		nameservers := []string{}
		for _, r := range resolvers {
			nameservers = append(nameservers, strconv.Quote(r.String()))
		}
		config.Env = append(config.Env, "BUILDKIT_HOST=unix:///run/buildkit/buildkitd.sock", "ASTILBA_GC_MIB="+strconv.Itoa(limits.StateMiB/2), "ASTILBA_NAMESERVERS="+strings.Join(nameservers, ", "))
		if p.Registry != nil {
			config.Env = append(config.Env, "ASTILBA_REGISTRY_HOST="+strings.Split(p.Registry.Repository, "/")[0])
		}
		// RUN steps share the build container's network, which the policy filters on the host.
		// BuildKit replaces Docker's loopback resolver for them, so it is given the host's.
		// rootlesskit creates the user namespace and keeps that network, so the policy holds.
		config.Entrypoint = []string{"sh", "-ec"}
		config.Cmd = []string{`mkdir -p /run/buildkit; : > /run/buildkit/buildkitd.toml
if [ -n "$ASTILBA_REGISTRY_HOST" ]; then printf '[registry."%s"]\n  http = false\n  insecure = false\n' "$ASTILBA_REGISTRY_HOST" >> /run/buildkit/buildkitd.toml; fi
if [ -n "$ASTILBA_NAMESERVERS" ]; then printf '[dns]\n  nameservers = [%s]\n' "$ASTILBA_NAMESERVERS" >> /run/buildkit/buildkitd.toml; fi
exec rootlesskit --net=host buildkitd --config /run/buildkit/buildkitd.toml --root /var/lib/buildkit --addr unix:///run/buildkit/buildkitd.sock --oci-worker-net host --oci-worker-gc --oci-worker-gc-keepstorage "$ASTILBA_GC_MIB"`}
	}
	made, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: pipelineName(c), Config: config, HostConfig: host})
	if err != nil {
		return errors.New("cannot create bounded runner container")
	}
	if _, err = e.Docker.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
		return errors.New("cannot start bounded runner container")
	}
	if build {
		return e.awaitBuilder(ctx, c)
	}
	return nil
}

// awaitBuilder waits for rootless BuildKit to answer. It exits at once when the host refuses its
// user namespace, so the build stops here with that cause rather than at its next step.
func (e *Engine) awaitBuilder(ctx context.Context, c api.ExecutionCommand) error {
	for range 40 {
		if _, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 4096, "buildctl", "debug", "workers"); err == nil {
			return nil
		}
		if v, err := e.Docker.ContainerInspect(ctx, pipelineName(c), client.ContainerInspectOptions{}); err == nil && v.Container.State != nil && !v.Container.State.Running {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return errors.New("The rootless builder did not start. Check that this server can create user namespaces, as the builder requirements in the Git builds documentation describe.")
}
func int64Pointer(value int64) *int64 { return &value }
func (e *Engine) buildPipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) (pipeline.Result, error) {
	result := pipeline.Result{Commit: p.Commit, Architecture: p.Build.Architecture, Cache: "BuildKit cache is isolated to this run and cleared at completion."}
	if p.CompareCommit != "" && !e.watchedSourceChanged(ctx, c, p) {
		result.Skipped = true
		return result, nil
	}
	listing, err := e.pipelineGit(ctx, c, p, nil, "ls-tree", "-r", p.Commit)
	if err != nil {
		return result, err
	}
	for _, line := range strings.Split(string(listing), "\n") {
		if strings.HasPrefix(line, "120000 ") || strings.HasPrefix(line, "160000 ") {
			return result, errors.New("build contexts with symlinks or submodules require an explicit supported source workflow")
		}
	}
	if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "mkdir", "-p", "/workspace/context", "/workspace/secrets", "/workspace/auth"); err != nil {
		return result, err
	}
	if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "sh", "-ec", `set -o pipefail; git --git-dir=/workspace/repo archive "$1" | tar -x -C /workspace/context`, "--", p.Commit); err != nil {
		return result, errors.New("source extraction exceeded its bounded workspace")
	}
	contextDir := path.Join("/workspace/context", p.Build.Context)
	args := []string{"buildctl", "build", "--frontend", "dockerfile.v0", "--local", "context=" + contextDir, "--local", "dockerfile=" + contextDir, "--opt", "filename=" + p.Build.Dockerfile, "--opt", "platform=linux/" + p.Build.Architecture, "--progress", "plain", "--metadata-file", "/workspace/metadata.json"}
	if p.Build.UsesRailpack() {
		prepared, logs, err := e.prepareRailpack(ctx, c, p, contextDir)
		result.Logs = logs
		if err != nil {
			return result, err
		}
		result.Railpack = &prepared.Summary
		args = []string{"buildctl", "build", "--frontend", "gateway.v0", "--local", "context=" + contextDir, "--local", "dockerfile=/workspace/prepared", "--opt", "source=" + p.Railpack.Frontend, "--opt", "platform=linux/" + p.Build.Architecture, "--source-policy-file", "/workspace/prepared/source-policy.json", "--progress", "plain", "--metadata-file", "/workspace/metadata.json"}
	}
	var cache *buildCache
	if p.CacheScope != "" {
		cache, err = openBuildCache(filepath.Join(e.Journal.dir, "build-cache-v1"))
		result.Cache = "Project layer cache unavailable; building with fresh state."
		if err == nil {
			defer cache.root.Close()
			result.Cache = "No compatible project layer cache imported."
			if e.importBuildCache(ctx, c, p, cache) {
				args = append(args, "--import-cache", "type=local,src="+cacheContainerDir+"/in")
				result.Cache = "Compatible project layer cache imported; cache hits depend on build inputs."
			}
			args = append(args, "--export-cache", "type=local,dest="+cacheContainerDir+"/cache-out,mode=max,ignore-error=true")
		}
	}
	keys := []string{}
	for k := range p.Build.Arguments {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if p.Build.UsesRailpack() {
			filename := "/workspace/secrets/" + k
			if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, []byte(p.Build.Arguments[k]), 1024, "sh", "-ec", `umask 077; cat > "$1"`, "--", filename); err != nil {
				return result, err
			}
			args = append(args, "--secret", "id="+k+",src="+filename)
		} else {
			args = append(args, "--opt", "build-arg:"+k+"="+p.Build.Arguments[k])
		}
	}
	for name, value := range p.BuildSecrets {
		filename := "/workspace/secrets/" + name
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, []byte(value), 1024, "sh", "-ec", `umask 077; cat > "$1"`, "--", filename); err != nil {
			return result, err
		}
		args = append(args, "--secret", "id="+name+",src="+filename)
	}
	image := "astilba-build-" + c.Id.String() + ":built"
	if p.Registry != nil {
		image = p.Registry.Repository + ":astilba-" + c.Id.String()
		host := strings.Split(p.Registry.Repository, "/")[0]
		if host == "docker.io" {
			host = "https://index.docker.io/v1/"
		}
		data, _ := buildKitAuthConfig(host, p.RegistryAccess.Push)
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, data, 1024, "sh", "-ec", "umask 077; cat > /workspace/auth/config.json"); err != nil {
			return result, err
		}
		args = append(args, "--output", "type=image,name="+image+",push=true")
	} else {
		args = append(args, "--output", "type=docker,name="+image+",dest=/workspace/image.tar")
	}
	logs, err := e.pipelineLog(ctx, c, p, []string{"DOCKER_CONFIG=/workspace/auth"}, args)
	result.Logs = pipelineLogTail(result.Logs + logs + e.buildRefusalNote(ctx))
	if err != nil {
		return result, errors.New("BuildKit failed or exceeded its memory/storage budget; existing workloads are unchanged")
	}
	metadata, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 16384, "cat", "/workspace/metadata.json")
	if err != nil {
		return result, err
	}
	var info map[string]jsontext.Value
	if json.Unmarshal(metadata, &info) != nil {
		return result, errors.New("invalid BuildKit metadata")
	}
	if p.Registry != nil {
		if json.Unmarshal(info["containerimage.digest"], &result.Digest) != nil || !strings.HasPrefix(result.Digest, "sha256:") || len(result.Digest) != 71 {
			return result, errors.New("BuildKit omitted immutable registry digest")
		}
		result.Image = p.Registry.Repository + "@" + result.Digest
	} else {
		// The builder's user owns the workspace and can change the archive after it is measured,
		// so Docker reads no more than the measured size.
		size, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "stat", "-c", "%s", "/workspace/image.tar")
		if err != nil {
			return result, err
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
		if err != nil || n > localImageArchiveBytes {
			return result, fmt.Errorf("The built image was not loaded: its archive is larger than %d MiB. Make the image smaller, for example with a multi-stage build. Existing workloads are unchanged.", localImageArchiveBytes>>20)
		}
		if err = e.loadPipelineImage(ctx, c, int64(n)); err != nil {
			return result, err
		}
		inspected, err := e.Docker.ImageInspect(ctx, image)
		if err != nil || inspected.Architecture != p.Build.Architecture {
			return result, errors.New("local image architecture differs from requested target")
		}
		result.Digest = inspected.ID
		result.Image = image
	}
	if cache != nil {
		if e.retainBuildCache(ctx, c, p, cache) {
			result.Cache += " Layer cache retained within project and runner limits."
		} else {
			result.Cache += " No new layer cache retained."
		}
	}
	return result, nil
}

// Only a complete diff against a fetched successful artifact may skip a build.
// Force-pushed or unavailable baselines and bounded-output failures rebuild.
func (e *Engine) watchedSourceChanged(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) bool {
	if _, err := e.pipelineGit(ctx, c, p, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "origin", p.CompareCommit); err != nil {
		return true
	}
	paths, err := e.pipelineGit(ctx, c, p, nil, "diff", "--no-renames", "--name-only", "-z", p.CompareCommit, p.Commit, "--")
	if err != nil || len(paths) > 0 && paths[len(paths)-1] != 0 {
		return true
	}
	for _, name := range bytes.Split(paths, []byte{0}) {
		if len(name) > 0 && p.Build.Watches(string(name)) {
			return true
		}
	}
	return false
}
func (e *Engine) pipelineLog(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, env, args []string) (string, error) {
	command := e.pipelineExec(ctx, pipelineName(c), env, args...)
	out := &boundedOutput{limit: 1 << 20, stopOnLimit: true}
	command.Stdout = out
	command.Stderr = out
	err := command.Run()
	if out.truncated {
		return "", errors.New("build output exceeded its capture limit")
	}
	values := map[string]string{}
	for k, v := range p.BuildSecrets {
		values["build:"+k] = v
	}
	if p.Access != nil {
		values["clone"] = p.Access.Clone.Password
	}
	if p.RegistryAccess != nil {
		values["push"] = p.RegistryAccess.Push.Password
		values["pull"] = p.RegistryAccess.Pull.Password
	}
	// Match secrets against the text JSON will carry, before retaining its tail.
	logs := redactValues(strings.ToValidUTF8(out.b.String(), "\ufffd"), values)
	return pipelineLogTail(logs), err
}

func pipelineLogTail(logs string) string {
	if len(logs) <= pipeline.LogLimit {
		return logs
	}
	start := len(logs) - pipeline.LogLimit
	for start < len(logs) && !utf8.RuneStart(logs[start]) {
		start++
	}
	return logs[start:]
}

// loadPipelineImage loads the archive the build container wrote, measuring it on the way. See
// image_archive.go.
func (e *Engine) loadPipelineImage(ctx context.Context, c api.ExecutionCommand, size int64) error {
	// The agent runs one operation at a time, so nothing else of its own writes to Docker's
	// storage between measuring it here and the load.
	limit, err := e.imageLoadLimit(ctx)
	if err != nil {
		return err
	}
	cli := e.DockerCLI
	if cli == "" {
		cli = "docker"
	}
	cmd := exec.CommandContext(ctx, cli, "exec", pipelineName(c), "cat", "/workspace/image.tar")
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + e.Docker.DaemonHost()}
	if v := os.Getenv("DOCKER_CONFIG"); v != "" {
		cmd.Env = append(cmd.Env, "DOCKER_CONFIG="+v)
	}
	stream, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err = cmd.Start(); err != nil {
		return err
	}
	// Docker reads the archive only through the gate. A refusal fails the stream it reads, so
	// Docker never receives a complete archive to import.
	reader, writer := io.Pipe()
	inspected := make(chan error, 1)
	go func() {
		err := inspectImageArchive(&imageArchiveGate{src: &measuredReader{r: stream, remaining: size}, out: writer}, limit)
		if errors.Is(err, errImageOverBytes) || errors.Is(err, errImageOverEntries) {
			err = limit.refusal(err)
		}
		_ = writer.CloseWithError(err)
		inspected <- err
	}()
	response, loaded := e.Docker.ImageLoad(ctx, reader)
	if loaded == nil {
		loaded = imageLoadOutcome(response)
		_ = response.Close()
	}
	// Docker has stopped reading, so the inspection's next write fails rather than waits, and
	// whatever the build container still sends after the archive's end is not needed.
	_ = reader.CloseWithError(errors.New("Docker stopped reading the image archive"))
	_ = cmd.Process.Kill()
	checked := <-inspected
	_ = cmd.Wait()
	var refused *imageLoadRefused
	switch {
	case errors.As(checked, &refused):
		return refused
	case loaded != nil:
		return fmt.Errorf("The built image was not loaded: Docker's image load failed: %s. Existing workloads are unchanged.", firstLine(loaded.Error(), 300))
	case checked != nil:
		return errors.New("The built image was not loaded: the agent could not read its archive from the build container. Existing workloads are unchanged.")
	}
	return nil
}

// imageLoadOutcome reads Docker's progress messages for a load, which carry its failure: Docker
// answers before it has read the archive, so the HTTP status cannot.
func imageLoadOutcome(r io.Reader) error {
	decoder := jsontext.NewDecoder(r)
	var failed error
	for {
		var message struct {
			Error string `json:"error"`
		}
		err := json.UnmarshalDecode(decoder, &message)
		if errors.Is(err, io.EOF) {
			return failed
		}
		if err != nil {
			return err
		}
		if message.Error != "" && failed == nil {
			failed = errors.New(message.Error)
		}
	}
}

func firstLine(s string, limit int) string {
	s, _, _ = strings.Cut(s, "\n")
	if len(s) > limit {
		s = strings.ToValidUTF8(s[:limit], "")
	}
	return s
}

// imageLoadLimit is what a load may write now: the host's per-load limit, or less where Docker's
// storage would otherwise drop below its reserve.
func (e *Engine) imageLoadLimit(ctx context.Context) (imageLoadLimit, error) {
	load, reserve := e.ImageLoadBytes, e.ImageLoadReserveBytes
	if load <= 0 {
		load = DefaultImageLoadBytes
	}
	if reserve <= 0 {
		reserve = DefaultImageLoadReserveBytes
	}
	bytes, inodes, ok := imageStorage(ctx, e.Docker)
	if !ok {
		return imageLoadLimit{}, errors.New("The built image was not loaded: the agent cannot observe this server's Docker storage. Existing workloads are unchanged.")
	}
	limit := imageLoadLimit{Bytes: load, Entries: load >> 30 * imageLoadEntriesPerGiB, loadBytes: load, reserveBytes: reserve}
	if room := bytes - reserve; room < limit.Bytes {
		limit.Bytes, limit.bytesByStorage = room, true
	}
	if room := inodes - dockerReserveInodes; inodes >= 0 && room < limit.Entries {
		limit.Entries, limit.entriesByStorage = room, true
	}
	if limit.Bytes <= 0 {
		return limit, limit.refusal(errImageOverBytes)
	}
	if limit.Entries <= 0 {
		return limit, limit.refusal(errImageOverEntries)
	}
	return limit, nil
}

// measuredReader reads exactly remaining bytes, and fails if r has more.
type measuredReader struct {
	r         io.Reader
	remaining int64
}

func (m *measuredReader) Read(p []byte) (int, error) {
	if m.remaining == 0 {
		if n, _ := m.r.Read(make([]byte, 1)); n > 0 {
			return 0, errors.New("the image archive grew after it was measured")
		}
		return 0, io.EOF
	}
	n, err := m.r.Read(p[:min(int64(len(p)), m.remaining)])
	m.remaining -= int64(n)
	if err == io.EOF && m.remaining > 0 {
		err = io.ErrUnexpectedEOF
	}
	return n, err
}
func (e *Engine) executePipeline(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash {
		return e.finish(c.Id, "failed", "Pipeline identity or inputs do not match the assigned operation.")
	}
	// An earlier agent may have started this operation's containers before refusing it now,
	// such as a build queued before builds carried the build network, so they are removed.
	if err := p.Validate(); err != nil {
		return e.finishPipeline(c, "failed", "Pipeline inputs are not supported: "+err.Error()+".")
	}
	if e.Docker == nil {
		return e.finish(c.Id, "failed", "Docker is unavailable.")
	}
	ctx, stopCommand := e.commandContext(ctx, c)
	defer stopCommand()
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_ = e.removePipeline(cleanup, c)
	}()
	if e.cancelled(c.Id) || ctx.Err() != nil {
		return e.finishPipeline(c, "cancelled", "Build/source assignment was cancelled or expired.")
	}
	if err := e.checkpoint(c.Id, "runner"); err != nil {
		return err
	}
	if err := e.pipelineContainer(ctx, c, p); err != nil {
		return e.pipelineFailure(ctx, c, err)
	}
	if err := e.checkpoint(c.Id, "source"); err != nil {
		return err
	}
	if err := e.clonePipeline(ctx, c, p); err != nil {
		return e.pipelineFailure(ctx, c, err)
	}
	var result pipeline.Result
	var err error
	switch p.Action {
	case "import":
		result, err = e.importPipeline(ctx, c, p)
	case "build", "build-preview":
		if err = e.checkpoint(c.Id, "build"); err == nil {
			result, err = e.buildPipeline(ctx, c, p)
		}
	}
	if e.cancelled(c.Id) || ctx.Err() != nil {
		raw, _ := stablejson.Marshal(pipeline.Result{Logs: result.Logs})
		if err := e.Journal.Mutate(c.Id, func(record *Record) { message := jsontext.Value(raw); record.Report.Pipeline = &message }); err != nil {
			return err
		}
		return e.finishPipeline(c, "cancelled", "Build/source assignment was cancelled or expired; no artifact is eligible for deployment.")
	}
	if err != nil {
		raw, _ := stablejson.Marshal(result)
		_ = e.Journal.Mutate(c.Id, func(record *Record) { message := jsontext.Value(raw); record.Report.Pipeline = &message })
		return e.pipelineFailure(ctx, c, err)
	}
	raw, err := stablejson.Marshal(result)
	if err != nil || len(raw) > 150000 {
		return e.finishPipeline(c, "failed", "Pipeline result exceeds its supported size.")
	}
	if err = e.Journal.Mutate(c.Id, func(record *Record) { message := jsontext.Value(raw); record.Report.Pipeline = &message }); err != nil {
		return err
	}
	e.point("pipeline-result")
	return e.finishPipeline(c, "succeeded", "The frozen source operation completed. Deployment remains a separately reviewed action.")
}

func (e *Engine) pipelineFailure(ctx context.Context, c api.ExecutionCommand, err error) error {
	if e.cancelled(c.Id) || ctx.Err() != nil {
		return e.finishPipeline(c, "cancelled", "Source/build execution was cancelled or expired.")
	}
	return e.finishPipeline(c, "failed", err.Error())
}

// A terminal report is durable only after the independently running container is gone.
// An interrupted process keeps its running journal entry and replays ownership cleanup.
func (e *Engine) finishPipeline(c api.ExecutionCommand, state api.ExecutionState, note string) error {
	cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := e.removePipeline(cleanup, c); err != nil {
		return e.finish(c.Id, "needs_attention", "Runner cleanup failed. Inspect the owned container and retry this operation locally.")
	}
	return e.finish(c.Id, state, note)
}

// buildKitAuthConfig writes the Docker config file BuildKit reads to push an image.
func buildKitAuthConfig(host string, push pipeline.Credential) ([]byte, error) {
	return json.Marshal(map[string]any{"auths": map[string]any{host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(push.Username + ":" + push.Password))}}})
}
