package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/composeproject"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

func TestNativePipeline(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	state := t.TempDir()
	_ = os.Chmod(state, 0700)
	journal, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	serverID := uuid.New()
	if err = journal.SetConfig(Config{ServerID: serverID, MachineID: uuid.New(), Role: "combined"}); err != nil {
		t.Fatal(err)
	}
	dockerCLI := "docker"
	if _, err := os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		dockerCLI = filepath.Join(state, "docker")
		config := filepath.Join(state, "fixture-docker")
		_ = os.Mkdir(config, 0700)
		_ = os.WriteFile(filepath.Join(config, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600)
		script := fmt.Sprintf("#!/bin/sh\nexport DOCKER_CONFIG='%s'\nexec /opt/astilba-plat4/bin/docker \"$@\"\n", config)
		if err = os.WriteFile(dockerCLI, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	engine := &Engine{Journal: journal, Docker: docker, DockerCLI: dockerCLI}
	root := t.TempDir()
	repo := filepath.Join(root, "team", "app.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_CONFIG_NOSYSTEM=1")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v %s", args[0], err, out)
		}
		return strings.TrimSpace(string(out))
	}
	work := filepath.Join(root, "work")
	_ = os.Mkdir(work, 0700)
	git("init", "--initial-branch=main", work)
	files := map[string]string{"compose.yaml": "# keep source comment\nservices:\n  app:\n    image: " + execution.TestImage + "\n", "Dockerfile": "FROM " + execution.TestImage + "\nCOPY input.txt /input\nRUN --mount=type=secret,id=token test -s /run/secrets/token && sha256sum /run/secrets/token > /proof\nCMD [\"sleep\",\"86400\"]\n", "input.txt": "initial build input\n", "untouched.txt": "retain this\n"}
	for name, value := range files {
		if err = os.WriteFile(filepath.Join(work, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "fixture")
	commit := git("-C", work, "rev-parse", "HEAD")
	_ = os.Mkdir(filepath.Dir(repo), 0700)
	git("clone", "--bare", work, repo)
	git("--git-dir="+repo, "config", "http.receivepack", "true")
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	var reviews atomic.Int32
	var publicMode atomic.Bool
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") {
			if r.Header.Get("Authorization") != "token writer-token" {
				w.WriteHeader(401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.Method == "GET" {
				if reviews.Load() > 0 {
					_, branch, _ := strings.Cut(r.URL.Query().Get("head"), ":")
					_ = json.NewEncoder(w).Encode([]map[string]any{{"number": 1, "html_url": "http://127.0.0.1/review/1", "head": map[string]string{"ref": branch}, "base": map[string]string{"ref": "main"}}})
				} else {
					_, _ = w.Write([]byte("[]"))
				}
				return
			}
			reviews.Add(1)
			// Simulate a provider accepting the review before losing its response.
			w.WriteHeader(503)
			return
		}
		user, password, ok := r.BasicAuth()
		if !publicMode.Load() && (!ok || user != "fixture" || password != "reader-token" && password != "writer-token") {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		backend.ServeHTTP(w, r)
	}))
	defer provider.Close()
	source := pipeline.Source{ConnectionID: uuid.New(), Branch: "main", RunnerID: serverID, Paths: []string{"compose.yaml"}, Entrypoints: []string{"compose.yaml"}}
	base := pipeline.Command{Action: "import", Connection: pipeline.Connection{ID: source.ConnectionID, Provider: "gitea", BaseURL: provider.URL, Repository: "team/app", RepositoryID: 12, Private: true}, Commit: commit, Branch: "main", RevisionID: uuid.New(), UnitID: uuid.New(), Source: &source, SubmittedAt: time.Now().UTC().Truncate(time.Second), Access: &pipeline.Access{Clone: pipeline.Credential{Username: "fixture", Password: "reader-token"}}}
	var active api.ExecutionCommand
	var registryCA []byte
	cancelNext := false
	engine.Point = func(point string) {
		if point == "build" {
			var p pipeline.Command
			_ = json.Unmarshal(*active.Pipeline, &p)
			if p.Build.Budget != nil {
				v, err := docker.ContainerInspect(ctx, pipelineName(active), client.ContainerInspectOptions{})
				if err != nil || v.Container.HostConfig.Memory != 1536<<20 || v.Container.HostConfig.NanoCPUs != 1_000_000_000 || v.Container.HostConfig.Tmpfs["/workspace"] != "rw,exec,nosuid,nodev,size=128m,mode=0700" || v.Container.HostConfig.Tmpfs["/var/lib/buildkit"] != "rw,exec,nosuid,size=640m" {
					t.Fatal("custom build limits not applied", err)
				}
				if p.CacheScope != "" && v.Container.HostConfig.Tmpfs[cacheContainerDir] != "rw,nosuid,nodev,noexec,size=256m,mode=0700" {
					t.Fatal("cache exchange has no hard ceiling")
				}
			}
		}
		if point == "build" && cancelNext {
			active.CancelRequested = true
			var frozen pipeline.Command
			if json.Unmarshal(*active.Pipeline, &frozen) != nil {
				t.Fatal("invalid native pipeline fixture")
			}
			frozen.Access, frozen.BuildSecrets, frozen.RegistryAccess = nil, nil, nil
			raw, _ := json.Marshal(frozen)
			message := json.RawMessage(raw)
			active.Pipeline = &message
			if _, err := journal.Accept(active); err != nil {
				t.Fatal(err)
			}
		}
		if point == "source" && len(registryCA) > 0 {
			appendNativeCA(t, engine, pipelineName(active), registryCA)
			if _, err := docker.ContainerRestart(ctx, pipelineName(active), client.ContainerRestartOptions{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	generation := int64(0)
	run := func(p pipeline.Command) Record {
		t.Helper()
		generation++
		raw, _ := json.Marshal(p)
		message := json.RawMessage(raw)
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: serverID, EnvironmentId: uuid.New(), ResourceId: p.UnitID, Generation: generation, Kind: "pipeline", Action: "inspect", PlanHash: p.Digest(), Pipeline: &message, Deadline: time.Now().Add(2 * time.Minute)}
		active = c
		if err = engine.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		record, err := journal.Record(c.Id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = docker.ContainerInspect(ctx, pipelineName(c), client.ContainerInspectOptions{}); err == nil {
			t.Fatal("runner container was not cleaned")
		}
		if err = journal.Acknowledge(c.Id, record.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return record
	}
	imported := run(base)
	if imported.Report.State != "succeeded" {
		t.Fatal("import", imported.Report.Note)
	}
	var result pipeline.Result
	_ = json.Unmarshal(*imported.Report.Pipeline, &result)
	if len(result.Files) != 1 || result.Files[0].Content != files["compose.yaml"] {
		t.Fatal("import changed source")
	}
	submit := base
	submit.Action = "submit"
	submit.SubmissionID = uuid.New()
	submit.Access = &pipeline.Access{API: pipeline.Credential{Username: "fixture", Password: "writer-token"}}
	submit.Bundle = &composeproject.Bundle{Namespace: base.UnitID, Entrypoints: source.Entrypoints, Files: []composeproject.File{{Path: "compose.yaml", Content: strings.ReplaceAll(files["compose.yaml"], "  app:", "  renamed:")}}}
	failedSubmission := run(submit)
	if failedSubmission.Report.State != "failed" {
		t.Fatal("lost provider response did not fail the attempt")
	}
	submitted := run(submit)
	if submitted.Report.State != "succeeded" {
		t.Fatal("submission", submitted.Report.Note)
	}
	_ = json.Unmarshal(*submitted.Report.Pipeline, &result)
	if result.ReviewNumber != 1 || reviews.Load() != 1 {
		t.Fatal("review missing")
	}
	if got := git("--git-dir="+repo, "show", result.Commit+":untouched.txt"); got != "retain this" {
		t.Fatal("submission changed unselected file")
	}
	build := base
	build.Action = "build"
	build.Source = nil
	build.AppID = uuid.New()
	build.InputHash = strings.Repeat("a", 64)
	build.Build = &pipeline.BuildConfig{ConnectionID: source.ConnectionID, Branch: "main", RunnerID: serverID, Context: ".", Dockerfile: "Dockerfile", Architecture: runtime.GOARCH, Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{"token": uuid.New()}}
	build.Build.Budget = &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1536, SourceMiB: 128, StateMiB: 640, Minutes: 2}
	build.BuildSecrets = map[string]string{"token": "build-secret-canary"}
	build.CacheScope = pipeline.Hash([]uuid.UUID{uuid.New(), uuid.New()})
	coldAt := time.Now()
	built := run(build)
	coldTime := time.Since(coldAt)
	if built.Report.State != "succeeded" {
		t.Fatal("build", built.Report.Note)
	}
	_ = json.Unmarshal(*built.Report.Pipeline, &result)
	if !strings.HasPrefix(result.Digest, "sha256:") || result.Architecture != runtime.GOARCH || strings.Contains(result.Logs, "build-secret-canary") {
		t.Fatal("build identity or secret redaction failed")
	}
	inspected, err := docker.ImageInspect(ctx, result.Image)
	if err != nil || inspected.ID != result.Digest {
		t.Fatal("built image absent", err)
	}
	defer docker.ImageRemove(ctx, result.Image, client.ImageRemoveOptions{Force: true})
	cacheRun := func(command pipeline.Command, cached bool) pipeline.Result {
		t.Helper()
		r := run(command)
		var value pipeline.Result
		if r.Report.Pipeline != nil {
			_ = json.Unmarshal(*r.Report.Pipeline, &value)
		}
		if r.Report.State != "succeeded" {
			t.Fatal("cache build", r.Report.Note, value.Cache, value.Logs)
		}
		t.Cleanup(func() { _, _ = docker.ImageRemove(ctx, value.Image, client.ImageRemoveOptions{Force: true}) })
		found := false
		for _, line := range strings.Split(value.Logs, "\n") {
			if strings.Contains(line, "RUN --mount=type=secret") {
				fields := strings.Fields(line)
				found = strings.Contains(value.Logs, fields[0]+" CACHED")
			}
		}
		if found != cached || !strings.Contains(value.Cache, "Layer cache retained") {
			t.Fatal("unexpected real BuildKit layer reuse", cached, value.Cache, value.Logs)
		}
		return value
	}
	warmAt := time.Now()
	warm := cacheRun(build, true)
	t.Logf("bounded layer cache cold=%s warm=%s; timings are fixture observations, not performance guarantees", coldTime, time.Since(warmAt))
	if !strings.HasPrefix(warm.Cache, "Compatible project layer cache imported;") {
		t.Fatal("warm build did not import project cache")
	}
	other := build
	other.CacheScope = pipeline.Hash([]uuid.UUID{uuid.New(), uuid.New()})
	isolated := cacheRun(other, false)
	if strings.HasPrefix(isolated.Cache, "Compatible project layer cache imported;") {
		t.Fatal("another project imported cache")
	}
	secretVersion := build
	secretConfig := *build.Build
	secretConfig.Secrets = map[string]uuid.UUID{"token": uuid.New()}
	secretVersion.Build = &secretConfig
	secretVersion.BuildSecrets = map[string]string{"token": "rotated-build-canary"}
	cacheRun(secretVersion, false)
	if err = os.WriteFile(filepath.Join(work, "input.txt"), []byte("changed source input\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "change build input")
	git("-C", work, "push", repo, "main")
	cacheRun(build, true) // The assigned old commit stays exact after the branch moves.
	changed := build
	changed.Commit = git("-C", work, "rev-parse", "HEAD")
	changedResult := cacheRun(changed, false)
	if changedResult.Commit != changed.Commit {
		t.Fatal("cache changed assigned source identity")
	}
	raw, _ := json.Marshal(inspected)
	if strings.Contains(string(raw), "build-secret-canary") || strings.Contains(string(raw), "reader-token") {
		t.Fatal("credential leaked into image config")
	}
	publicMode.Store(true)
	publicBuild := build
	publicBuild.Connection.Private = false
	publicBuild.Access = nil
	publicRecord := run(publicBuild)
	publicMode.Store(false)
	if publicRecord.Report.State != "succeeded" {
		t.Fatal("public exact-commit build", publicRecord.Report.Note)
	}
	var publicResult pipeline.Result
	_ = json.Unmarshal(*publicRecord.Report.Pipeline, &publicResult)
	if publicResult.Commit != commit {
		t.Fatal("public build used a different commit")
	}
	defer docker.ImageRemove(ctx, publicResult.Image, client.ImageRemoveOptions{Force: true})
	registry := startNativeRegistry(t)
	registryCA = registry.ca
	remote := build
	remote.Registry = &pipeline.Registry{ID: uuid.New(), Name: "Fixture", Repository: registry.repository}
	remote.RegistryAccess = &pipeline.RegistryAccess{Push: pipeline.Credential{Username: "builder", Password: registry.push}}
	remoteResult := run(remote)
	if remoteResult.Report.State != "succeeded" {
		t.Log(string(*remoteResult.Report.Pipeline))
		t.Fatal("registry build", remoteResult.Report.Note)
	}
	var published pipeline.Result
	_ = json.Unmarshal(*remoteResult.Report.Pipeline, &published)
	if published.Image != registry.repository+"@"+published.Digest {
		t.Fatal("registry build omitted immutable identity")
	}
	auth := deployment.Payload{RegistryAccess: map[string]pipeline.Credential{"app": {Username: "deployer", Password: registry.pull}}}
	if err = engine.pullImageAuth(ctx, published.Image, visualRegistryAuth(auth, "app")); err != nil {
		t.Fatal("deployment pull by digest", err)
	}
	pulled, err := docker.ImageInspect(ctx, published.Image)
	if err != nil || pulled.Architecture != runtime.GOARCH {
		t.Fatal("registry image architecture", err)
	}
	defer docker.ImageRemove(ctx, published.Image, client.ImageRemoveOptions{Force: true})
	denied := remote
	denied.RegistryAccess = &pipeline.RegistryAccess{Push: pipeline.Credential{Username: "deployer", Password: registry.pull}}
	rejected := run(denied)
	if rejected.Report.State != "failed" {
		t.Fatal("read-only registry credential could publish")
	}
	registryCA = nil
	saved, err := docker.ImageSave(ctx, []string{result.Image})
	if err != nil {
		t.Fatal(err)
	}
	archive := tar.NewReader(saved)
	for {
		_, err := archive.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(io.LimitReader(archive, 32<<20))
		if err != nil {
			t.Fatal(err)
		}
		if bytes.HasPrefix(data, []byte{0x1f, 0x8b}) {
			gz, err := gzip.NewReader(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			data, err = io.ReadAll(io.LimitReader(gz, 32<<20))
			gz.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
		for _, secret := range []string{"build-secret-canary", "reader-token", "writer-token"} {
			if bytes.Contains(data, []byte(secret)) {
				t.Fatal("secret entered image layer", secret)
			}
		}
	}
	saved.Close()
	// Compare frozen commits rather than webhook path summaries or the moving branch.
	if err = os.WriteFile(filepath.Join(work, "README.md"), []byte("docs only\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "documentation")
	git("-C", work, "push", repo, "main")
	watched := build
	watched.Build = new(pipeline.BuildConfig)
	*watched.Build = *build.Build
	watched.Build.WatchPaths = []string{"Dockerfile", "untouched.txt"}
	watched.CompareCommit = commit
	watched.Commit = git("-C", work, "rev-parse", "HEAD")
	skipped := run(watched)
	var skippedResult pipeline.Result
	_ = json.Unmarshal(*skipped.Report.Pipeline, &skippedResult)
	if skipped.Report.State != "succeeded" || !skippedResult.Skipped || skippedResult.Image != "" {
		t.Fatal("unrelated source was built", skipped.Report.Note)
	}
	git("-C", work, "mv", "untouched.txt", "renamed.txt")
	git("-C", work, "commit", "-m", "rename out of watched path")
	git("-C", work, "push", repo, "main")
	watched.Commit = git("-C", work, "rev-parse", "HEAD")
	renamed := run(watched)
	var renamedResult pipeline.Result
	_ = json.Unmarshal(*renamed.Report.Pipeline, &renamedResult)
	if renamed.Report.State != "succeeded" || renamedResult.Skipped || renamedResult.Image == "" {
		t.Fatal("watched deletion was skipped", renamed.Report.Note)
	}
	defer docker.ImageRemove(ctx, renamedResult.Image, client.ImageRemoveOptions{Force: true})
	watched.CompareCommit = strings.Repeat("f", 40)
	unavailable := run(watched)
	var unavailableResult pipeline.Result
	_ = json.Unmarshal(*unavailable.Report.Pipeline, &unavailableResult)
	if unavailable.Report.State != "succeeded" || unavailableResult.Skipped {
		t.Fatal("unavailable baseline did not rebuild", unavailable.Report.Note)
	}
	defer docker.ImageRemove(ctx, unavailableResult.Image, client.ImageRemoveOptions{Force: true})
	// A failing build cannot replace or mutate the earlier immutable local image.
	cachePath := filepath.Join(journal.dir, "build-cache-v1", build.CacheScope+"."+build.CacheKey()+".tar")
	cacheBefore, err := os.ReadFile(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	git("-C", work, "config", "user.name", "Fixture")
	if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM "+execution.TestImage+"\nRUN exit 19\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "failure")
	git("-C", work, "push", repo, "main")
	build.Commit = git("-C", work, "rev-parse", "HEAD")
	failed := run(build)
	if failed.Report.State != "failed" {
		t.Fatal("failing build accepted", failed.Report.State)
	}
	current, err := docker.ImageInspect(ctx, result.Image)
	if err != nil || current.ID != result.Digest {
		t.Fatal("failed build changed previous artifact")
	}
	// Exhaust only the configured builder tmpfs, never host disk or database storage.
	if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM "+execution.TestImage+"\nRUN dd if=/dev/zero of=/oversized bs=1M count=1100\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "bounded storage exhaustion")
	git("-C", work, "push", repo, "main")
	build.Commit = git("-C", work, "rev-parse", "HEAD")
	exhausted := run(build)
	if exhausted.Report.State != "failed" {
		t.Fatal("unbounded build storage", exhausted.Report.State)
	}
	if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte("FROM "+execution.TestImage+"\nRUN sleep 30\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", ".")
	git("-C", work, "commit", "-m", "cancellation")
	git("-C", work, "push", repo, "main")
	build.Commit = git("-C", work, "rev-parse", "HEAD")
	cancelNext = true
	cancelled := run(build)
	if cancelled.Report.State != "cancelled" {
		t.Fatal("build cancellation state", cancelled.Report.State)
	}
	current, err = docker.ImageInspect(ctx, result.Image)
	if err != nil || current.ID != result.Digest {
		t.Fatal("resource failure changed previous artifact")
	}
	cacheAfter, err := os.ReadFile(cachePath)
	if err != nil || !bytes.Equal(cacheBefore, cacheAfter) {
		t.Fatal("failed or cancelled build replaced the retained layer cache", err)
	}

}
