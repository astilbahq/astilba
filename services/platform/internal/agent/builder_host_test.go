package agent

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/execution"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// builderEscapes tries what code that escapes a build step's sandbox would try next to take the
// host, and prints NAME=allowed or NAME=refused for each. It changes nothing: the host's disk is
// already mounted read-write, so mounting it again only shares that mount, and the kernel setting
// is written back unchanged. The kernel checks permission to load a module before it reads one,
// so any file shows which it is. DISK is the host disk's device number, which defaults to that of
// the disk holding the container's /etc/hostname. Each line is a whole command, so the script
// also runs joined into one.
const builderEscapes = `set +e
dev=${DISK:-$(awk '$5 == "/etc/hostname" {print $3; exit}' /proc/self/mountinfo)}
node=/tmp/escape-disk
if mknod "$node" b "${dev%%:*}" "${dev#*:}" 2>/dev/null && head -c 512 "$node" >/dev/null 2>&1; then echo "host-disk-device=allowed"; else echo "host-disk-device=refused"; fi
mkdir -p /tmp/escape-mount
if mount "$node" /tmp/escape-mount 2>/dev/null; then echo "host-disk-mount=allowed"; umount /tmp/escape-mount; else echo "host-disk-mount=refused"; fi
out=$(insmod /etc/passwd 2>&1)
case "$out" in *"not permitted"*|*"denied"*) echo "kernel-module=refused";; *"invalid"*|*"format"*|*"Exec format"*) echo "kernel-module=allowed";; *) echo "kernel-module=unknown: $out";; esac
if cat /proc/sys/kernel/core_pattern > /tmp/escape-core 2>/dev/null && cat /tmp/escape-core > /proc/sys/kernel/core_pattern 2>/dev/null; then echo "kernel-setting=allowed"; else echo "kernel-setting=refused"; fi
rm -f "$node"
`

// escapeResults reads builderEscapes' output.
func escapeResults(output string) map[string]string {
	got := map[string]string{}
	for _, match := range regexp.MustCompile(`(?m)(?:^|\s)(host-disk-device|host-disk-mount|kernel-module|kernel-setting)=(allowed|refused|unknown: .*)$`).FindAllStringSubmatch(output, -1) {
		got[match[1]] = strings.TrimSpace(match[2])
	}
	return got
}

func everyEscape(result string) map[string]string {
	return map[string]string{"host-disk-device": result, "host-disk-mount": result, "kernel-module": result, "kernel-setting": result}
}

// Code that escapes a build step can take the host from a privileged builder, the way builds ran
// before, and can't from the unprivileged one: not from the build container, not as root of the
// user namespace BuildKit runs build steps in, and not from a build step.
func TestNativeBuilderConfinement(t *testing.T) {
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
	dockerCLI, err := exec.LookPath("docker")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = docker.ImageInspect(ctx, pipeline.BuilderImage); err != nil {
		if err = (&Engine{Docker: docker}).pullImage(ctx, pipeline.BuilderImage); err != nil {
			t.Fatal(err)
		}
	}

	// The same escapes succeed in the builder as it ran before: privileged, as root.
	control := exec.CommandContext(ctx, dockerCLI, "run", "--rm", "--interactive", "--privileged", "--user", "0", "--entrypoint", "sh", pipeline.BuilderImage, "-s")
	control.Env = []string{"PATH=" + os.Getenv("PATH"), "DOCKER_HOST=" + docker.DaemonHost()}
	control.Stdin = strings.NewReader(builderEscapes)
	out, err := control.CombinedOutput()
	if got := escapeResults(string(out)); err != nil || !maps.Equal(got, everyEscape("allowed")) {
		t.Fatalf("a privileged builder allowed %v (%v), want every escape; the probes no longer show anything\n%s", got, err, out)
	}

	management := httptest.NewServer(http.NotFoundHandler())
	defer management.Close()
	state := t.TempDir()
	_ = os.Chmod(state, 0700)
	journal, err := Open(state)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = journal.Close() }()
	serverID := uuid.New()
	if err = journal.SetConfig(Config{Origin: management.URL, ServerID: serverID, MachineID: uuid.New(), Role: "combined"}); err != nil {
		t.Fatal(err)
	}
	engine := &Engine{Journal: journal, Docker: docker, DockerCLI: dockerCLI}
	connection := uuid.New()
	p := pipeline.Command{Action: "build", BuildNetwork: pipeline.BuildNetworkV1, RootlessBuild: pipeline.RootlessBuildV1, Connection: pipeline.Connection{ID: connection, Provider: "gitea", BaseURL: "https://git.example.test", Repository: "team/app", RepositoryID: 12}, Commit: strings.Repeat("a", 40), Branch: "main", RevisionID: uuid.New(), UnitID: uuid.New(), AppID: uuid.New(), InputHash: strings.Repeat("a", 64), SubmittedAt: time.Now().UTC().Truncate(time.Second)}
	p.Build = &pipeline.BuildConfig{ConnectionID: connection, Branch: "main", RunnerID: serverID, Context: ".", Dockerfile: "Dockerfile", Architecture: runtime.GOARCH, Arguments: map[string]string{}, Secrets: map[string]uuid.UUID{}}
	if err = p.Validate(); err != nil {
		t.Fatal(err)
	}
	c := api.ExecutionCommand{Id: uuid.New(), ServerId: serverID, EnvironmentId: uuid.New(), ResourceId: p.UnitID, Generation: 1, Kind: "pipeline", Action: "inspect", PlanHash: p.Digest(), Deadline: time.Now().Add(5 * time.Minute)}
	if err = engine.pipelineContainer(ctx, c, p); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := engine.removePipeline(cleanup, c); err != nil {
			t.Error(err)
		}
	}()

	inspected, err := docker.ContainerInspect(ctx, pipelineName(c), client.ContainerInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	v := inspected.Container
	if h := v.HostConfig; h.Privileged || !h.ReadonlyRootfs || len(h.Devices) > 0 || !slices.Equal(h.CapDrop, []string{"ALL"}) || !slices.Equal(slices.Sorted(slices.Values(h.CapAdd)), []string{"CAP_SETGID", "CAP_SETUID"}) || v.Config.User != fmt.Sprintf("%d:%d", builderUID, builderUID) || v.AppArmorProfile != builderProfile {
		t.Fatalf("build container privileged %v, read-only %v, devices %v, capabilities -%v +%v, user %q, AppArmor %q", h.Privileged, h.ReadonlyRootfs, h.Devices, h.CapDrop, h.CapAdd, v.Config.User, v.AppArmorProfile)
	}
	var workers error
	for range 40 {
		if _, workers = engine.pipelineCLI(ctx, pipelineName(c), nil, nil, 4096, "buildctl", "debug", "workers"); workers == nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if workers != nil {
		logs, _ := exec.Command(dockerCLI, "--host", docker.DaemonHost(), "logs", pipelineName(c)).CombinedOutput()
		t.Fatalf("rootless BuildKit did not start: %v\n%s", workers, logs)
	}
	run := func(input string, args ...string) string {
		t.Helper()
		cmd := engine.pipelineExec(ctx, pipelineName(c), nil, args...)
		cmd.Stdin = strings.NewReader(input)
		var output bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &output
		if err := cmd.Run(); err != nil {
			t.Fatalf("%s: %v\n%s", strings.Join(args, " "), err, output.String())
		}
		return output.String()
	}

	// From the build container itself, as its unprivileged user.
	if out := run(builderEscapes, "sh", "-s"); !maps.Equal(escapeResults(out), everyEscape("refused")) {
		t.Fatalf("the build container allowed %v\n%s", escapeResults(out), out)
	}
	// As root of a user namespace like the one BuildKit runs in, with every capability there.
	if out := run(builderEscapes, "rootlesskit", "--net=host", "sh", "-s"); !maps.Equal(escapeResults(out), everyEscape("refused")) {
		t.Fatalf("root of the build's user namespace allowed %v\n%s", escapeResults(out), out)
	}
	// The set-user-ID program the builder never uses can't run.
	for _, program := range builderMasked {
		if out, err := engine.pipelineCLI(ctx, pipelineName(c), nil, nil, 4096, program, "-V"); err == nil {
			t.Fatalf("%s ran in the build container: %s", program, out)
		}
	}

	// A build step runs as root of BuildKit's user namespace, which is the builder's host user.
	// It is given the host disk's device number, which it can't see.
	disk := strings.TrimSpace(run("", "awk", `$5 == "/etc/hostname" {print $3; exit}`, "/proc/self/mountinfo"))
	if !regexp.MustCompile(`^[1-9][0-9]*:[0-9]+$`).MatchString(disk) {
		t.Fatalf("the host's disk is %q, not a block device", disk)
	}
	// It can still do what a build step can in any Docker build, such as chroot.
	dockerfile := "FROM " + execution.TestImage + "\nENV DISK=" + disk + "\nRUN chroot / true && echo chroot=$(echo works)\nRUN cat /proc/self/uid_map; " + strings.ReplaceAll(strings.TrimSpace(builderEscapes), "\n", "; ") + "\n"
	run(dockerfile, "sh", "-ec", "mkdir -p /workspace/context; cat > /workspace/context/Dockerfile")
	out = []byte(run("", "buildctl", "build", "--frontend", "dockerfile.v0", "--local", "context=/workspace/context", "--local", "dockerfile=/workspace/context", "--opt", "platform=linux/"+runtime.GOARCH, "--progress", "plain", "--no-cache"))
	if !regexp.MustCompile(fmt.Sprintf(`(?m)\s0\s+%d\s+1\s*$`, builderUID)).Match(out) || !regexp.MustCompile(fmt.Sprintf(`(?m)\s1\s+%d\s+%d\s*$`, builderUID+1, builderSubIDs)).Match(out) {
		t.Fatalf("a build step's user namespace does not map root to the builder's user\n%s", out)
	}
	if got := escapeResults(string(out)); !maps.Equal(got, everyEscape("refused")) {
		t.Fatalf("a build step allowed %v\n%s", got, out)
	}
	if !regexp.MustCompile(`(?m)\schroot=works$`).Match(out) {
		t.Fatalf("a build step could not chroot\n%s", out)
	}

	// The image's privileged programs are exactly those the builder masks or uses. A new
	// BuildKit image that adds one must be reviewed here.
	made, err := docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: pipeline.BuilderImage}})
	if err != nil {
		t.Fatal(err)
	}
	defer docker.ContainerRemove(context.Background(), made.ID, client.ContainerRemoveOptions{Force: true})
	export, err := docker.ContainerExport(ctx, made.ID, client.ContainerExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer export.Close()
	privileged := map[string]string{}
	archive := tar.NewReader(export)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		_, capabilities := header.PAXRecords["SCHILY.xattr.security.capability"]
		switch {
		case header.Typeflag != tar.TypeReg:
		case header.Mode&04000 != 0:
			privileged["/"+header.Name] = fmt.Sprintf("set-user-ID %d", header.Uid)
		case header.Mode&02000 != 0:
			privileged["/"+header.Name] = fmt.Sprintf("set-group-ID %d", header.Gid)
		case capabilities:
			privileged["/"+header.Name] = "file capabilities"
		}
	}
	want := map[string]string{"/usr/bin/newuidmap": "file capabilities", "/usr/bin/newgidmap": "file capabilities", "/usr/sbin/unix_chkpwd": "set-group-ID 42"}
	for _, program := range builderMasked {
		want[program] = "set-user-ID 0"
	}
	if !maps.Equal(privileged, want) {
		t.Fatalf("the builder image's privileged programs are %v, want %v", privileged, want)
	}
}
