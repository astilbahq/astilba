//go:build linux || darwin

package agent

import (
	"io/fs"
	"os"
	"syscall"
)

// ownedByProcess reports whether info's owner is this process's effective user.
func ownedByProcess(info fs.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(stat.Uid) == os.Geteuid()
}
