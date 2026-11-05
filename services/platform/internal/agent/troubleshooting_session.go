package agent

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/terminal"
	"github.com/coder/websocket"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

const (
	troubleshootingStartWait  = 60 * time.Second
	troubleshootingPing       = 30 * time.Second
	troubleshootingWrite      = 30 * time.Second
	troubleshootingMaxStreams = 2 * domain.TroubleshootingPerHost
	// Bounds the Docker calls before a shell runs, so a hung daemon cannot hold stream slots.
	troubleshootingDockerWait = 30 * time.Second
	// Well within a ticket's lifetime. Ending a shell normally takes a fraction of a second.
	troubleshootingPlaceWait = 5 * time.Second
	// Exits with 3, which is neither of the codes for a missing executable, if bash does not run.
	troubleshootingBashProbe = `/bin/bash -c "exit 0" >/dev/null 2>&1 || exit 3`
	// Input waiting for a shell that is not reading, about 1 MiB at most. Beyond this the
	// session ends: dropping some of it could run a different command from the one sent.
	troubleshootingInputQueue = 32
	// The daemon has no way to turn detach keys off, and its default would swallow Ctrl-P.
	// Nobody types this sequence, and each key but the last only waits for the next one.
	troubleshootingDetachKeys = "ctrl-@,ctrl-@,ctrl-@,ctrl-@,ctrl-@,ctrl-@"
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
		if err = e.endShell(ctx, shell); err != nil {
			slog.Error("terminal session cleanup failed", "session", shell.Session, "error", err)
			continue
		}
		slog.Info("terminal session cleanup finished after restart", "session", shell.Session, "container", shell.ContainerID)
	}
}

// endShell ends a shell's session and forgets it. A leader still matching its identity
// needs nothing from the daemon, which may be restarting. Otherwise the daemon knows
// every exec until shortly after it exits, so an unknown exec means nothing of this
// session is left to trust: its PID may belong to something else by now.
func (e *Engine) endShell(ctx context.Context, shell troubleshootingShell) error {
	if shell.Identity.PID > 0 && shellLeaderAlive(shell.Identity) {
		if err := endShellSession(shell.Identity); err != nil {
			return err
		}
		return e.Journal.removeTroubleshootingShell(shell.Session)
	}
	state, err := e.Docker.ExecInspect(ctx, shell.ExecID, client.ExecInspectOptions{})
	if errdefs.IsNotFound(err) {
		slog.Warn("terminal session entry dropped: the daemon no longer knows its exec, so any survivors cannot be identified", "session", shell.Session, "container", shell.ContainerID)
		return e.Journal.removeTroubleshootingShell(shell.Session)
	}
	if err != nil {
		return err
	}
	if shell.Identity.PID == 0 {
		// A starting shell is running before it has a PID.
		for range 50 {
			if !state.Running || state.PID > 0 {
				break
			}
			time.Sleep(100 * time.Millisecond)
			if state, err = e.Docker.ExecInspect(ctx, shell.ExecID, client.ExecInspectOptions{}); err != nil {
				return err
			}
		}
		if state.Running && state.PID == 0 {
			return errors.New("terminal shell has not reported its process")
		}
		// An exited exec keeps its PID, which may since have been reused.
		if state.Running {
			if shell.Identity, err = identifyShell(state.PID); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	if shell.Identity.PID > 0 {
		if err = endShellSession(shell.Identity); err != nil {
			return err
		}
	}
	return e.Journal.removeTroubleshootingShell(shell.Session)
}

// troubleshootingRefusal re-checks locally what management decided from the last
// heartbeat: consent, the runtime floor and eligibility can all have changed since.
func (e *Engine) troubleshootingRefusal(ctx context.Context, offer api.AgentTroubleshootingOffer) (string, string) {
	ctx, cancel := context.WithTimeout(ctx, troubleshootingDockerWait)
	defer cancel()
	enabled, err := e.Journal.TroubleshootingEnabled()
	if err != nil {
		return terminal.RefusedUnavailable, ""
	}
	if !enabled {
		return terminal.RefusedOptIn, ""
	}
	version, err := e.Docker.ServerVersion(ctx, client.ServerVersionOptions{})
	if err != nil {
		return terminal.RefusedUnavailable, ""
	}
	if !troubleshootingRuntime(version.Components) {
		return terminal.RefusedRuntime, ""
	}
	container, err := e.TroubleshootingTarget(ctx, offer.UnitId, offer.ApplicationId, offer.Instance)
	if errors.Is(err, ErrTroubleshootingIneligible) {
		return terminal.RefusedIneligible, ""
	}
	if err != nil {
		return terminal.RefusedUnavailable, ""
	}
	return "", container
}

// offerTroubleshootingSessions starts each new offer once, on its own connection. An offer
// is remembered until its ticket has expired, so a failure is not retried on every poll.
func (c *Client) offerTroubleshootingSessions(ctx context.Context, offers []api.AgentTroubleshootingOffer) {
	for _, offer := range offers {
		// Refusals need a stream too, but a flood of offers must not exhaust the host.
		if c.terminalActive.Load() >= troubleshootingMaxStreams {
			return
		}
		if _, seen := c.terminalOffers.LoadOrStore(offer.SessionId, true); seen {
			continue
		}
		c.terminalActive.Add(1)
		c.terminalSessions.Go(func() {
			defer c.terminalActive.Add(-1)
			defer func() {
				time.AfterFunc(domain.TroubleshootingTicketLifetime, func() { c.terminalOffers.Delete(offer.SessionId) })
			}()
			if err := c.runTroubleshootingSession(ctx, offer); err != nil && ctx.Err() == nil {
				slog.Error("terminal session failed", "session", offer.SessionId, "actor", offer.ActorId, "error", err)
			}
		})
	}
}

func (c *Client) dialTroubleshootingStream(ctx context.Context, session uuid.UUID) (*websocket.Conn, error) {
	cfg, err := c.Journal.Config()
	if err != nil {
		return nil, err
	}
	// A dedicated connection without the polling client's overall timeout. Nothing reuses
	// this transport, so a refused upgrade must not leave a connection idle in it.
	configured, ok := c.HTTP.Transport.(*http.Transport)
	if !ok {
		return nil, errors.New("terminal stream needs the agent's HTTP transport")
	}
	transport := configured.Clone()
	transport.DisableKeepAlives = true
	dial, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(dial, cfg.Origin+"/agent/v1/troubleshooting/"+session.String()+"/stream", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, CheckRedirect: c.HTTP.CheckRedirect}, HTTPHeader: http.Header{"Authorization": {"Bearer " + cfg.ServerID.String() + "." + cfg.Credential}}})
	if err != nil {
		return nil, err
	}
	conn.SetReadLimit(terminal.MaxFrame)
	return conn, nil
}

// claimTroubleshootingPlace takes the container's place among this agent's sessions. A
// session has already closed for management while its shell is still being ended here, so
// a person who reopens at once is not told too many are open: the wait lets that finish.
func (c *Client) claimTroubleshootingPlace(ctx context.Context, container string, wait time.Duration) (func(), bool) {
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	for {
		c.terminalMu.Lock()
		if c.terminalContainers == nil {
			c.terminalContainers = map[string]bool{}
		}
		if !c.terminalContainers[container] && len(c.terminalContainers) < domain.TroubleshootingPerHost {
			c.terminalContainers[container] = true
			c.terminalMu.Unlock()
			return func() {
				c.terminalMu.Lock()
				delete(c.terminalContainers, container)
				c.terminalMu.Unlock()
			}, true
		}
		c.terminalMu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(50 * time.Millisecond):
		}
	}
}

func (c *Client) runTroubleshootingSession(ctx context.Context, offer api.AgentTroubleshootingOffer) error {
	cfg, err := c.Journal.Config()
	if err != nil {
		return err
	}
	if offer.ControllerGeneration != cfg.ControllerGeneration {
		return errors.New("offer is from another controller generation")
	}
	reason, container := c.Engine.troubleshootingRefusal(ctx, offer)
	if reason == "" {
		release, claimed := c.claimTroubleshootingPlace(ctx, container, troubleshootingPlaceWait)
		if !claimed {
			reason = terminal.RefusedLimit
		} else {
			defer release()
		}
	}

	conn, err := c.dialTroubleshootingStream(ctx, offer.SessionId)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	wait, cancel := context.WithTimeout(ctx, troubleshootingStartWait)
	_, frame, err := conn.Read(wait)
	cancel()
	if err != nil {
		return err
	}
	if len(frame) != 1 || frame[0] != terminal.Start {
		return errors.New("expected the start signal")
	}
	if reason != "" {
		return finishTroubleshooting(ctx, conn, append([]byte{terminal.Refused}, reason...))
	}
	return c.Engine.runShell(ctx, conn, offer, container)
}

func writeTroubleshooting(ctx context.Context, conn *websocket.Conn, frame []byte) error {
	ctx, cancel := context.WithTimeout(ctx, troubleshootingWrite)
	defer cancel()
	return conn.Write(ctx, websocket.MessageBinary, frame)
}

// finishTroubleshooting sends a stream's last frame and closes with a handshake, which
// reads any input still queued. Closing over unread data resets the connection, and that
// can destroy the frame just written.
func finishTroubleshooting(ctx context.Context, conn *websocket.Conn, frame []byte) error {
	if err := writeTroubleshooting(ctx, conn, frame); err != nil {
		return err
	}
	return terminal.Close(conn, websocket.StatusNormalClosure, "")
}

// findShell returns the shell to run, preferring bash, or "" if the image has no usable
// /bin/sh. A probe runs because starting a missing executable in an attached exec only
// fails after the stream has been upgraded. /bin/sh is asked about bash, rather than bash
// being tried and sh started next, and nothing starts after /bin/sh fails to: Docker can
// stall an exec that starts just after another failed to, for as long as the caller waits,
// with the person's input waiting for a shell. Looking for the file first would need the
// archive API, which has dockerd set up the container's mounts as root on the host.
func (e *Engine) findShell(ctx context.Context, container string) (string, error) {
	missing, code, err := e.probeShell(ctx, container, []string{"/bin/sh", "-c", troubleshootingBashProbe})
	switch {
	case err != nil:
		return "", err
	case missing:
		return "", nil
	case code == 0:
		return "/bin/bash", nil
	case code == 3:
		return "/bin/sh", nil
	}
	return "", errors.New("shell probe failed")
}

// probeShell runs a command and reports whether its executable is missing, or else its
// exit code. The daemon reports a missing or unusable executable as exit code 126 or 127
// with an invalid-request error, and also gives 126 to failures unrelated to the image,
// so the code alone is not enough. Anything else says nothing about the image.
func (e *Engine) probeShell(ctx context.Context, container string, command []string) (bool, int, error) {
	probe, err := e.Docker.ExecCreate(ctx, container, client.ExecCreateOptions{Cmd: command, WorkingDir: "/"})
	if err != nil {
		return false, 0, err
	}
	_, startErr := e.Docker.ExecStart(ctx, probe.ID, client.ExecStartOptions{Detach: true})
	for attempt := 0; ; attempt++ {
		state, err := e.Docker.ExecInspect(ctx, probe.ID, client.ExecInspectOptions{})
		if err != nil {
			return false, 0, err
		}
		switch {
		case !state.Running && (state.ExitCode == 126 || state.ExitCode == 127) && (startErr == nil || errdefs.IsInvalidArgument(startErr)):
			if startErr != nil {
				slog.Warn("troubleshooting shell did not start", "shell", command[0], "error", startErr)
			}
			return true, 0, nil
		case startErr != nil:
			return false, 0, startErr
		case !state.Running:
			return false, state.ExitCode, nil
		case attempt == 50:
			return false, 0, errors.New("shell probe did not finish")
		default:
			time.Sleep(100 * time.Millisecond)
		}
	}
}

// runShell starts the shell only now that a person is connected, as the container's
// configured user, and ends everything in its session however the stream ends. The
// agent enforces the idle limit and maximum itself, as durations from this moment.
func (e *Engine) runShell(ctx context.Context, conn *websocket.Conn, offer api.AgentTroubleshootingOffer, container string) error {
	starting, cancel := context.WithTimeout(ctx, troubleshootingDockerWait)
	defer cancel()
	// Until the shell is running, a failure is reported to the person, not just the host.
	unavailable := func(cause error) error {
		return errors.Join(cause, finishTroubleshooting(ctx, conn, append([]byte{terminal.Refused}, terminal.RefusedUnavailable...)))
	}
	shell, err := e.findShell(starting, container)
	if err != nil {
		return unavailable(err)
	}
	if shell == "" {
		return finishTroubleshooting(ctx, conn, append([]byte{terminal.Refused}, terminal.RefusedNoShell...))
	}
	created, err := e.Docker.ExecCreate(starting, container, client.ExecCreateOptions{Cmd: []string{shell}, TTY: true, AttachStdin: true, AttachStdout: true, AttachStderr: true, WorkingDir: "/", Env: []string{"TERM=xterm-256color"}, DetachKeys: troubleshootingDetachKeys})
	if err != nil {
		return unavailable(err)
	}
	// Journal the exec before it can start, so no path or crash leaves a shell unknown.
	record := troubleshootingShell{Session: offer.SessionId, ContainerID: container, ExecID: created.ID}
	if err = e.Journal.saveTroubleshootingShell(record); err != nil {
		return unavailable(err)
	}
	// Cleanup uses its own context: it must run when the agent is stopping too.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := e.endShell(cleanup, record); err != nil {
			slog.Error("terminal session cleanup failed", "session", offer.SessionId, "error", err)
		}
	}()
	attached, err := e.Docker.ExecAttach(starting, created.ID, client.ExecAttachOptions{TTY: true})
	if err != nil {
		return unavailable(err)
	}
	defer attached.Close()
	if record.Identity, err = e.startedShell(starting, created.ID); err != nil {
		return unavailable(err)
	}
	if err = e.Journal.saveTroubleshootingShell(record); err != nil {
		return unavailable(err)
	}
	slog.Info("terminal session started", "session", offer.SessionId, "actor", offer.ActorId, "container", container)
	reason := e.pumpShell(ctx, conn, created.ID, attached)
	slog.Info("terminal session ended", "session", offer.SessionId, "actor", offer.ActorId, "container", container, "reason", reason)
	return nil
}

// startedShell identifies the shell's process. One that has already exited has no
// identity, and the session then just reports its exit.
func (e *Engine) startedShell(ctx context.Context, exec string) (shellIdentity, error) {
	for range 50 {
		state, err := e.Docker.ExecInspect(ctx, exec, client.ExecInspectOptions{})
		if err != nil {
			return shellIdentity{}, err
		}
		if !state.Running && state.PID > 0 {
			return shellIdentity{}, nil
		}
		if state.PID > 0 {
			identity, err := identifyShell(state.PID)
			if errors.Is(err, os.ErrNotExist) {
				return shellIdentity{}, nil
			}
			return identity, err
		}
		time.Sleep(100 * time.Millisecond)
	}
	return shellIdentity{}, errors.New("terminal shell did not start")
}

func (e *Engine) pumpShell(parent context.Context, conn *websocket.Conn, exec string, attached client.ExecAttachResult) string {
	ctx, cancel := context.WithTimeout(parent, domain.TroubleshootingMaxDuration)
	defer cancel()
	var once sync.Once
	reason := "deadline"
	// The reason is named before a closing handshake and the context cancelled after it,
	// because cancelling closes the stream at once and the reader sees the peer's reply first.
	name := func(why string) { once.Do(func() { reason = why }) }
	stop := func(why string) {
		if ctx.Err() != nil {
			return
		}
		name(why)
		cancel()
	}
	var lastInput atomic.Int64
	lastInput.Store(time.Now().UnixNano())
	var workers sync.WaitGroup
	// Stdin has its own goroutine because pongs are only handled while reading the stream:
	// a shell that stops reading must not look like a lost connection.
	stdin := make(chan []byte, troubleshootingInputQueue)
	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case input := <-stdin:
				if _, err := attached.Conn.Write(input); err != nil {
					return // the output side sees the shell end and reports it
				}
			}
		}
	})
	workers.Go(func() {
		for {
			kind, frame, err := conn.Read(ctx)
			if err != nil {
				stop("stream lost")
				return
			}
			if kind != websocket.MessageBinary || len(frame) == 0 {
				stop("invalid frame")
				return
			}
			switch frame[0] {
			case terminal.Input:
				lastInput.Store(time.Now().UnixNano())
				select {
				case stdin <- frame[1:]:
				default:
					name("input overflow")
					_ = finishTroubleshooting(ctx, conn, append([]byte{terminal.Refused}, terminal.RefusedInputOverflow...))
					cancel()
					return
				}
			case terminal.Resize:
				columns, rows, err := terminal.ParseResize(frame)
				if err != nil {
					stop("invalid frame")
					return
				}
				if _, err = e.Docker.ExecResize(ctx, exec, client.ExecResizeOptions{Height: uint(rows), Width: uint(columns)}); err != nil && ctx.Err() == nil {
					slog.Warn("terminal resize failed", "error", err)
				}
			default:
				stop("invalid frame")
				return
			}
		}
	})
	workers.Go(func() {
		buffer := make([]byte, terminal.MaxFrame)
		buffer[0] = terminal.Output
		for {
			n, err := attached.Reader.Read(buffer[1:])
			if n > 0 {
				if writeTroubleshooting(ctx, conn, buffer[:n+1]) != nil {
					stop("stream lost")
					return
				}
			}
			if err != nil {
				code, exited := e.shellExitCode(ctx, exec)
				if !exited {
					stop("shell stream lost")
					return
				}
				name("shell exited")
				_ = finishTroubleshooting(ctx, conn, []byte{terminal.Exit, code})
				cancel()
				return
			}
		}
	})
	workers.Go(func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		nextPing := time.Now().Add(troubleshootingPing)
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				if now.Sub(time.Unix(0, lastInput.Load())) >= e.troubleshootingIdle() {
					stop("idle timeout")
					return
				}
				if now.After(nextPing) {
					nextPing = now.Add(troubleshootingPing)
					ping, done := context.WithTimeout(ctx, 10*time.Second)
					err := conn.Ping(ping)
					done()
					if err != nil && ctx.Err() == nil {
						stop("stream lost")
						return
					}
				}
			}
		}
	})
	<-ctx.Done()
	conn.CloseNow()
	attached.Close()
	workers.Wait()
	if parent.Err() != nil {
		return "agent stopped"
	}
	return reason
}

// shellExitCode reports how the shell exited, if the daemon can confirm that it did. It
// allows for the daemon noticing the exit slightly after the stream ends. A broken stream
// to a shell the daemon still shows running, or no answer at all, is not an exit.
func (e *Engine) shellExitCode(ctx context.Context, exec string) (byte, bool) {
	for range 20 {
		state, err := e.Docker.ExecInspect(ctx, exec, client.ExecInspectOptions{})
		if err != nil {
			return 0, false
		}
		if !state.Running {
			return byte(state.ExitCode), true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return 0, false
}

func (e *Engine) troubleshootingIdle() time.Duration {
	if e.TroubleshootingIdle > 0 {
		return e.TroubleshootingIdle
	}
	return domain.TroubleshootingIdle
}
