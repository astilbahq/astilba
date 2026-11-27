package agent

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/moby/moby/client"
)

// routed is the one route server w's reservation recorded.
func (w *unreadyWriter) routed() routedServer {
	w.t.Helper()
	s, err := w.journal.crossHost()
	if err != nil {
		w.t.Fatal(err)
	}
	if s.Routed == nil || len(s.Routed.Servers) != 1 {
		w.t.Fatalf("the reservation recorded %+v", s.Routed)
	}
	return s.Routed.Servers[0]
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

// writerExec runs cmd in the writer and requires it to succeed.
func (w *unreadyWriter) writerExec(cmd ...string) {
	w.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	exec, err := w.docker.ExecCreate(ctx, deployment.UnitName(w.id), client.ExecCreateOptions{Cmd: cmd})
	if err == nil {
		_, err = w.docker.ExecStart(ctx, exec.ID, client.ExecStartOptions{})
	}
	for err == nil {
		status, inspectErr := w.docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
		if err = inspectErr; err != nil || !status.Running {
			if err == nil && status.ExitCode != 0 {
				err = errors.New("it exited " + strconv.Itoa(status.ExitCode))
			}
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		w.t.Fatalf("%s failed in the writer: %v", strings.Join(cmd, " "), err)
	}
}

// Pausing takes a writer out of its pool, and the proxy takes it back only on its next
// probe, up to an interval after the writer resumes. Completion must not release the
// reservation until it has, even across an agent restart after the reservation recorded
// which servers the proxy routed.
func TestNativeCompletionWaitsForTheProxyToRouteAResumedWriter(t *testing.T) {
	w := newRoutedWriter(t)
	w.phase("reserve")
	server := w.routed()
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
// reservation, with its writers running, until the bound, and says which route and server.
// Accepting the host's writers as they are accepts its routes too, and completion finishes.
func TestNativeAcceptingRoutesTheProxyCannotRouteTo(t *testing.T) {
	w := newRoutedWriter(t)
	w.phase("reserve")
	server := w.routed()
	_, at := w.routeStatus(server)
	w.phase("pause")
	w.phase("resume-source")
	w.writerExec("rm", "/www/health")
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
