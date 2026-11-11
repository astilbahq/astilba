//go:build !linux

package agent

// Agents run only on Linux. These let the rest of the session code build and run its
// stream tests on a development machine, where the daemon's processes are not visible.
type shellIdentity struct {
	PID       int
	StartTime uint64
	PIDNS     string
	Boot      string
}

func identifyShell(pid int) (shellIdentity, error) { return shellIdentity{PID: pid}, nil }
func endShellSession(shellIdentity) (bool, error)  { return false, nil }
func shellLeaderAlive(shellIdentity) bool          { return false }
