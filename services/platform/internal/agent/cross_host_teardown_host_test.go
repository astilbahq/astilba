package agent

import (
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// stopFixtureDaemon stops a cross-host fixture's Docker daemon, killing it after 15 seconds. A
// killed daemon leaves its containers' network namespaces mounted, so removing the fixture then
// fails on one; the log says why.
func stopFixtureDaemon(t *testing.T, daemon *exec.Cmd, hostRoot string) {
	_ = daemon.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { _ = daemon.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		t.Logf("fixture daemon for %s did not stop within 15s; killing it", hostRoot)
		_ = daemon.Process.Kill()
		<-done
	}
}

// requireNoDatabaseHelpers fails if a database helper is left on a fixture host once its agent
// has stopped. A helper shares its database's network namespace, so one left there is a leak,
// and holds that namespace open.
func requireNoDatabaseHelpers(t *testing.T, docker *client.Client, hostRoot string) {
	for _, kind := range []string{"backup-dump-v1", "upgrade-preflight-v1", "upgrade-tools-v1"} {
		left, err := docker.ContainerList(context.Background(), client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", labelKind+"="+kind)})
		if err != nil {
			t.Error(err)
			return
		}
		for _, c := range left.Items {
			t.Errorf("database helper %s outlived its work on %s", containerName(c), hostRoot)
		}
	}
}
