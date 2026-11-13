package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moby/moby/client"
)

// Statfs must observe the daemon's host, never a similarly named client-side directory.
func dockerFreeBytes(ctx context.Context, docker *client.Client) int64 {
	if docker == nil || runtime.GOOS != "linux" || !strings.HasPrefix(docker.DaemonHost(), "unix://") {
		return 0
	}
	info, err := docker.Info(ctx, client.InfoOptions{})
	if err != nil || !filepath.IsAbs(info.Info.DockerRootDir) {
		return 0
	}
	return freeBytes(info.Info.DockerRootDir)
}

// containerdRoot is where Docker's containerd image store keeps image content and unpacked
// layers on a packaged install. It can be a different filesystem from Docker's data-root.
const containerdRoot = "/var/lib/containerd"

// imageStorage is the free space and free inodes where Docker writes a loaded image: the
// smaller of its data-root and, when present, containerd's root. Inodes are -1 where the
// filesystem allocates them dynamically and reports none. ok is false when the daemon's
// storage cannot be observed from here.
func imageStorage(ctx context.Context, docker *client.Client) (bytes, inodes int64, ok bool) {
	if docker == nil || runtime.GOOS != "linux" || !strings.HasPrefix(docker.DaemonHost(), "unix://") {
		return 0, 0, false
	}
	info, err := docker.Info(ctx, client.InfoOptions{})
	if err != nil || !filepath.IsAbs(info.Info.DockerRootDir) {
		return 0, 0, false
	}
	roots := []string{info.Info.DockerRootDir}
	if _, err := os.Stat(containerdRoot); err == nil {
		roots = append(roots, containerdRoot)
	}
	bytes, inodes = -1, -1
	for _, root := range roots {
		b, i, known := freeSpace(root)
		if !known {
			return 0, 0, false
		}
		if bytes < 0 || b < bytes {
			bytes = b
		}
		if i >= 0 && (inodes < 0 || i < inodes) {
			inodes = i
		}
	}
	return bytes, inodes, true
}
