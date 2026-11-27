package agent

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// remoteAddress and remotePort are where a routed writer's fixture serves a pool as a
// server on another host does: at the peer's private address, on an endpoint port.
const (
	remoteAddress = "10.251.0.2"
	remotePort    = hostnetwork.FirstEndpointPort
)

// newRemoteServer serves a pool at remoteAddress as another host would, and returns the
// container and the interface that gives this host the address. The proxy reaches it
// through this host's network stack, as it reaches a peer's private address over the
// host's private link.
func newRemoteServer(t *testing.T, docker *client.Client) (name, link string) {
	t.Helper()
	link = "pr" + strings.ReplaceAll(uuid.NewString(), "-", "")[:10]
	hostIP(t, "link", "add", link, "type", "dummy")
	t.Cleanup(func() { _ = exec.Command("ip", "link", "del", link).Run() })
	hostIP(t, "addr", "add", remoteAddress+"/32", "dev", link)
	hostIP(t, "link", "set", link, "up")
	name = "astilba-test-" + link
	listen := remoteAddress + ":" + strconv.Itoa(remotePort)
	created, err := docker.ContainerCreate(t.Context(), client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: execution.TestImage, Cmd: []string{"sh", "-c", "mkdir -p /www && echo remote > /www/index.html && touch /www/health && trap 'exit 0' TERM; httpd -f -p " + listen + " -h /www & wait"}}, HostConfig: &container.HostConfig{NetworkMode: "host"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = docker.ContainerRemove(context.Background(), created.ID, client.ContainerRemoveOptions{Force: true})
	})
	if _, err = docker.ContainerStart(t.Context(), created.ID, client.ContainerStartOptions{}); err != nil {
		t.Fatal(err)
	}
	return name, link
}

func hostIP(t *testing.T, args ...string) {
	t.Helper()
	if out, err := exec.Command("ip", args...).CombinedOutput(); err != nil {
		t.Fatalf("ip %s: %v: %s", strings.Join(args, " "), err, out)
	}
}

// routed is the route server of w's writer and the one on the other host that w's
// reservation recorded.
func (w *unreadyWriter) routed() (local, remote routedServer) {
	w.t.Helper()
	s, err := w.journal.crossHost()
	if err != nil {
		w.t.Fatal(err)
	}
	if s.Routed == nil || len(s.Routed.Servers) != 2 {
		w.t.Fatalf("the reservation recorded %+v", s.Routed)
	}
	for _, server := range s.Routed.Servers {
		if server.Backend == net.JoinHostPort(remoteAddress, strconv.Itoa(remotePort)) {
			remote = server
		} else {
			local = server
		}
	}
	if local.Backend == "" || remote.Backend == "" {
		w.t.Fatalf("the reservation recorded %+v", s.Routed)
	}
	return local, remote
}

// routeStatus is what the proxy reports of server, and the URL it reports it by.
func (w *unreadyWriter) routeStatus(server routedServer) (status, at string) {
	w.t.Helper()
	runtime, err := w.engine.readIngressRuntime(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	for at, status := range runtime["services"][server.Service].ServerStatus {
		if backend, err := backendOf(w.plan.UnitID, at); err == nil && backend == server.Backend {
			return status, at
		}
	}
	return "", ""
}

// waitRouteStatus waits for the proxy to report server as status.
func (w *unreadyWriter) waitRouteStatus(server routedServer, status string) {
	w.t.Helper()
	for range 60 {
		if got, _ := w.routeStatus(server); got == status {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	w.t.Fatalf("the proxy never reported %s %s", server.Backend, status)
}

// restart opens the journal again, as an agent that restarts does.
func (w *unreadyWriter) restart() {
	w.t.Helper()
	w.journal = reopen(w.t, w.journal)
	w.engine = &Engine{Docker: w.docker, Journal: w.journal}
}

// containerExec runs cmd in the container named name and requires it to succeed.
func (w *unreadyWriter) containerExec(name string, cmd ...string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	created, err := w.docker.ExecCreate(ctx, name, client.ExecCreateOptions{Cmd: cmd})
	if err == nil {
		_, err = w.docker.ExecStart(ctx, created.ID, client.ExecStartOptions{})
	}
	for err == nil {
		status, inspectErr := w.docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		if err = inspectErr; err != nil || !status.Running {
			if err == nil && status.ExitCode != 0 {
				err = errors.New("it exited " + strconv.Itoa(status.ExitCode))
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		w.t.Fatalf("%s failed in %s: %v", strings.Join(cmd, " "), name, err)
	}
}

// Pausing takes a writer out of its pool, and the proxy takes it back only on its next
// probe, up to an interval after the writer resumes. Completion must not release the
// reservation until it has, even across an agent restart after the reservation recorded
// which servers the proxy routed.
func TestNativeCompletionWaitsForTheProxyToRouteAResumedWriter(t *testing.T) {
	w := newRoutedWriter(t)
	w.phase("reserve")
	server, _ := w.routed()
	w.phase("pause")
	w.waitRouteStatus(server, "DOWN")
	w.restart()
	w.phase("resume-source")
	w.phase("complete")
	if status, at := w.routeStatus(server); status != "UP" {
		t.Fatalf("completion released the reservation while the proxy reported %s %s", at, status)
	}
	if out, _, ok, err := w.engine.proxyExec(context.Background(), []string{"wget", "-T", "5", "-qO-", "--header", "Host: routed.example.test", "http://127.0.0.1:80/"}, 4096); err != nil || !ok || strings.TrimSpace(string(out)) != "writer" {
		t.Fatalf("the route answered %q after completion: %v", out, err)
	}
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
}

// A writer that passes its own readiness while the proxy cannot route to it keeps the
// reservation, with its writers running, until the bound, and says which route and server,
// though the proxy routes the unit's servers on other hosts.
// Accepting the host's writers as they are accepts its routes too, and completion finishes.
func TestNativeAcceptingRoutesTheProxyCannotRouteTo(t *testing.T) {
	w := newRoutedWriter(t)
	w.phase("reserve")
	server, remote := w.routed()
	_, at := w.routeStatus(server)
	w.phase("pause")
	w.phase("resume-source")
	w.containerExec(deployment.UnitName(w.id), "rm", "/www/health")
	complete := w.command("complete")
	started := time.Now()
	report := w.deliver(complete)
	if took := time.Since(started); took < routeServerWait || took > routeServerWait+20*time.Second {
		t.Fatalf("completion gave up after %s", took)
	}
	if report.State != "needs_attention" {
		t.Fatalf("completion ended %s: %s", report.State, report.Note)
	}
	for _, want := range []string{"the resumed routes are not ready for maintenance completion", "route routed.example.test", "server " + at} {
		if !strings.Contains(report.Note, want) {
			t.Fatalf("the note %q does not say %q", report.Note, want)
		}
	}
	if !w.barrier() || !w.running() {
		t.Fatal("a completion the proxy could not confirm released the reservation or stopped the writer")
	}
	if status, _ := w.routeStatus(remote); status != "UP" {
		t.Fatalf("the proxy reports the other host's server %s, so it is not this host's that held completion", status)
	}
	if err := w.engine.AcceptUnreadyWriters(context.Background(), complete.Id); err != nil {
		t.Fatal(err)
	}
	if report = w.deliver(complete); report.State != "succeeded" {
		t.Fatalf("the accepted completion ended %s: %s", report.State, report.Note)
	}
	if w.barrier() {
		t.Fatal("the reservation is still held")
	}
}

// Nothing converts a reserved host's routes until the reservation is released, so bare
// backend names an earlier agent published would last as long as it does. Reserving
// converts them first, and a host whose conversion fails is not reserved. The proxy counts
// every server up again when it loads the converted routes, until it probes it, so a
// server it had out, here the other host's, is still left out of what the reservation
// records.
func TestNativeReservationConvertsBareBackends(t *testing.T) {
	w := newRoutedWriter(t)
	ctx := context.Background()
	remote := net.JoinHostPort(remoteAddress, strconv.Itoa(remotePort))
	if _, err := w.docker.ContainerStop(ctx, w.remote, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	state, err := w.journal.routing()
	if err != nil {
		t.Fatal(err)
	}
	bare := state.Current
	bare.QualifiedBackends = false
	if err = w.engine.replaceRouteFile(ctx, bare); err != nil {
		t.Fatal(err)
	}
	state.Current = bare
	if err = w.journal.saveRouting(state); err != nil {
		t.Fatal(err)
	}
	published, err := os.ReadFile(w.engine.routePath())
	if err != nil {
		t.Fatal(err)
	}
	qualified := func() bool {
		t.Helper()
		state, err := w.journal.routing()
		if err != nil {
			t.Fatal(err)
		}
		drift, err := w.engine.ingressDrift(ctx, state)
		if err != nil || drift {
			t.Fatalf("the host's routes drifted: %v", err)
		}
		return state.Current.QualifiedBackends
	}

	remoteDown := func() bool {
		t.Helper()
		runtime, err := w.engine.readIngressRuntime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, service := range runtime["services"] {
			for at, status := range service.ServerStatus {
				if backend, err := backendOf(w.plan.UnitID, at); err == nil && backend == remote {
					return status == "DOWN"
				}
			}
		}
		return false
	}
	for range 30 {
		if remoteDown() {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !remoteDown() {
		t.Fatal("the proxy never took the other host's stopped server out")
	}

	// A conversion the proxy never activates is put back.
	broken := false
	w.engine.Point = func(point string) {
		if point == "route-published" && !broken {
			broken = true
			if err := os.WriteFile(w.engine.routePath(), []byte("{}"), 0o600); err != nil {
				t.Error(err)
			}
		}
	}
	w.phaseEnding("reserve", "failed")
	w.engine.Point = nil
	if w.barrier() || qualified() {
		t.Fatal("a host whose conversion failed was reserved, or kept the conversion")
	}
	if raw, err := os.ReadFile(w.engine.routePath()); err != nil || !bytes.Equal(raw, published) {
		t.Fatal("the failed conversion did not restore the route file", err)
	}

	w.phase("reserve")
	if !w.barrier() || !qualified() {
		t.Fatal("the host was reserved without converting its routes")
	}
	s, err := w.journal.crossHost()
	if err != nil {
		t.Fatal(err)
	}
	if s.Routed == nil || len(s.Routed.Servers) != 1 || s.Routed.Servers[0].Backend == remote {
		t.Fatalf("the reservation recorded %+v, not only this host's writer", s.Routed)
	}
}

// A proxy that cannot say which servers it routes could not confirm them at completion
// either, so the reservation is refused before anything stops.
func TestNativeReservationRefusesAProxyThatCannotReport(t *testing.T) {
	w := newRoutedWriter(t)
	if _, err := w.docker.ContainerStop(context.Background(), proxyName, client.ContainerStopOptions{}); err != nil {
		t.Fatal(err)
	}
	w.phaseEnding("reserve", "failed")
	if w.barrier() || !w.running() {
		t.Fatal("a refused reservation held the host or stopped its writer")
	}
}

// A server on another host may never be routed again for reasons only its own host knows:
// the host was revoked, its writer was stopped by hand so it refused the pause, or it
// excuses its writer from being ready. Completion on this host waits for the server until
// the bound, then releases the reservation, naming the route and server, once the proxy
// routes this host's writer.
func TestNativeCompletionReleasesWithoutAnotherHostsServer(t *testing.T) {
	for _, c := range []struct {
		name  string
		leave func(w *unreadyWriter)
	}{
		{"revoked_host", func(w *unreadyWriter) { hostIP(w.t, "link", "del", w.link) }},
		{"writer_stopped_by_hand", func(w *unreadyWriter) {
			if _, err := w.docker.ContainerStop(context.Background(), w.remote, client.ContainerStopOptions{}); err != nil {
				w.t.Fatal(err)
			}
		}},
		{"excused_writer", func(w *unreadyWriter) { w.containerExec(w.remote, "rm", "/www/health") }},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newRoutedWriter(t)
			w.phase("reserve")
			local, remote := w.routed()
			_, at := w.routeStatus(remote)
			w.phase("pause")
			c.leave(w)
			w.phase("resume-source")
			started := time.Now()
			report := w.deliver(w.command("complete"))
			if took := time.Since(started); took < routeServerWait || took > routeServerWait+20*time.Second {
				t.Fatalf("completion gave up on the other host's server after %s", took)
			}
			if report.State != "succeeded" {
				t.Fatalf("completion ended %s: %s", report.State, report.Note)
			}
			for _, want := range []string{"only their own hosts can excuse them", "route maintenance.example.test", "server " + at} {
				if !strings.Contains(report.Note, want) {
					t.Fatalf("the note %q does not say %q", report.Note, want)
				}
			}
			if status, at := w.routeStatus(local); status != "UP" {
				t.Fatalf("completion released the reservation while the proxy reported this host's %s %s", at, status)
			}
			if w.barrier() {
				t.Fatal("the reservation is still held")
			}
		})
	}
}
