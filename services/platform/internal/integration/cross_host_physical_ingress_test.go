package integration

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// assertPhysicalRemoteHTTP waits for the ingress host's proxy to serve path from the remote
// container over managed networking. The proxy probes a replica pool every 5 seconds and stops
// sending to a server after one failed probe, so it can answer 503 for a few seconds after a
// deployment and its network report ready. Each kind of answer that is not the container's is
// logged once, with the proxy's view of its servers; an answer that never comes fails with the
// last one.
func assertPhysicalRemoteHTTP(t *testing.T, ctx context.Context, host *recoveryFixtureHost, path, containerID string) {
	t.Helper()
	started := time.Now()
	seen := map[string]bool{}
	var last string
	for deadline := started.Add(30 * time.Second); ; {
		status, body, err := physicalIngressGet(ctx, host.namespace, path)
		if err == nil && status/100 == 2 && body == containerID[:12] {
			if len(seen) > 0 {
				t.Logf("ingress served %s after %s", containerID[:12], time.Since(started).Round(time.Millisecond))
			}
			return
		}
		kind := fmt.Sprint(status)
		last = fmt.Sprintf("%d %q", status, body)
		if err != nil {
			kind, last = "error", err.Error()
		}
		if status/100 != 2 && !seen[kind] {
			seen[kind] = true
			t.Logf("ingress answered %s after %s; proxy services: %s", last, time.Since(started).Round(time.Millisecond), physicalProxyServices(ctx, host))
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			t.Fatalf("ingress never reached the remote container %s over managed networking; last answer %s", containerID[:12], last)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// physicalIngressGet requests path from the proxy in namespace, as a client of the application
// would. A connection failure is an error; any status is an answer.
func physicalIngressGet(ctx context.Context, namespace, path string) (int, string, error) {
	var stdout, stderr bytes.Buffer
	command := exec.CommandContext(ctx, "nsenter", "--net=/run/netns/"+namespace, "curl", "--silent", "--show-error", "--max-time", "3", "-H", "Host: physical-recovery.example.test", "--write-out", "\n%{http_code}", "http://127.0.0.1"+path)
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		return 0, "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	out := stdout.String()
	i := strings.LastIndex(out, "\n")
	status, err := strconv.Atoi(out[i+1:])
	if i < 0 || err != nil {
		return 0, "", fmt.Errorf("curl wrote no status: %q", out)
	}
	return status, strings.TrimSpace(out[:i]), nil
}

// physicalProxyServices is the proxy's report of its HTTP services and each server's status.
func physicalProxyServices(ctx context.Context, host *recoveryFixtureHost) string {
	out, err := exec.CommandContext(ctx, "/opt/astilba-plat4/bin/docker", "--host", host.engine.Docker.DaemonHost(), "exec", "astilba-ingress", "wget", "-T", "2", "-qO-", "http://127.0.0.1:8080/api/http/services").CombinedOutput()
	if err != nil {
		return fmt.Sprintf("unavailable (%v: %s)", err, bytes.TrimSpace(out))
	}
	return string(bytes.TrimSpace(out))
}
