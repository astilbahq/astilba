package agent

import (
	"context"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
)

// Compiled services now say pull_policy: never. Compose leaves a policy out of a service's
// config hash, so a container an older agent started is not recreated by the next up.
func TestNativeComposePullPolicyKeepsRunningContainers(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires disposable native Linux host")
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Fatal("requires root on disposable Linux host")
	}
	ctx := context.Background()
	host := os.Getenv("PLAT_APP_DOCKER_HOST")
	docker, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	defer docker.Close()
	if version, err := composeVersion(ctx, "docker"); err != nil {
		t.Fatal("requires the pinned Compose plugin", version, err)
	}
	project := "astilba-pull-policy-" + uuid.NewString()[:8]
	compose := func(file string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(ctx, "docker", append([]string{"--host", host, "compose", "--project-name", project, "--file", "-"}, args...)...)
		cmd.Stdin = strings.NewReader(file)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("compose %v: %v %s", args, err, out)
		}
	}
	service := "services:\n  web:\n    image: " + execution.TestImage + "\n    command: [sleep, \"3600\"]\n    restart: unless-stopped\n"
	defer compose(service, "down", "--timeout", "1")
	state := func() (string, string) {
		t.Helper()
		list, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: make(client.Filters).Add("label", "com.docker.compose.project="+project)})
		if err != nil || len(list.Items) != 1 {
			t.Fatal("expected one container", err)
		}
		return list.Items[0].ID, list.Items[0].Labels["com.docker.compose.config-hash"]
	}
	compose(service, "up", "--detach")
	before, hash := state()
	compose(service+"    pull_policy: never\n", "up", "--detach")
	after, newHash := state()
	if after != before || newHash != hash {
		t.Fatalf("pull_policy recreated the container: %s %s -> %s %s", before, hash, after, newHash)
	}
}
