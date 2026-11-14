package agent

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/moby/moby/client"
)

// dockerRoot is the daemon's data-root, when the daemon is this host's own. Statfs must observe
// the daemon's host, never a similarly named client-side directory.
func dockerRoot(ctx context.Context, docker *client.Client) (string, bool) {
	if docker == nil || runtime.GOOS != "linux" || !strings.HasPrefix(docker.DaemonHost(), "unix://") {
		return "", false
	}
	info, err := docker.Info(ctx, client.InfoOptions{})
	if err != nil || !filepath.IsAbs(info.Info.DockerRootDir) {
		return "", false
	}
	return info.Info.DockerRootDir, true
}

func dockerFreeBytes(ctx context.Context, docker *client.Client) int64 {
	root, ok := dockerRoot(ctx, docker)
	if !ok {
		return 0
	}
	return freeBytes(root)
}

// containerdRoot is where Docker's containerd image store keeps image content and unpacked
// layers on a packaged install. It can be a different filesystem from Docker's data-root.
const containerdRoot = "/var/lib/containerd"

// imageStorage is the free space and free inodes where Docker writes a loaded image: the
// smaller of its data-root and, when present, containerd's root. Inodes are -1 where the
// filesystem allocates them dynamically and reports none. ok is false when the daemon's
// storage cannot be observed from here.
func imageStorage(ctx context.Context, docker *client.Client) (bytes, inodes int64, ok bool) {
	roots, ok := imageStorageRoots(ctx, docker)
	if !ok {
		return 0, 0, false
	}
	return storageFree(roots)
}

// imageStorageRoots are the directories whose filesystems Docker writes an image to.
func imageStorageRoots(ctx context.Context, docker *client.Client) ([]string, bool) {
	root, ok := dockerRoot(ctx, docker)
	if !ok {
		return nil, false
	}
	roots := []string{root}
	if _, err := os.Stat(containerdRoot); err == nil {
		roots = append(roots, containerdRoot)
	}
	return roots, true
}

// storageFree is the least free space and inodes on the filesystems of roots.
func storageFree(roots []string) (bytes, inodes int64, ok bool) {
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
