package agent

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

func TestNativePreviewEnvironment(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires owned disposable native Ubuntu host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Ubuntu")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if _, err = docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("fixture requires absent ingress")
	}
	server := uuid.New()
	state := t.TempDir()
	if err = os.Chmod(state, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if j != nil {
			j.Close()
		}
	}()
	if err = j.SetConfig(Config{ServerID: server, MachineID: uuid.New(), Role: "combined", PreviewOnly: true}); err != nil {
		t.Fatal(err)
	}
	cli := "docker"
	if _, err = os.Stat("/opt/astilba-plat4/bin/docker"); err == nil {
		dir := filepath.Join(state, "docker-config")
		if err = os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"cliPluginsExtraDirs":["/opt/astilba-plat4/cli-plugins"]}`), 0600); err != nil {
			t.Fatal(err)
		}
		cli = filepath.Join(state, "docker")
		script := "#!/bin/sh\nexport DOCKER_CONFIG=" + dir + "\nexec /opt/astilba-plat4/bin/docker \"$@\"\n"
		if err = os.WriteFile(cli, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	e := &Engine{Journal: j, Docker: docker, DockerCLI: cli}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		filter := client.Filters{}.Add("label", labelServer+"="+server.String())
		if list, x := docker.ContainerList(cleanup, client.ContainerListOptions{All: true, Filters: filter}); x == nil {
			for _, v := range list.Items {
				if v.Labels[labelServer] == server.String() {
					_, _ = docker.ContainerRemove(cleanup, v.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: false})
				}
			}
		}
		if list, x := docker.VolumeList(cleanup, client.VolumeListOptions{Filters: filter}); x == nil {
			for _, v := range list.Items {
				if v.Labels[labelServer] == server.String() {
					_, _ = docker.VolumeRemove(cleanup, v.Name, client.VolumeRemoveOptions{})
				}
			}
		}
		if list, x := docker.NetworkList(cleanup, client.NetworkListOptions{Filters: filter}); x == nil {
			for _, v := range list.Items {
				if v.Labels[labelServer] == server.String() {
					_, _ = docker.NetworkRemove(cleanup, v.ID, client.NetworkRemoveOptions{})
				}
			}
		}
	})
	// Fetch the base repository's PR ref, rather than trusting a source-fork URL.
	root := t.TempDir()
	work := filepath.Join(root, "work")
	bare := filepath.Join(root, "team", "app.git")
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("fixture git failed: %v %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "--initial-branch=main", work)
	dockerfile := "FROM " + execution.TestImage + "\nRUN mkdir -p /www && printf 'preview\\n' >/www/index.html\nEXPOSE 8080\nCMD [\"httpd\",\"-f\",\"-p\",\"8080\",\"-h\",\"/www\"]\n"
	if err = os.WriteFile(filepath.Join(work, "Dockerfile"), []byte(dockerfile), 0600); err != nil {
		t.Fatal(err)
	}
	git("-C", work, "add", "Dockerfile")
	git("-C", work, "-c", "user.name=Fixture", "-c", "user.email=fixture@users.noreply.github.com", "commit", "-m", "Preview fixture")
	sha := git("-C", work, "rev-parse", "HEAD")
	if err = os.MkdirAll(filepath.Dir(bare), 0700); err != nil {
		t.Fatal(err)
	}
	git("clone", "--bare", work, bare)
	git("--git-dir="+bare, "update-ref", "refs/pull/7/head", sha)
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(&cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}})
	defer provider.Close()
	env, unit, app, pg, volume, request := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
	cfg := pipeline.BuildConfig{Builder: "dockerfile", ConnectionID: uuid.New(), Branch: "main", RunnerID: server, Context: ".", Dockerfile: "Dockerfile", Architecture: runtime.GOARCH, Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}, Budget: &pipeline.BuildBudget{CPUs: 1, MemoryMiB: 1024, SourceMiB: 128, StateMiB: 512, Minutes: 5}}
	build := pipeline.Command{Action: "build-preview", Preview: &pipeline.PreviewSource{RequestID: request, Generation: 1, Number: 7, HeadRepository: 12}, Connection: pipeline.Connection{ID: cfg.ConnectionID, Provider: "gitea", BaseURL: provider.URL, Repository: "team/app", RepositoryID: 12}, Commit: sha, Branch: "main", RevisionID: uuid.New(), UnitID: unit, AppID: app, Build: &cfg, InputHash: strings.Repeat("a", 64), SubmittedAt: time.Now().UTC()}
	raw, _ := json.Marshal(build)
	msg := json.RawMessage(raw)
	command := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: unit, Generation: 1, Kind: "pipeline", Action: "inspect", PlanHash: build.Digest(), Pipeline: &msg, Deadline: time.Now().Add(5 * time.Minute)}
	if err = e.Execute(ctx, command); err != nil {
		t.Fatal(err)
	}
	record, err := j.Record(command.Id)
	if err != nil || record.Report.State != "succeeded" || record.Report.Pipeline == nil {
		t.Fatalf("preview build failed: %+v %v", record.Report, err)
	}
	var result pipeline.Result
	if err = json.Unmarshal(*record.Report.Pipeline, &result); err != nil {
		t.Fatal(err)
	}
	if err = j.Acknowledge(command.Id, record.Report.Sequence); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		v, err := docker.ImageInspect(context.Background(), result.Image)
		if err == nil && v.ID == result.Digest {
			_, _ = docker.ImageRemove(context.Background(), result.Image, client.ImageRemoveOptions{})
		}
	})
	password, _ := cryptobox.Token()
	secret := uuid.New()
	pgImage := domain.PostgresImage
	built := result.Image
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: app, Name: "Web", Source: domain.ApplicationSource{Kind: "image", Image: &built, ArtifactID: &command.Id}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}, {ID: pg, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &pgImage}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: domain.PostgresMount(pgImage)}}}}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}, Bindings: []domain.Binding{{ID: uuid.New(), SourceID: pg, TargetID: app, Variable: "DATABASE_URL"}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: app, Protocol: "http", Domain: "preview.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{"127.0.0.1/32"}}}}
	limits := domain.PreviewLimits{CPUs: 1, MemoryMiB: 512, Pids: 64}
	plan := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: env, UnitID: unit, RevisionID: uuid.New(), Recipe: deployment.PreviewRecipe, Preview: &domain.PreviewIdentity{RequestID: request, Generation: 1, Limits: limits}, Specification: spec, Images: map[string]string{app.String(): result.Digest, pg.String(): pgImage}, Artifacts: map[string]pipeline.Artifact{app.String(): {ID: command.Id, BuildID: command.Id, Image: result.Image, Digest: result.Digest, Architecture: runtime.GOARCH, ServerID: &server}}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}
	generation := int64(1)
	execute := func(p deployment.Plan, action string, secrets map[string]string) api.ExecutionCommand {
		t.Helper()
		generation++
		payload := deployment.Payload{Plan: p, Action: action, Secrets: secrets}
		raw, _ := json.Marshal(payload)
		msg := json.RawMessage(raw)
		cfg, _ := j.Config()
		c := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: p.EnvironmentID, ResourceId: p.UnitID, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Visual: &msg, Deadline: time.Now().Add(3 * time.Minute)}
		if cfg.ControllerGeneration > 0 {
			c.ControllerGeneration = &cfg.ControllerGeneration
		}
		if err = e.Execute(ctx, c); err != nil {
			t.Fatal(err)
		}
		r, err := j.Record(c.Id)
		if err != nil || r.Report.State != "succeeded" {
			t.Fatalf("%s: %+v %v", action, r.Report, err)
		}
		if err = j.Acknowledge(c.Id, r.Report.Sequence); err != nil {
			t.Fatal(err)
		}
		return c
	}
	deploy := func(p deployment.Plan, secrets map[string]string) deployment.Plan {
		t.Helper()
		p = p.WithHash()
		before, _, err := e.observeUnit(ctx, p, secrets)
		if err != nil {
			t.Fatal(err)
		}
		p.ExpectedState = before.StateHash
		p = p.WithHash()
		execute(p, "deploy", secrets)
		return p
	}
	// A second preview and an unrelated volume/container must survive A's cleanup.
	keepApp := uuid.New()
	busy := execution.TestImage
	keepEnv, keepUnit := uuid.New(), uuid.New()
	keep := deployment.Plan{ID: uuid.New(), ServerID: server, EnvironmentID: keepEnv, UnitID: keepUnit, RevisionID: uuid.New(), Recipe: deployment.PreviewRecipe, Preview: &domain.PreviewIdentity{RequestID: uuid.New(), Generation: 1, Limits: limits}, Specification: domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{{ID: keepApp, Name: "Keep", Source: domain.ApplicationSource{Kind: "image", Image: &busy}, Command: []string{"sh", "-c", "mkdir -p /www; echo keep >/www/index.html; httpd -f -p 8081 -h /www"}, Health: &domain.HealthCheck{Port: 8081, Path: "/"}}}, Routes: []domain.Route{{ID: uuid.New(), TargetID: keepApp, Protocol: "http", Domain: "keep.example.test", Port: 8081, TLS: "off", AllowCIDRs: []string{"127.0.0.1/32"}}}}, Images: map[string]string{keepApp.String(): busy}, HostChanges: true, ExpiresAt: time.Now().Add(time.Hour)}
	keep = deploy(keep, nil)
	plan = deploy(plan, map[string]string{secret.String(): password})
	for _, id := range []uuid.UUID{app, pg} {
		v, err := docker.ContainerInspect(ctx, plan.ContainerName(id), client.ContainerInspectOptions{})
		if err != nil {
			t.Fatal(err)
		}
		r := v.Container.HostConfig.Resources
		if r.Memory != 512<<20 || r.NanoCPUs != 1_000_000_000 || r.PidsLimit == nil || *r.PidsLimit != 64 {
			t.Fatal("preview runtime limits were not enforced")
		}
	}
	if _, err = e.pipelineCLI(ctx, plan.ContainerName(pg), []string{"PGPASSWORD=" + password}, []byte("CREATE TABLE preview_data(value text); INSERT INTO preview_data VALUES('owned preview data');"), 2048, "psql", "-U", "app", "-d", "app", "-v", "ON_ERROR_STOP=1"); err != nil {
		t.Fatal("cannot initialize preview data", err)
	}
	proxy, err := docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	keepContainer, err := docker.ContainerInspect(ctx, keep.ContainerName(keepApp), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sentinel := "plat50-sentinel-" + uuid.NewString()
	nonce := uuid.NewString()
	v, err := docker.VolumeCreate(ctx, client.VolumeCreateOptions{Name: sentinel, Labels: map[string]string{"plat50.owner": nonce}})
	if err != nil {
		t.Fatal(err)
	}
	made, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: sentinel, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sleep", "3600"}, Labels: map[string]string{"plat50.owner": nonce}}, HostConfig: &container.HostConfig{Mounts: []mount.Mount{{Type: mount.TypeVolume, Source: v.Volume.Name, Target: "/data"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = docker.ContainerStart(ctx, made.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if v, e := docker.ContainerInspect(context.Background(), made.ID, client.ContainerInspectOptions{}); e == nil && v.Container.Config.Labels["plat50.owner"] == nonce {
			_, _ = docker.ContainerRemove(context.Background(), made.ID, client.ContainerRemoveOptions{Force: true})
		}
		if v, e := docker.VolumeInspect(context.Background(), sentinel, client.VolumeInspectOptions{}); e == nil && v.Volume.Labels["plat50.owner"] == nonce {
			_, _ = docker.VolumeRemove(context.Background(), sentinel, client.VolumeRemoveOptions{})
		}
	})
	generation++
	payload := deployment.Payload{Action: "preview-cleanup", Plan: plan}
	raw, _ = json.Marshal(payload)
	msg = json.RawMessage(raw)
	cleanup := api.ExecutionCommand{Id: uuid.New(), ServerId: server, EnvironmentId: env, ResourceId: unit, Generation: generation, Kind: "visual", Action: "inspect", PlanHash: payload.Digest(), Visual: &msg, Deadline: time.Now().Add(3 * time.Minute)}
	interrupted := false
	e.Point = func(point string) {
		if point == "preview-volume-removed" && !interrupted {
			interrupted = true
			panic("simulated preview cleanup interruption")
		}
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Error("cleanup interruption did not occur")
			}
		}()
		_ = e.Execute(ctx, cleanup)
	}()
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	j, err = Open(state)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal = j
	e.Point = nil
	if err = e.Execute(ctx, cleanup); err != nil {
		t.Fatal(err)
	}
	record, err = j.Record(cleanup.Id)
	if err != nil || record.Report.State != "succeeded" {
		t.Fatalf("cleanup resume: %+v %v", record.Report, err)
	}
	if _, err = docker.VolumeInspect(ctx, deployment.VolumeName(volume), client.VolumeInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("preview data volume survived cleanup", err)
	}
	if _, err = docker.NetworkInspect(ctx, deployment.NetworkName(unit), client.NetworkInspectOptions{}); !errdefs.IsNotFound(err) {
		t.Fatal("preview network survived cleanup", err)
	}
	after, err := docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil || after.Container.ID != proxy.Container.ID {
		t.Fatal("cleanup restarted shared ingress", err)
	}
	after, err = docker.ContainerInspect(ctx, keep.ContainerName(keepApp), client.ContainerInspectOptions{})
	if err != nil || after.Container.ID != keepContainer.Container.ID {
		t.Fatal("cleanup changed another preview", err)
	}
	if _, err = docker.ContainerInspect(ctx, made.ID, client.ContainerInspectOptions{}); err != nil {
		t.Fatal("cleanup removed unrelated workload", err)
	}
	inventory, err := j.Inventory()
	if err != nil || len(inventory.Units) != 1 || inventory.PreviewCleanups == nil || len(*inventory.PreviewCleanups) != 1 {
		t.Fatal("cleanup inventory/proof mismatch", inventory, err)
	}
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := cryptobox.Token()
	if err = j.Transfer("https://destination.example.test", key, 1, token); err != nil {
		t.Fatal(err)
	}
	inventory, err = j.Inventory()
	if err != nil || inventory.PreviewCleanups == nil || (*inventory.PreviewCleanups)[0].OperationId != cleanup.Id {
		t.Fatal("transfer lost cleanup proof", err)
	}
	execute(keep, "preview-cleanup", nil)
}
