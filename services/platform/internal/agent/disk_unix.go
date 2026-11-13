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

// freeSpace is the space and inodes an unprivileged writer may still use at path. Inodes are
// -1 when the filesystem reports none, as btrfs does because it allocates them as needed.
func freeSpace(path string) (bytes, inodes int64, ok bool) {
	var s unix.Statfs_t
	if unix.Statfs(path, &s) != nil {
		return 0, 0, false
	}
	inodes = -1
	if s.Files > 0 {
		inodes = int64(s.Ffree)
	}
	return int64(s.Bavail) * int64(s.Bsize), inodes, true
}
