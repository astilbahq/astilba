package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// This child owns the journal so exit 77 drops the process without a deferred
// close. Its retry reads the frozen retirement from Bolt, not the parent's copy.
func TestCrossHostRetainedPoolProcess(t *testing.T) {
	if os.Getenv("ASTILBA_RETAINED_POOL_HELPER") != "1" {
		t.Skip("retained-pool process helper")
	}
	j, err := Open(os.Getenv("ASTILBA_RETAINED_POOL_STATE"))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	state, err := j.crossHost()
	if err != nil {
		t.Fatal(err)
	}
	engine := Engine{Journal: j, Docker: docker, Point: func(point string) {
		if point == "cross-host-pool-stopped" && os.Getenv("ASTILBA_RETAINED_POOL_CRASH") == "1" {
			os.Exit(77)
		}
	}}
	reply := struct{ Error string }{}
	if err := engine.retainCrossHostPool(t.Context(), state.Unit.Plan, &state); err != nil {
		reply.Error = err.Error()
	}
	if err := json.NewEncoder(os.Stdout).Encode(reply); err != nil {
		t.Fatal(err)
	}
}

func TestCrossHostRetainedPools(t *testing.T) {
	if testing.Short() {
		t.Skip("real Docker retention lifecycle")
	}
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	if host == "" {
		t.Fatal("PLAT_APP_DOCKER_HOST must explicitly select a disposable daemon")
	}
	ctx := t.Context()
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	owner, ownerLabel := uuid.NewString(), "com.astilba.retention-fixture"
	created := map[string]bool{}
	uniqueImage := ""
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		for id := range created {
			v, err := docker.ContainerInspect(cleanup, id, client.ContainerInspectOptions{})
			if errdefs.IsNotFound(err) {
				continue
			}
			if err != nil || v.Container.ID != id || v.Container.Config.Labels[ownerLabel] != owner {
				t.Errorf("retention cleanup ownership check: %s: %v", id, err)
				continue
			}
			if _, err := docker.ContainerRemove(cleanup, id, client.ContainerRemoveOptions{Force: true}); err != nil {
				t.Error(err)
			}
		}
		if uniqueImage != "" {
			image, err := docker.ImageInspect(cleanup, uniqueImage)
			if errdefs.IsNotFound(err) {
				return
			}
			if err != nil || image.Config.Labels[ownerLabel] != owner {
				t.Errorf("retention image cleanup ownership check: %v", err)
				return
			}
			if _, err := docker.ImageRemove(cleanup, uniqueImage, client.ImageRemoveOptions{}); err != nil {
				t.Error(err)
			}
		}
	})
	create := func(t *testing.T, name, image string, labels map[string]string) string {
		t.Helper()
		labels[ownerLabel] = owner
		made, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: image, Cmd: []string{"sh", "-c", "trap 'exit 0' TERM; while sleep 1; do :; done"}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
		if err != nil {
			t.Fatal(err)
		}
		created[made.ID] = true
		return made.ID
	}
	seed := create(t, "astilba-retention-seed-"+owner, execution.TestImage, map[string]string{})
	imageName := "astilba-retention-" + owner + ":fixture"
	committed, err := docker.ContainerCommit(ctx, seed, client.ContainerCommitOptions{Reference: imageName, Changes: []string{"LABEL " + ownerLabel + "=" + owner}})
	if err != nil {
		t.Fatal(err)
	}
	uniqueImage = committed.ID
	if _, err := docker.ContainerRemove(ctx, seed, client.ContainerRemoveOptions{}); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	newFixture := func(t *testing.T) (string, deployment.Plan, uuid.UUID) {
		t.Helper()
		state := t.TempDir()
		if err := os.Chmod(state, 0700); err != nil {
			t.Fatal(err)
		}
		base := deployment.Plan{ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour)}
		return state, base, uuid.New()
	}
	plan := func(t *testing.T, base deployment.Plan, app uuid.UUID, local bool) deployment.Plan {
		t.Helper()
		p := base
		p.ID, p.RevisionID = uuid.New(), uuid.New()
		source, resolved := execution.TestImage, execution.TestImage
		if local {
			source, resolved = imageName, uniqueImage
			p.Artifacts = map[string]pipeline.Artifact{app.String(): {ID: uuid.New(), BuildID: uuid.New(), Image: source, Digest: resolved, Architecture: runtime.GOARCH, ServerID: &p.ServerID}}
		}
		p.Specification = domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "retention", Source: domain.ApplicationSource{Kind: "image", Image: &source}}}, Variables: []domain.Variable{}}
		p.Instances = map[string]uuid.UUID{app.String(): uuid.New()}
		p.Images = map[string]string{app.String(): resolved}
		p = p.WithHash()
		if err := p.Validate(true); err != nil {
			t.Fatal(err)
		}
		return p
	}
	start := func(t *testing.T, p deployment.Plan, app uuid.UUID) string {
		t.Helper()
		id := create(t, p.ContainerName(app), p.Images[app.String()], visualLabels(p, app))
		if _, err := docker.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	stage := func(t *testing.T, path string, previous, next deployment.Plan) {
		t.Helper()
		j, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer j.Close()
		state := crossHostState{ParentID: uuid.New(), GroupHash: strings.Repeat("a", 64), Unit: deployment.DistributedUnit{Plan: next, Previous: previous}, Phase: "retire"}
		if err := j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "cross-host", "active", state) }); err != nil {
			t.Fatal(err)
		}
	}
	run := func(t *testing.T, path string, crash bool) string {
		t.Helper()
		process := exec.CommandContext(ctx, executable, "-test.run=^TestCrossHostRetainedPoolProcess$")
		process.Env = append(os.Environ(), "ASTILBA_RETAINED_POOL_HELPER=1", "ASTILBA_RETAINED_POOL_STATE="+path, "ASTILBA_RETAINED_POOL_CRASH="+map[bool]string{true: "1", false: "0"}[crash])
		out, err := process.CombinedOutput()
		if crash {
			var exited *exec.ExitError
			if !errors.As(err, &exited) || exited.ExitCode() != 77 {
				t.Fatalf("retention crash did not interrupt child at stop: %s: %v", out, err)
			}
			return ""
		}
		if err != nil {
			t.Fatalf("retention child: %s: %v", out, err)
		}
		var reply struct{ Error string }
		if err := json.NewDecoder(bytes.NewReader(out)).Decode(&reply); err != nil {
			t.Fatalf("retention reply: %s: %v", out, err)
		}
		return reply.Error
	}
	assertState := func(t *testing.T, id string, running bool) {
		t.Helper()
		v, err := docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
		if err != nil || v.Container.ID != id || v.Container.State.Running != running {
			t.Fatalf("retained container state (running=%t): %s: %v", running, id, err)
		}
	}
	t.Run("crash_replay_bound_and_image_pin", func(t *testing.T) {
		path, base, app := newFixture(t)
		a, b, c, d := plan(t, base, app, true), plan(t, base, app, false), plan(t, base, app, false), plan(t, base, app, false)
		oldest, second, third := start(t, a, app), start(t, b, app), start(t, c, app)
		stage(t, path, a, b)
		run(t, path, true)
		assertState(t, oldest, false)
		if _, err := docker.ImageRemove(ctx, uniqueImage, client.ImageRemoveOptions{}); !errdefs.IsConflict(err) {
			t.Fatalf("stopped retained container did not pin its unique image: %v", err)
		}
		for range 2 {
			if err := run(t, path, false); err != "" {
				t.Fatal(err)
			}
		}
		stage(t, path, b, c)
		if err := run(t, path, false); err != "" {
			t.Fatal(err)
		}
		if _, err := docker.ContainerStart(ctx, oldest, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		stage(t, path, c, d)
		if err := run(t, path, false); !strings.Contains(err, "restarted") {
			t.Fatalf("restarted retained container was eligible for eviction: %q", err)
		}
		assertState(t, oldest, true)
		assertState(t, second, false)
		assertState(t, third, false)
		if _, err := docker.ContainerStop(ctx, oldest, client.ContainerStopOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := run(t, path, false); err != "" {
			t.Fatal(err)
		}
		if _, err := docker.ContainerInspect(ctx, oldest, client.ContainerInspectOptions{}); !errdefs.IsNotFound(err) {
			t.Fatalf("third retirement did not evict the oldest pool: %v", err)
		}
		assertState(t, second, false)
		assertState(t, third, false)
		if _, err := docker.ImageRemove(ctx, uniqueImage, client.ImageRemoveOptions{}); err != nil {
			t.Fatalf("evicted pool still pinned its otherwise unused image: %v", err)
		}
	})
	t.Run("replacement_after_crash_is_untouched", func(t *testing.T) {
		path, base, app := newFixture(t)
		a, b := plan(t, base, app, false), plan(t, base, app, false)
		old := start(t, a, app)
		stage(t, path, a, b)
		run(t, path, true)
		if _, err := docker.ContainerRemove(ctx, old, client.ContainerRemoveOptions{}); err != nil {
			t.Fatal(err)
		}
		replacement := start(t, a, app)
		if err := run(t, path, false); !strings.Contains(err, "identity changed") {
			t.Fatalf("replacement passed the frozen Docker ID guard: %q", err)
		}
		assertState(t, replacement, true)
	})
}
