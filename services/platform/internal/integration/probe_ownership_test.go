package integration

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// probeHarness runs probe commands against Docker with a fresh journal, for one server and
// environment.
type probeHarness struct {
	docker      *client.Client
	engine      *agent.Engine
	journal     *agent.Journal
	server, env uuid.UUID
	generation  int64
}

func newProbeHarness(t *testing.T) *probeHarness {
	t.Helper()
	if testing.Short() {
		t.Skip("disposable Docker")
	}
	docker, e := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = docker.Close() })
	dir := t.TempDir()
	_ = os.Chmod(dir, 0700)
	j, e := agent.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = j.Close() })
	server := uuid.New()
	_ = j.SetConfig(agent.Config{ServerID: server, Role: "deployment"})
	return &probeHarness{docker: docker, engine: &agent.Engine{Journal: j, Docker: docker}, journal: j, server: server, env: uuid.New()}
}

// command is the next probe command, each a later generation than the one before.
func (h *probeHarness) command(action api.TestAction) api.ExecutionCommand {
	h.generation++
	return api.ExecutionCommand{Id: uuid.New(), ServerId: h.server, EnvironmentId: h.env, ResourceId: h.resource(), Generation: h.generation, PlanHash: strings.Repeat("a", 64), Kind: "test", Action: action, Image: execution.TestImage, Deadline: time.Now().Add(time.Minute)}
}

func (h *probeHarness) resource() uuid.UUID {
	return uuid.NewSHA1(h.server, []byte(h.env.String()+":probe:v1"))
}

func (h *probeHarness) labels() map[string]string {
	return map[string]string{"com.astilba.server": h.server.String(), "com.astilba.resource": h.resource().String(), "com.astilba.environment": h.env.String(), "com.astilba.kind": "probe-v1"}
}

// run executes a probe command, acknowledges its report as management would, and returns its
// final state.
func (h *probeHarness) run(t *testing.T, action api.TestAction) api.ExecutionState {
	t.Helper()
	c := h.command(action)
	if e := h.engine.Execute(context.Background(), c); e != nil {
		t.Fatal(e)
	}
	r, e := h.journal.Record(c.Id)
	if e != nil {
		t.Fatal(e)
	}
	if e = h.journal.Acknowledge(c.Id, r.Report.Sequence); e != nil {
		t.Fatal(e)
	}
	t.Logf("%s: %s, %s", action, r.Report.State, r.Report.Note)
	return r.Report.State
}

// create makes a container under the probe's name, removed when the test ends unless the agent
// already removed it.
func (h *probeHarness) create(t *testing.T, image string, labels map[string]string) string {
	t.Helper()
	created, e := h.docker.ContainerCreate(context.Background(), client.ContainerCreateOptions{Name: agent.Name(api.ExecutionCommand{ResourceId: h.resource()}), Config: &container.Config{Image: image, Cmd: []string{"sleep", "86400"}, Labels: labels}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, _ = h.docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	return created.ID
}

// A container under the probe's name without all four of its labels is never the agent's.
// needs_attention blocks later work on the probe, so each command gets its own host.
func TestUnownedContainerIsNeverRemoved(t *testing.T) {
	keys := []string{"com.astilba.server", "com.astilba.resource", "com.astilba.environment", "com.astilba.kind"}
	cases := map[string]func(map[string]string){
		"no labels":    func(l map[string]string) { clear(l) },
		"another kind": func(l map[string]string) { l["com.astilba.kind"] = "visual-v1" },
	}
	for _, key := range keys {
		cases["without "+key] = func(l map[string]string) { delete(l, key) }
	}
	for name, change := range cases {
		for _, action := range []api.TestAction{"inspect", "start", "remove"} {
			t.Run(name+"/"+string(action), func(t *testing.T) {
				h := newProbeHarness(t)
				labels := h.labels()
				change(labels)
				labels["com.astilba.fixture"] = uuid.NewString()
				id := h.create(t, execution.TestImage, labels)
				if state := h.run(t, action); state != "needs_attention" {
					t.Fatalf("%s of a container that is not the probe ended %s", action, state)
				}
				if _, e := h.docker.ContainerInspect(context.Background(), id, client.ContainerInspectOptions{}); e != nil {
					t.Fatal("unowned container removed", e)
				}
			})
		}
	}
}

// A probe started before the test image moved is still the agent's to inspect, start and remove.
func TestProbeOnAnEarlierImageStaysManaged(t *testing.T) {
	h := newProbeHarness(t)
	ctx := context.Background()
	earlier := "astilba-test/earlier-probe:" + uuid.NewString()
	if _, e := h.docker.ImageTag(ctx, client.ImageTagOptions{Source: execution.TestImage, Target: earlier}); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _, _ = h.docker.ImageRemove(ctx, earlier, client.ImageRemoveOptions{}) })
	id := h.create(t, earlier, h.labels())
	if state := h.run(t, "inspect"); state != "succeeded" {
		t.Fatalf("inspect ended %s", state)
	}
	if state := h.run(t, "start"); state != "succeeded" {
		t.Fatalf("start ended %s", state)
	}
	v, e := h.docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if e != nil || !v.Container.State.Running || v.Container.Config.Image != earlier {
		t.Fatal("start did not run the existing probe", e)
	}
	if state := h.run(t, "remove"); state != "succeeded" {
		t.Fatalf("remove ended %s", state)
	}
	if _, e = h.docker.ContainerInspect(ctx, id, client.ContainerInspectOptions{}); e == nil {
		t.Fatal("probe on the earlier image was not removed")
	}
}
