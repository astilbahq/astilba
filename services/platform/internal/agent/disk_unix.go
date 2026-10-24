//go:build linux || darwin

package agent

import "golang.org/x/sys/unix"

func freeBytes(path string) int64 {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil {
		return 0
	}
	return int64(s.Bavail) * int64(s.Bsize)
}
