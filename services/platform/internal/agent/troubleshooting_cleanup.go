package agent

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"time"

	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// troubleshootingShell is journaled, without terminal content, so a restarted agent can
// end a shell left by a crash.
type troubleshootingShell struct {
	Session     uuid.UUID
	ContainerID string
	ExecID      string
	Identity    shellIdentity
}

func (j *Journal) saveTroubleshootingShell(shell troubleshootingShell) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "troubleshooting-shells", shell.Session.String(), shell) })
}

func (j *Journal) removeTroubleshootingShell(session uuid.UUID) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("troubleshooting-shells")).Delete([]byte(session.String()))
	})
}

func (j *Journal) troubleshootingShells() ([]troubleshootingShell, error) {
	shells := []troubleshootingShell{}
	err := j.db.View(func(tx *bolt.Tx) error {
		return tx.Bucket([]byte("troubleshooting-shells")).ForEach(func(k, _ []byte) error {
			var shell troubleshootingShell
			if err := j.get(tx, "troubleshooting-shells", string(k), &shell); err != nil {
				return err
			}
			shells = append(shells, shell)
			return nil
		})
	})
	return shells, err
}

// EndJournaledShells runs at startup. No stream survives a restart, so any journaled
// shell is left over from a crash or an upgrade. A failure is logged and its entry kept
// for the next start: cleaning up a session must not stop the agent deploying.
func (e *Engine) EndJournaledShells(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	shells, err := e.Journal.troubleshootingShells()
	if err != nil {
		slog.Error("terminal session cleanup failed", "error", err)
		return
	}
	for _, shell := range shells {
		outcome, err := e.endShell(ctx, shell)
		switch {
		case err != nil:
			slog.Error("terminal session cleanup failed", "session", shell.Session, "error", err)
		case outcome == shellEnded:
			slog.Info("terminal session cleanup after restart ended the shell's processes", "session", shell.Session, "container", shell.ContainerID)
		case outcome == shellAlreadyGone:
			slog.Info("terminal session cleanup after restart found nothing left to end", "session", shell.Session, "container", shell.ContainerID)
		}
		// endShell has already warned that an untraceable shell's survivors were left.
	}
}

// shellCleanup is what ending a journaled shell found.
type shellCleanup int

const (
	shellEnded       shellCleanup = iota // processes of the session were left, and were ended
	shellAlreadyGone                     // nothing of the session was left
	shellUntraceable                     // the daemon has forgotten the exec, so survivors cannot be identified
)

// endShell ends a shell's session and forgets it. A leader still matching its identity
// needs nothing from the daemon, which may be restarting. Otherwise the daemon knows
// every exec until shortly after it exits, so an unknown exec means nothing of this
// session is left to trust: its PID may belong to something else by now.
func (e *Engine) endShell(ctx context.Context, shell troubleshootingShell) (shellCleanup, error) {
	forget := func(outcome shellCleanup) (shellCleanup, error) {
		return outcome, e.Journal.removeTroubleshootingShell(shell.Session)
	}
	ended := func(ended bool) shellCleanup {
		if ended {
			return shellEnded
		}
		return shellAlreadyGone
	}
	if shell.Identity.PID > 0 && shellLeaderAlive(shell.Identity) {
		found, err := endShellSession(shell.Identity)
		if err != nil {
			return 0, err
		}
		return forget(ended(found))
	}
	state, err := e.Docker.ExecInspect(ctx, shell.ExecID, client.ExecInspectOptions{})
	if errdefs.IsNotFound(err) {
		slog.Warn("terminal session entry dropped: the daemon no longer knows its exec, so any survivors cannot be identified", "session", shell.Session, "container", shell.ContainerID)
		return forget(shellUntraceable)
	}
	if err != nil {
		return 0, err
	}
	if shell.Identity.PID == 0 {
		// A starting shell is running before it has a PID.
		for range 50 {
			if !state.Running || state.PID > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
			if state, err = e.Docker.ExecInspect(ctx, shell.ExecID, client.ExecInspectOptions{}); err != nil {
				return 0, err
			}
		}
		if state.Running && state.PID == 0 {
			return 0, errors.New("terminal shell has not reported its process")
		}
		// An exited exec keeps its PID, which may since have been reused.
		if state.Running {
			if shell.Identity, err = identifyShell(state.PID); err != nil && !errors.Is(err, os.ErrNotExist) {
				return 0, err
			}
		}
	}
	found := false
	if shell.Identity.PID > 0 {
		if found, err = endShellSession(shell.Identity); err != nil {
			return 0, err
		}
	}
	return forget(ended(found))
}
