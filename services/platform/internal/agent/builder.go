package agent

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/moby/moby/api/types/mount"
)

// The build container runs rootless BuildKit as builderUID, a host user with no capability of its
// own, and BuildKit runs every build step in a user namespace where root is builderUID and the
// step's other users are the builderSubIDs host IDs after it. A process that escapes a build step
// is therefore an unprivileged host user in a container without host devices, confined by
// builderProfile and the seccomp profile builderSeccompProfile returns. No host account has these
// IDs: they are in the range systemd leaves unused, above the container ranges systemd-nspawn
// picks from and the subordinate IDs useradd hands out.
const (
	builderUID     = 2_000_000_000
	builderSubIDs  = 65536
	builderProfile = "astilba-builder"
)

// appArmorParser loads builderProfile. Ubuntu's apparmor package installs it.
const appArmorParser = "/usr/sbin/apparmor_parser"

//go:embed builder.apparmor
var builderAppArmor []byte

// builderMasked are the image's set-user-ID programs the builder never uses. Each is covered by a
// read-only empty file, so nothing in the build container can run it as host root. newuidmap and
// newgidmap stay: rootless BuildKit maps its user namespace with them, through their file
// capabilities, which is why the build container can't set no-new-privileges.
var builderMasked = []string{"/usr/bin/fusermount3"}

// builderUnsupportedNote says what a host needs to build and where the requirements are described.
const builderUnsupportedNote = "Builds need AppArmor enabled, Ubuntu's apparmor package and user namespaces, which confine the unprivileged builder; see the builder requirements in the Git builds documentation."

// builderFiles are the files builderMounts places over the image's own: the accounts rootless
// BuildKit maps its user namespace from, and the empty file that masks a program.
func builderFiles() map[string]string {
	uid := strconv.Itoa(builderUID)
	sub := "builder:" + strconv.Itoa(builderUID+1) + ":" + strconv.Itoa(builderSubIDs) + "\n"
	return map[string]string{
		"passwd": "root:x:0:0:root:/root:/sbin/nologin\nbuilder:x:" + uid + ":" + uid + ":Astilba builder:/workspace:/sbin/nologin\n",
		"group":  "root:x:0:\nbuilder:x:" + uid + ":\n",
		"subuid": sub,
		"subgid": sub,
		"masked": "",
	}
}

// builderLock keeps one build's profile load and file writes from interleaving with another's.
var builderLock sync.Mutex

// builderMounts writes builderFiles to the agent's state directory and returns the read-only
// mounts that place them in a runner container. Each is written afresh and renamed into place,
// so what Docker mounts is always a regular file the agent wrote.
func (e *Engine) builderMounts() ([]mount.Mount, error) {
	builderLock.Lock()
	defer builderLock.Unlock()
	dir := filepath.Join(e.Journal.dir, "builder-v1")
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return nil, errors.New("the builder's state directory is not a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for name, content := range builderFiles() {
		pending := ".pending-" + uuid.NewString()
		f, err := root.OpenFile(pending, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		// The agent's umask would leave them unreadable to the build container's user.
		_, err = f.WriteString(content)
		err = errors.Join(err, f.Chmod(0644), f.Close())
		if err == nil {
			err = root.Rename(pending, name)
		}
		if err != nil {
			_ = root.Remove(pending)
			return nil, err
		}
	}
	mounts := []mount.Mount{}
	for _, name := range []string{"passwd", "group", "subuid", "subgid"} {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: filepath.Join(dir, name), Target: "/etc/" + name, ReadOnly: true})
	}
	for _, target := range builderMasked {
		mounts = append(mounts, mount.Mount{Type: mount.TypeBind, Source: filepath.Join(dir, "masked"), Target: target, ReadOnly: true})
	}
	return mounts, nil
}

// loadBuilderProfile loads builderProfile into the kernel, replacing whatever an earlier agent
// loaded under its name, so a build is always confined by this agent's rules. The kernel keeps
// it until the host restarts; nothing is written to the host's AppArmor configuration.
func loadBuilderProfile(ctx context.Context) error {
	builderLock.Lock()
	defer builderLock.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, appArmorParser, "--replace", "--skip-cache", "--quiet")
	cmd.Stdin = bytes.NewReader(builderAppArmor)
	output := &boundedOutput{limit: 2048}
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Run(); err != nil {
		reason := strings.TrimSpace(strings.ToValidUTF8(output.b.String(), "�"))
		if reason == "" {
			reason = err.Error()
		}
		return fmt.Errorf("%s could not load it: %s", appArmorParser, reason)
	}
	loaded, err := os.ReadFile("/sys/kernel/security/apparmor/profiles")
	if err != nil || !slices.Contains(strings.Split(string(loaded), "\n"), builderProfile+" (enforce)") {
		return errors.New("the kernel does not list it as enforced")
	}
	return nil
}

// builderUnsupported reports whether this host lacks what the unprivileged builder needs. It only
// inspects the host: the profile is loaded when a build starts.
func builderUnsupported() bool {
	if runtime.GOOS != "linux" {
		return true
	}
	enabled, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	if err != nil || strings.TrimSpace(string(enabled)) != "Y" {
		return true
	}
	if info, err := os.Stat(appArmorParser); err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
		return true
	}
	namespaces, err := os.ReadFile("/proc/sys/user/max_user_namespaces")
	if n, parseErr := strconv.Atoi(strings.TrimSpace(string(namespaces))); err != nil || parseErr != nil || n < 1 {
		return true
	}
	_, err = builderSeccompProfile()
	return err != nil
}

// builderSecurity returns the security options for a build container, loading its AppArmor profile
// first. A host that can't confine the builder refuses the build: it never runs privileged.
func builderSecurity(ctx context.Context) ([]string, error) {
	seccomp, err := builderSeccompProfile()
	if err != nil {
		return nil, errors.New("Builds need Linux, which can confine the unprivileged builder.")
	}
	if err = loadBuilderProfile(ctx); err != nil {
		return nil, fmt.Errorf("Builds run in an unprivileged builder that AppArmor confines, and this server could not load its %s profile: %w. See the builder requirements in the Git builds documentation.", builderProfile, err)
	}
	return []string{"apparmor=" + builderProfile, "seccomp=" + seccomp}, nil
}
