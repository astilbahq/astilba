package agent

import (
	"encoding/json/v2"
	"sync"

	"github.com/moby/profiles/seccomp"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

// builderSyscalls are what rootless BuildKit needs beyond Docker's default seccomp profile, which
// allows them only to a container with CAP_SYS_ADMIN. The build container has none of the host's
// capabilities, so each acts only within the user namespace BuildKit creates.
var builderSyscalls = []string{"clone", "mount", "pivot_root", "sethostname", "setns", "umount2", "unshare"}

// builderSeccompProfile returns the build container's seccomp profile, which Docker reads.
var builderSeccompProfile = sync.OnceValues(builderSeccompJSON)

// builderSeccompJSON is Docker's default seccomp profile, which Docker applies to every other
// container, with builderSyscalls allowed. runc asks for a new session keyring for each build
// step, and keyctl answers that keyrings don't exist, which runc accepts, so builds never reach
// the host's keyrings.
func builderSeccompJSON() (string, error) {
	p := seccomp.DefaultProfile()
	enosys := uint(unix.ENOSYS)
	p.Syscalls = append(p.Syscalls,
		&seccomp.Syscall{LinuxSyscall: specs.LinuxSyscall{Names: builderSyscalls, Action: specs.ActAllow}},
		&seccomp.Syscall{LinuxSyscall: specs.LinuxSyscall{Names: []string{"keyctl"}, Action: specs.ActErrno, ErrnoRet: &enosys}})
	raw, err := json.Marshal(p)
	return string(raw), err
}
