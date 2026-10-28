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
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
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
	config := &container.Config{Image: pipeline.BuilderImage, Entrypoint: []string{"sleep"}, Cmd: []string{"infinity"}, Labels: map[string]string{labelServer: c.ServerId.String(), labelResource: c.Id.String(), labelEnvironment: c.EnvironmentId.String(), labelKind: "pipeline-v1", "com.astilba.plan": c.PlanHash}}
	build := p.IsBuild()
	memory := int64(512 << 20)
	if build {
		memory = int64(p.Build.Limits().MemoryMiB) << 20
	}
	host := &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "host", Privileged: build, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Tmpfs: map[string]string{"/workspace": "rw,exec,nosuid,nodev,size=256m,mode=0700", "/tmp": "rw,nosuid,nodev,size=32m", "/run": "rw,nosuid,nodev,size=16m", "/var/lib/buildkit": "rw,exec,nosuid,size=1024m"}, Resources: container.Resources{Memory: memory, MemorySwap: memory, NanoCPUs: 2_000_000_000, PidsLimit: int64Pointer(256)}}
	if build {
		limits := p.Build.Limits()
		if p.CacheScope != "" {
			host.Tmpfs[cacheContainerDir] = "rw,nosuid,nodev,noexec,size=256m,mode=0700"
		}
		host.Resources.NanoCPUs = int64(limits.CPUs) * 1_000_000_000
		host.Tmpfs["/workspace"] = fmt.Sprintf("rw,exec,nosuid,nodev,size=%dm,mode=0700", limits.SourceMiB)
		host.Tmpfs["/var/lib/buildkit"] = fmt.Sprintf("rw,exec,nosuid,size=%dm", limits.StateMiB)
		host.ReadonlyRootfs = false
		host.CapDrop = nil
		host.SecurityOpt = nil
		config.Entrypoint = []string{"buildkitd"}
		config.Cmd = []string{"--addr", "unix:///run/buildkit/buildkitd.sock", "--oci-worker-gc", "--oci-worker-gc-keepstorage", strconv.Itoa(limits.StateMiB / 2)}
		if p.Registry != nil {
			config.Env = []string{"ASTILBA_REGISTRY_HOST=" + strings.Split(p.Registry.Repository, "/")[0]}
			config.Entrypoint = []string{"sh", "-ec"}
			config.Env = append(config.Env, "ASTILBA_GC_MIB="+strconv.Itoa(limits.StateMiB/2))
			config.Cmd = []string{`mkdir -p /etc/buildkit; printf '[registry."%s"]\n  http = false\n  insecure = false\n' "$ASTILBA_REGISTRY_HOST" > /etc/buildkit/buildkitd.toml; exec buildkitd --config /etc/buildkit/buildkitd.toml --addr unix:///run/buildkit/buildkitd.sock --oci-worker-gc --oci-worker-gc-keepstorage "$ASTILBA_GC_MIB"`}
		}

	}
	made, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: pipelineName(c), Config: config, HostConfig: host})
	if err != nil {
		return errors.New("cannot create bounded runner container")
	}
	if _, err = e.Docker.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
		return errors.New("cannot start bounded runner container")
	}
	return nil
}
func int64Pointer(value int64) *int64 { return &value }
func gitEnvironment(p pipeline.Command) []string {
	credential := pipeline.Credential{}
	if p.Access != nil {
		credential = p.Access.Clone
		if p.Action == "submit" {
			credential = p.Access.API
		}
	}
	endpoint := pipeline.GitURL(p.Connection)
	environment := []string{"HOME=/workspace", "LC_ALL=C", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ALLOW_PROTOCOL=https:http", "GIT_LITERAL_PATHSPECS=1", "GIT_INDEX_FILE=/workspace/index",
		"GIT_CONFIG_COUNT=5", "GIT_CONFIG_KEY_0=http." + endpoint + ".extraHeader", "GIT_CONFIG_VALUE_0=Authorization: Basic " + base64.StdEncoding.EncodeToString([]byte(credential.Username+":"+credential.Password)),
		"GIT_CONFIG_KEY_1=http.followRedirects", "GIT_CONFIG_VALUE_1=false", "GIT_CONFIG_KEY_2=core.hooksPath", "GIT_CONFIG_VALUE_2=/dev/null", "GIT_CONFIG_KEY_3=credential.helper", "GIT_CONFIG_VALUE_3=", "GIT_CONFIG_KEY_4=transfer.fsckObjects", "GIT_CONFIG_VALUE_4=true",
		"GIT_AUTHOR_NAME=Astilba", "GIT_AUTHOR_EMAIL=astilba@users.noreply.github.com", "GIT_COMMITTER_NAME=Astilba", "GIT_COMMITTER_EMAIL=astilba@users.noreply.github.com", "GIT_AUTHOR_DATE=" + p.SubmittedAt.Format(time.RFC3339), "GIT_COMMITTER_DATE=" + p.SubmittedAt.Format(time.RFC3339)}
	if credential.Password == "" {
		for i, v := range environment {
			if strings.HasPrefix(v, "GIT_CONFIG_VALUE_0=") {
				environment[i] = "GIT_CONFIG_VALUE_0="
			}
		}
	}
	return environment
}

func (e *Engine) pipelineGit(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, input []byte, args ...string) ([]byte, error) {
	return e.pipelineCLI(ctx, pipelineName(c), gitEnvironment(p), input, 1<<20, append([]string{"git", "--git-dir=/workspace/repo"}, args...)...)
}
func (e *Engine) clonePipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) error {
	if _, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "mkdir", "-p", "/workspace/repo"); err != nil {
		return err
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "init", "--bare", "--quiet"); err != nil {
		return err
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "remote", "add", "origin", pipeline.GitURL(p.Connection)); err != nil {
		return err
	}
	fetch := p.Commit
	if p.Preview != nil {
		fetch = pipeline.PullRequestRef(p.Connection.Provider, p.Preview.Number)
	}
	if _, err := e.pipelineGit(ctx, c, p, nil, "fetch", "--quiet", "--depth=1", "--no-tags", "origin", fetch); err != nil {
		return errors.New("exact-commit fetch failed or exceeded the source storage budget")
	}
	out, err := e.pipelineGit(ctx, c, p, nil, "rev-parse", "FETCH_HEAD")
	if err != nil || strings.TrimSpace(string(out)) != p.Commit {
		return errors.New("fetched commit differs from assigned source")
	}
	return nil
}
func (e *Engine) sourceFile(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, name string) (composeproject.File, string, error) {
	file := composeproject.File{Path: name}
	if !composeproject.ValidPath(name) || strings.HasPrefix(name, ".git/") {
		return file, "", errors.New("invalid selected source file")
	}
	listing, err := e.pipelineGit(ctx, c, p, nil, "ls-tree", "-z", p.Commit, "--", name)
	if err != nil {
		return file, "", err
	}
	metadata, filename, ok := strings.Cut(strings.TrimSuffix(string(listing), "\x00"), "\t")
	parts := strings.Fields(metadata)
	if !ok || filename != name || len(parts) != 3 || (parts[0] != "100644" && parts[0] != "100755") || parts[1] != "blob" {
		return file, "", errors.New("source must be a regular tracked file; symlinks and submodules are unsupported")
	}
	size, err := e.pipelineGit(ctx, c, p, nil, "cat-file", "-s", p.Commit+":"+name)
	if err != nil {
		return file, "", err
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(size)))
	if err != nil || n > 65536 {
		return file, "", errors.New("selected source exceeds its file limit")
	}
	content, err := e.pipelineGit(ctx, c, p, nil, "show", p.Commit+":"+name)
	if err != nil {
		return file, "", err
	}
	file.Content = string(content)
	return file, parts[0], nil
}
func (e *Engine) importPipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) (pipeline.Result, error) {
	result := pipeline.Result{Commit: p.Commit, Files: []composeproject.File{}}
	for _, name := range p.Source.Paths {
		file, _, err := e.sourceFile(ctx, c, p, name)
		if err != nil {
			return result, err
		}
		result.Files = append(result.Files, file)
	}
	b := composeproject.Bundle{Namespace: p.UnitID, Entrypoints: p.Source.Entrypoints, Files: result.Files}
	return result, b.Validate()
}
func (e *Engine) remotePipelineHead(ctx context.Context, c api.ExecutionCommand, p pipeline.Command, branch string) (string, error) {
	out, err := e.pipelineGit(ctx, c, p, nil, "ls-remote", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != "refs/heads/"+branch || !pipeline.ValidCommit(fields[0]) {
		return "", errors.New("invalid remote branch")
	}
	return fields[0], nil
}
func (e *Engine) submitPipeline(ctx context.Context, c api.ExecutionCommand, p pipeline.Command) (pipeline.Result, error) {
	result := pipeline.Result{Commit: p.Commit}
	head, err := e.remotePipelineHead(ctx, c, p, p.Branch)
	if err != nil {
		return result, err
	}
	if head != p.Commit {
		return result, errors.New("source branch changed; reconcile this draft")
	}
	if _, err = e.pipelineGit(ctx, c, p, nil, "read-tree", p.Commit); err != nil {
		return result, err
	}
	old := map[string]bool{}
	for _, name := range p.Source.Paths {
		old[name] = true
	}
	current := map[string]bool{}
	for _, file := range p.Bundle.Files {
		current[file.Path] = true
		mode := "100644"
		if old[file.Path] {
			previous, m, err := e.sourceFile(ctx, c, p, file.Path)
			if err != nil {
				return result, err
			}
			mode = m
			if previous.Content == file.Content {
				continue
			}
		}
		blob, err := e.pipelineGit(ctx, c, p, []byte(file.Content), "hash-object", "-w", "--stdin")
		if err != nil {
			return result, err
		}
		if _, err = e.pipelineGit(ctx, c, p, nil, "update-index", "--add", "--cacheinfo", mode, strings.TrimSpace(string(blob)), file.Path); err != nil {
			return result, err
		}
	}
	for name := range old {
		if !current[name] {
			if _, err = e.pipelineGit(ctx, c, p, nil, "update-index", "--force-remove", "--", name); err != nil {
				return result, err
			}
		}
	}
	tree, err := e.pipelineGit(ctx, c, p, nil, "write-tree")
	if err != nil {
		return result, err
	}
	original, err := e.pipelineGit(ctx, c, p, nil, "rev-parse", p.Commit+"^{tree}")
	if err != nil {
		return result, err
	}
	if bytes.Equal(tree, original) {
		return result, errors.New("source draft has no changes")
	}
	commit, err := e.pipelineGit(ctx, c, p, []byte("Update deployment configuration\n\nAstilba-Submission: "+p.SubmissionID.String()+"\n"), "commit-tree", strings.TrimSpace(string(tree)), "-p", p.Commit)
	if err != nil {
		return result, err
	}
	target := strings.TrimSpace(string(commit))
	branch := "astilba/" + p.SubmissionID.String()
	existing, err := e.remotePipelineHead(ctx, c, p, branch)
	if err != nil {
		return result, err
	}
	if existing != "" && existing != target {
		return result, errors.New("submission branch already belongs to another change")
	}
	if existing == "" {
		// The empty lease permits creation only, including against a racing fast-forward.
		if _, err = e.pipelineGit(ctx, c, p, nil, "push", "--quiet", "--force-with-lease=refs/heads/"+branch+":", "origin", target+":refs/heads/"+branch); err != nil {
			return result, errors.New("create-only submission branch failed")
		}
	}
	head, err = e.remotePipelineHead(ctx, c, p, p.Branch)
	if err != nil {
		return result, err
	}
	if head != p.Commit {
		return result, errors.New("source branch changed before review creation")
	}
	u, _ := url.Parse(p.Connection.BaseURL)
	provider, err := pipeline.NewProvider(p.Connection, p.Access.API, pipeline.Policy{Origins: []string{u.Scheme + "://" + u.Host}, Development: u.Scheme == "http"})
	if err != nil {
		return result, err
	}
	review, err := provider.Review(ctx, p.Branch, branch)
	if err != nil {
		return result, errors.New("review request failed; retry resumes the same branch")
	}
	if review.Number < 1 {
		return result, errors.New("provider omitted review identity")
	}
	result.Commit = target
	result.ReviewURL = review.URL
	result.ReviewNumber = review.Number
	return result, nil
}
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
	for i := 0; i < 40; i++ {
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, nil, 4096, "buildctl", "debug", "workers"); err == nil {
			break
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
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
		auth := map[string]any{"auths": map[string]any{host: map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte(p.RegistryAccess.Push.Username + ":" + p.RegistryAccess.Push.Password))}}}
		data, _ := json.Marshal(auth)
		if _, err = e.pipelineCLI(ctx, pipelineName(c), nil, data, 1024, "sh", "-ec", "umask 077; cat > /workspace/auth/config.json"); err != nil {
			return result, err
		}
		args = append(args, "--output", "type=image,name="+image+",push=true")
	} else {
		args = append(args, "--output", "type=docker,name="+image+",dest=/workspace/image.tar")
	}
	logs, err := e.pipelineLog(ctx, c, p, []string{"DOCKER_CONFIG=/workspace/auth"}, args)
	result.Logs += logs
	if len(result.Logs) > 16384 {
		result.Logs = result.Logs[len(result.Logs)-16384:]
	}
	if err != nil {
		return result, errors.New("BuildKit failed or exceeded its memory/storage budget; existing workloads are unchanged")
	}
	metadata, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 16384, "cat", "/workspace/metadata.json")
	if err != nil {
		return result, err
	}
	var info map[string]json.RawMessage
	if json.Unmarshal(metadata, &info) != nil {
		return result, errors.New("invalid BuildKit metadata")
	}
	if p.Registry != nil {
		if json.Unmarshal(info["containerimage.digest"], &result.Digest) != nil || !strings.HasPrefix(result.Digest, "sha256:") || len(result.Digest) != 71 {
			return result, errors.New("BuildKit omitted immutable registry digest")
		}
		result.Image = p.Registry.Repository + "@" + result.Digest
	} else {
		size, err := e.pipelineCLI(ctx, pipelineName(c), nil, nil, 1024, "stat", "-c", "%s", "/workspace/image.tar")
		if err != nil {
			return result, err
		}
		n, err := strconv.ParseUint(strings.TrimSpace(string(size)), 10, 64)
		if err != nil || n > 512<<20 || dockerFreeBytes(ctx, e.Docker) < (4<<30)+4*int64(n) {
			return result, errors.New("local image exceeds reserved host storage")
		}
		if err = e.loadPipelineImage(ctx, c); err != nil {
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
		values["api"] = p.Access.API.Password
	}
	if p.RegistryAccess != nil {
		values["push"] = p.RegistryAccess.Push.Password
		values["pull"] = p.RegistryAccess.Pull.Password
	}
	logs := redactValues(out.b.String(), values)
	if len(logs) > 16384 {
		logs = logs[len(logs)-16384:]
	}
	return logs, err
}
func (e *Engine) loadPipelineImage(ctx context.Context, c api.ExecutionCommand) error {
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
	response, err := e.Docker.ImageLoad(ctx, stream)
	if err == nil {
		_, err = io.Copy(io.Discard, response)
		_ = response.Close()
	}
	wait := cmd.Wait()
	return errors.Join(err, wait)
}
func (e *Engine) executePipeline(ctx context.Context, c api.ExecutionCommand, r Record) error {
	var p pipeline.Command
	if c.Pipeline == nil || json.Unmarshal(*c.Pipeline, &p) != nil || p.Digest() != c.PlanHash || p.Validate() != nil {
		return e.finish(c.Id, "failed", "Pipeline identity or inputs do not match the assigned operation.")
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
	case "submit":
		if err = e.checkpoint(c.Id, "submission"); err == nil {
			result, err = e.submitPipeline(ctx, c, p)
		}
	case "build", "build-preview":
		if err = e.checkpoint(c.Id, "build"); err == nil {
			result, err = e.buildPipeline(ctx, c, p)
		}
	}
	if e.cancelled(c.Id) || ctx.Err() != nil {
		return e.finishPipeline(c, "cancelled", "Build/source assignment was cancelled or expired; no artifact is eligible for deployment.")
	}
	if err != nil {
		raw, _ := json.Marshal(result)
		_ = e.Journal.Mutate(c.Id, func(record *Record) { message := json.RawMessage(raw); record.Report.Pipeline = &message })
		return e.pipelineFailure(ctx, c, err)
	}
	raw, err := json.Marshal(result)
	if err != nil || len(raw) > 150000 {
		return e.finishPipeline(c, "failed", "Pipeline result exceeds its supported size.")
	}
	if err = e.Journal.Mutate(c.Id, func(record *Record) { message := json.RawMessage(raw); record.Report.Pipeline = &message }); err != nil {
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
