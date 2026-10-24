package agent

import (
	"context"
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
