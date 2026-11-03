//go:build linux

package agent

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// shellIdentity names a shell by more than its host PID, which could be reused: the
// boot it ran in, the kernel's start time for the process and its PID namespace.
type shellIdentity struct {
	PID       int
	StartTime uint64
	PIDNS     string
	Boot      string
}

type processStatus struct {
	state   string
	session int
	threads int
	start   uint64
}

// exited reports a zombie. A main thread that has exited also reads as one while the
// process's other threads keep running, so that alone is not enough.
func (s processStatus) exited() bool { return s.state == "Z" && s.threads <= 1 }

// parseProcessStatus reads /proc/<pid>/stat text. The command name may contain spaces
// and parentheses, so fields resume after the last closing parenthesis.
func parseProcessStatus(text string) (processStatus, error) {
	end := strings.LastIndexByte(text, ')')
	if end < 0 {
		return processStatus{}, errors.New("invalid process status")
	}
	fields := strings.Fields(text[end+1:])
	if len(fields) < 20 {
		return processStatus{}, errors.New("invalid process status")
	}
	session, err := strconv.Atoi(fields[3])
	if err != nil {
		return processStatus{}, err
	}
	threads, err := strconv.Atoi(fields[17])
	if err != nil {
		return processStatus{}, err
	}
	start, err := strconv.ParseUint(fields[19], 10, 64)
	if err != nil {
		return processStatus{}, err
	}
	return processStatus{fields[0], session, threads, start}, nil
}

func readProcessStatus(pid int) (processStatus, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if errors.Is(err, unix.ESRCH) {
		return processStatus{}, os.ErrNotExist // it exited while being read
	}
	if err != nil {
		return processStatus{}, err
	}
	return parseProcessStatus(string(raw))
}

func bootID() (string, error) {
	raw, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	return strings.TrimSpace(string(raw)), err
}

func identifyShell(pid int) (shellIdentity, error) {
	status, err := readProcessStatus(pid)
	if err != nil {
		return shellIdentity{}, err
	}
	namespace, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid))
	if err != nil {
		return shellIdentity{}, err
	}
	boot, err := bootID()
	if err != nil {
		return shellIdentity{}, err
	}
	return shellIdentity{PID: pid, StartTime: status.start, PIDNS: namespace, Boot: boot}, nil
}

// sessionMembers lists live processes in the shell's session. A zombie has already been
// killed, and only the container's PID 1 can reap it, so it does not count.
func sessionMembers(shell shellIdentity) (map[int]uint64, error) {
	entries, err := filepath.Glob("/proc/[0-9]*")
	if err != nil {
		return nil, err
	}
	members := map[int]uint64{}
	for _, entry := range entries {
		pid, err := strconv.Atoi(filepath.Base(entry))
		if err != nil {
			continue
		}
		status, err := readProcessStatus(pid)
		if err != nil || status.session != shell.PID || status.exited() {
			continue
		}
		// A process may exit between these reads; it then simply is not a member.
		if namespace, err := os.Readlink(fmt.Sprintf("/proc/%d/ns/pid", pid)); err == nil && namespace == shell.PIDNS {
			members[pid] = status.start
		}
	}
	return members, nil
}

// shellGone reports that the identity no longer names a running session: the host has
// rebooted, or the PID now belongs to a different process. The kernel keeps a PID
// allocated while any process uses it as a session ID, so a live session cannot lose it.
// A session is always in a container's namespace, never the host's, whose processes are
// not ours to end whatever a journal says.
func shellGone(shell shellIdentity) (bool, error) {
	boot, err := bootID()
	if err != nil {
		return false, err
	}
	host, err := os.Readlink("/proc/self/ns/pid")
	if err != nil {
		return false, err
	}
	if boot != shell.Boot || shell.PIDNS == host {
		return true, nil
	}
	status, err := readProcessStatus(shell.PID)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil // the leader exited, but its session's other members may remain
	}
	if err != nil {
		return false, err
	}
	return status.start != shell.StartTime, nil
}

// shellLeaderAlive reports that the session's leader is still the process identified.
func shellLeaderAlive(shell shellIdentity) bool {
	gone, err := shellGone(shell)
	if err != nil || gone {
		return false
	}
	status, err := readProcessStatus(shell.PID)
	return err == nil && !status.exited()
}

// endShellSession kills every process in the shell's Linux session, not just its process
// group, because interactive shells place jobs in their own groups. Each signal goes
// through a pidfd, opened and then checked against the process found, so a PID reused in
// between is not signalled.
func endShellSession(shell shellIdentity) error {
	gone, err := shellGone(shell)
	if err != nil || gone {
		return err
	}
	for range 20 {
		members, err := sessionMembers(shell)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return nil
		}
		for pid, start := range members {
			fd, err := unix.PidfdOpen(pid, 0)
			if err != nil {
				continue
			}
			if status, err := readProcessStatus(pid); err == nil && status.session == shell.PID && status.start == start {
				_ = unix.PidfdSendSignal(fd, unix.SIGKILL, nil, 0)
			}
			unix.Close(fd)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("terminal session processes survived")
}
