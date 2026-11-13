package agent

import (
	"slices"
	"testing"

	"github.com/moby/profiles/seccomp"
	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"
)

// The build container's seccomp profile, as Docker computes it for a container with only the
// capabilities the build container has, allows what rootless BuildKit needs and nothing that
// reaches the host's kernel state, the way Docker's default denies it to every other container.
func TestBuilderSeccompProfile(t *testing.T) {
	profile, err := builderSeccompProfile()
	if err != nil {
		t.Fatal(err)
	}
	spec := &specs.Spec{Process: &specs.Process{Capabilities: &specs.LinuxCapabilities{Bounding: []string{"CAP_SETUID", "CAP_SETGID"}}}}
	effective, err := seccomp.LoadProfile(profile, spec)
	if err != nil {
		t.Fatal(err)
	}
	if effective.DefaultAction != specs.ActErrno {
		t.Fatalf("default action %s, want %s", effective.DefaultAction, specs.ActErrno)
	}
	// allowed reports whether a rule lets every call of name through, whatever its arguments.
	allowed := func(name string) bool {
		return slices.ContainsFunc(effective.Syscalls, func(s specs.LinuxSyscall) bool {
			return s.Action == specs.ActAllow && len(s.Args) == 0 && slices.Contains(s.Names, name)
		})
	}
	for _, name := range builderSyscalls {
		if !allowed(name) {
			t.Errorf("%s is not allowed; rootless BuildKit needs it", name)
		}
	}
	// Loading modules, replacing the kernel, BPF, raw I/O, file handles that skip permission
	// checks, swap, the clock and reboot all stay refused, as for any Docker container.
	for _, name := range []string{"init_module", "finit_module", "delete_module", "kexec_load", "kexec_file_load", "bpf", "iopl", "ioperm", "open_by_handle_at", "perf_event_open", "swapon", "swapoff", "settimeofday", "clock_settime", "reboot", "acct", "quotactl", "lookup_dcookie", "userfaultfd", "add_key", "request_key", "keyctl", "fsopen", "fsmount", "move_mount", "open_tree", "mount_setattr"} {
		if allowed(name) {
			t.Errorf("%s is allowed; the builder doesn't need it", name)
		}
	}
	// runc accepts that keyrings don't exist, but not that they're refused.
	if !slices.ContainsFunc(effective.Syscalls, func(s specs.LinuxSyscall) bool {
		return slices.Contains(s.Names, "keyctl") && s.Action == specs.ActErrno && s.ErrnoRet != nil && *s.ErrnoRet == uint(unix.ENOSYS)
	}) {
		t.Error("keyctl does not answer ENOSYS")
	}
}
