package agent

import (
	"context"
	"encoding/base64"
	"encoding/json/jsontext"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// unreadyWriter runs one primary writer through the real journal and Docker daemon,
// delivering each coordinated phase as management would.
type unreadyWriter struct {
	t          *testing.T
	docker     *client.Client
	journal    *Journal
	engine     *Engine
	plan       deployment.Plan
	id         uuid.UUID
	intent     deployment.Maintenance
	parent     uuid.UUID
	root       string
	descriptor jsontext.Value
	generation int64
}

func newUnreadyWriter(t *testing.T, app domain.Application, health *container.HealthConfig) *unreadyWriter {
	t.Helper()
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	ctx := context.Background()
	docker, err := client.New(client.WithHost(os.Getenv("PLAT_APP_DOCKER_HOST")))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	j, p := testVisualJournal(t)
	cfg, err := j.Config()
	if err != nil {
		t.Fatal(err)
	}
	cfg.ControllerGeneration = 1
	if err = j.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	image := execution.TestImage
	app.ID, app.Name, app.Source = uuid.New(), "Writer", domain.ApplicationSource{Kind: "image", Image: &image}
	placed, networkID, remote := uuid.New(), uuid.New(), uuid.New()
	p.Recipe = deployment.CrossHostRecipe
	p.Images = map[string]string{app.ID.String(): image}
	p.Specification.Applications = []domain.Application{app, {ID: placed, Name: "Remote", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Health: &domain.HealthCheck{Port: 8080, Path: "/"}}}
	p.Specification.Routes = []domain.Route{{ID: uuid.New(), TargetID: placed, Protocol: "http", Domain: "maintenance.example.test", Port: 8080, TLS: "off", AllowCIDRs: []string{}}}
	p.Specification.Placement = &domain.Placement{NetworkID: networkID, IngressServerID: p.ServerID, Applications: []domain.ApplicationPlacement{{ApplicationID: placed, CPUMillis: 100, MemoryMiB: 64, Targets: []domain.PlacementTarget{{ServerID: remote, Count: 1}}}}}
	p.Local = &deployment.LocalPlacement{GroupID: uuid.New(), Primary: true, Instances: map[string][]uuid.UUID{app.ID.String(): {app.ID}}, Network: hostnetwork.Manifest{Version: 1, NetworkID: networkID, MembershipID: uuid.New(), ServerID: p.ServerID, Generation: 1, ControllerGeneration: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: base64.StdEncoding.EncodeToString([]byte(strings.Repeat("k", 32))), ListenPort: 51820}}
	p = p.WithHash()
	if err = p.Validate(true); err != nil {
		t.Fatal("invalid fixture plan: ", err)
	}
	name := deployment.NetworkName(p.UnitID)
	if _, err = docker.NetworkCreate(ctx, name, client.NetworkCreateOptions{Driver: "bridge", Labels: visualLabels(p, p.UnitID)}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker.NetworkRemove(context.Background(), name, client.NetworkRemoveOptions{}) })
	created, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: deployment.UnitName(app.ID), Config: &container.Config{Image: image, Cmd: app.Command, Labels: visualLabels(p, app.ID), Healthcheck: health}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(name), Resources: container.Resources{Memory: 128 << 20, MemorySwap: 128 << 20}}, NetworkingConfig: &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{name: {Aliases: []string{deployment.ServiceName(app.ID)}}}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Docker: docker, Journal: j}
	_, expected, err := engine.observeVisualPlan(ctx, p, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.saveUnit(unitState{Plan: p, Expected: expected}); err != nil {
		t.Fatal(err)
	}
	w := &unreadyWriter{t: t, docker: docker, journal: j, engine: engine, plan: p, id: app.ID, parent: uuid.New(), root: strings.Repeat("a", 64), generation: 1}
	w.intent = deployment.Maintenance{Kind: "backup", ResourceID: uuid.New(), RunID: uuid.New(), EffectHash: strings.Repeat("c", 64)}
	w.observeForManagement()
	envelope := []byte("opaque-frozen-maintenance-root")
	first := uuid.New()
	d := deployment.RecoveryDescriptor{Version: 1, WorkspaceID: uuid.New(), ParentID: w.parent, RootHash: w.root, GroupID: p.Local.GroupID, GroupHash: strings.Repeat("b", 64), BaseID: first, CheckpointID: first, Digest: deployment.RecoveryDigest(envelope), Bytes: len(envelope), Participants: []uuid.UUID{p.ServerID}}
	raw, _ := stablejson.Marshal(d)
	w.descriptor = jsontext.Value(raw)
	if err = j.retainCheckpoint(w.command("reserve"), d, envelope); err != nil {
		t.Fatal(err)
	}
	return w
}

// observeForManagement refreshes the state hash that management freezes into a plan,
// as an observation does.
func (w *unreadyWriter) observeForManagement() {
	w.t.Helper()
	o, _, err := w.engine.observeUnit(context.Background(), w.plan, nil)
	if err != nil {
		w.t.Fatal(err)
	}
	w.intent.ExpectedState = o.StateHash
}

func (w *unreadyWriter) command(phase string) api.ExecutionCommand {
	body := deployment.CrossHostCommand{ParentID: w.parent, GroupHash: w.root, Phase: phase, Unit: deployment.DistributedUnit{Plan: w.plan}, Maintenance: &w.intent}
	raw, _ := stablejson.Marshal(body)
	message := jsontext.Value(raw)
	w.generation++
	controller := int64(1)
	return api.ExecutionCommand{Id: uuid.New(), ParentId: &w.parent, ServerId: w.plan.ServerID, EnvironmentId: w.plan.EnvironmentID, ResourceId: w.plan.UnitID, Kind: "crosshost", Action: "inspect", Generation: w.generation, ControllerGeneration: &controller, PlanHash: body.Digest(), Deadline: time.Now().Add(time.Hour), Crosshost: &message, RecoveryCheckpoint: &w.descriptor}
}

func (w *unreadyWriter) deliver(c api.ExecutionCommand) api.AgentReport {
	w.t.Helper()
	if err := w.engine.Execute(context.Background(), c); err != nil {
		w.t.Fatal(err)
	}
	r, err := w.journal.Record(c.Id)
	if err != nil {
		w.t.Fatal(err)
	}
	return r.Report
}

// phase delivers a command and acknowledges it, requiring it to have succeeded.
func (w *unreadyWriter) phase(name string) {
	w.t.Helper()
	w.phaseEnding(name, "succeeded")
}

func (w *unreadyWriter) phaseEnding(name string, state api.ExecutionState) {
	w.t.Helper()
	c := w.command(name)
	report := w.deliver(c)
	if report.State != state {
		w.t.Fatalf("%s ended %s: %s", name, report.State, report.Note)
	}
	if err := w.journal.Acknowledge(c.Id, report.Sequence); err != nil {
		w.t.Fatal(err)
	}
}

// removeNetwork makes a stopped writer unable to start again.
func (w *unreadyWriter) removeNetwork() {
	w.t.Helper()
	if _, err := w.docker.NetworkRemove(context.Background(), deployment.NetworkName(w.plan.UnitID), client.NetworkRemoveOptions{}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *unreadyWriter) setMemory(bytes int64) {
	w.t.Helper()
	if _, err := w.docker.ContainerUpdate(context.Background(), deployment.UnitName(w.id), client.ContainerUpdateOptions{Resources: &container.Resources{Memory: bytes, MemorySwap: bytes}}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *unreadyWriter) stop() {
	w.t.Helper()
	if _, err := w.docker.ContainerStop(context.Background(), deployment.UnitName(w.id), client.ContainerStopOptions{}); err != nil {
		w.t.Fatal(err)
	}
}

func (w *unreadyWriter) running() bool {
	w.t.Helper()
	v, err := w.docker.ContainerInspect(context.Background(), deployment.UnitName(w.id), client.ContainerInspectOptions{})
	if err != nil {
		w.t.Fatal(err)
	}
	return v.Container.State.Running
}

func (w *unreadyWriter) barrier() bool {
	w.t.Helper()
	held, err := w.journal.hasCrossHostBarrier()
	if err != nil {
		w.t.Fatal(err)
	}
	return held
}

func (w *unreadyWriter) waitHealth(status container.HealthStatus) {
	w.t.Helper()
	for range 60 {
		v, err := w.docker.ContainerInspect(context.Background(), deployment.UnitName(w.id), client.ContainerInspectOptions{})
		if err != nil {
			w.t.Fatal(err)
		}
		if v.Container.State.Health != nil && v.Container.State.Health.Status == status {
			return
		}
		time.Sleep(time.Second)
	}
	w.t.Fatalf("writer never became %s", status)
}

// As process 1 a plain sleep ignores the stop signal, which costs each test ten seconds.
const sleepWriter = "trap 'exit 0' TERM; sleep 600 & wait"

// A writer already stopped when maintenance starts is not one maintenance stopped.
func TestNativeBackupOfStoppedWriter(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.stop()
	w.observeForManagement()
	w.phase("reserve")
	w.phase("pause")
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if w.running() {
		t.Fatal("maintenance started a writer that was stopped before it began")
	}
}

// Management's last state hash predates the stop, so reserve refuses and every phase after
// it still has to finish on this host for the parent to release the others.
func TestNativeRefusedReservationOfStoppedWriterCompletes(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.stop()
	w.phaseEnding("reserve", "failed")
	if w.barrier() {
		t.Fatal("a refused reservation installed a barrier")
	}
	w.phase("resume-source")
	w.phase("complete")
}

// Changed configuration is not an application's state to preserve. Refusing before any host
// stops anything lets the parent finish instead of holding other hosts' writers stopped.
func TestNativeReservationRefusesChangedConfiguration(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.setMemory(129 << 20)
	w.observeForManagement()
	w.phaseEnding("reserve", "failed")
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("a refused reservation stopped the writer")
	}
}

// A writer stopped after its host reserved has stopped nothing on maintenance's behalf.
func TestNativePauseRefusesAWriterStoppedSinceReserving(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.phase("reserve")
	w.stop()
	w.phaseEnding("pause", "failed")
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
}

// A writer already failing may not survive the restart at all. It must not hold other hosts.
func TestNativeWriterAlreadyFailingThatCannotRestart(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, &container.HealthConfig{Test: []string{"CMD", "false"}, Interval: time.Second, Retries: 1})
	w.waitHealth(container.Unhealthy)
	w.phase("reserve")
	w.phase("pause")
	w.removeNetwork()
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if w.running() {
		t.Fatal("a writer with no network started")
	}
}

// With no network to start on, accepting it is the only way to finish.
func TestNativeAcceptingAWriterThatCannotStart(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, nil)
	w.phase("reserve")
	w.phase("pause")
	w.removeNetwork()
	resume := w.command("resume-source")
	if r := w.deliver(resume); r.State != "needs_attention" {
		t.Fatalf("resuming a writer that cannot start ended %s: %s", r.State, r.Note)
	}
	if err := w.engine.AcceptUnreadyWriters(context.Background(), resume.Id); err != nil {
		t.Fatal(err)
	}
	report := w.deliver(resume)
	if report.State != "succeeded" {
		t.Fatalf("accepted resumption ended %s: %s", report.State, report.Note)
	}
	if err := w.journal.Acknowledge(resume.Id, report.Sequence); err != nil {
		t.Fatal(err)
	}
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
}

// An image's health check that was already failing before maintenance must not hold it.
func TestNativeBackupOfWriterFailingItsHealthCheck(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", sleepWriter}}, &container.HealthConfig{Test: []string{"CMD", "false"}, Interval: time.Second, Retries: 1})
	w.waitHealth(container.Unhealthy)
	w.phase("reserve")
	w.phase("pause")
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("maintenance left a writer stopped that was running before it began")
	}
}

// Plan readiness is not a Docker health check, so nothing but a probe shows it failing.
func TestNativeBackupOfWriterFailingItsReadiness(t *testing.T) {
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", "mkdir -p /www && exec httpd -f -p 8080 -h /www"}, Health: &domain.HealthCheck{Port: 8080, Path: "/ready"}}, nil)
	w.phase("reserve")
	w.phase("pause")
	w.phase("resume-source")
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("maintenance left a writer stopped that was running before it began")
	}
}

// A writer can pass its checks until maintenance restarts it. Nothing will cure that but a
// deployment, which needs the reservation released, so an operator can accept it.
func TestNativeAcceptingAWriterThatDoesNotComeBack(t *testing.T) {
	onceOnly := "mkdir -p /www; if [ -e /booted ]; then rm -f /www/ready; else touch /booted /www/ready; fi; exec httpd -f -p 8080 -h /www"
	w := newUnreadyWriter(t, domain.Application{Command: []string{"sh", "-c", onceOnly}, Health: &domain.HealthCheck{Port: 8080, Path: "/ready"}}, nil)
	ctx := context.Background()
	w.phase("reserve")
	w.phase("pause")
	resume := w.command("resume-source")
	if r := w.deliver(resume); r.State != "needs_attention" {
		t.Fatalf("resumption of a writer failing readiness ended %s: %s", r.State, r.Note)
	}
	if !w.barrier() {
		t.Fatal("a writer's unknown state released the reservation without an operator")
	}
	w.setMemory(129 << 20)
	if err := w.engine.AcceptUnreadyWriters(ctx, resume.Id); err == nil {
		t.Fatal("accepting a writer excused a change to its configuration")
	}
	w.setMemory(128 << 20)
	if err := w.engine.AcceptUnreadyWriters(ctx, resume.Id); err != nil {
		t.Fatal(err)
	}
	if !w.barrier() {
		t.Fatal("accepting a writer released the reservation instead of its command doing so")
	}
	report := w.deliver(resume)
	if report.State != "succeeded" {
		t.Fatalf("accepted resumption ended %s: %s", report.State, report.Note)
	}
	if err := w.journal.Acknowledge(resume.Id, report.Sequence); err != nil {
		t.Fatal(err)
	}
	w.phase("complete")
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
	if !w.running() {
		t.Fatal("accepting a running writer stopped it")
	}
}
