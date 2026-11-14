package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// containerExec runs command in a container, such as pg_dump or psql in a backup's source
// database, and writes its output to out. It goes through Docker's exec API rather than the
// docker CLI, because killing the CLI leaves the process running in a container the run may not
// own. However it returns, the process has ended, or its error is a *processLeft.
func (e *Engine) containerExec(ctx context.Context, containerID string, out io.Writer, command ...string) error {
	// The shell prints its PID in the container, then becomes command, so the agent can signal it.
	created, err := e.Docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{AttachStdout: true, Cmd: append([]string{"sh", "-c", `echo "$$" && exec "$@"`, "sh"}, command...)})
	if err != nil {
		return errors.New("data tool failed")
	}
	pid := &pidLine{out: out}
	attached, err := e.Docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err == nil {
		stop := context.AfterFunc(ctx, attached.Close)
		_, err = stdcopy.StdCopy(pid, io.Discard, attached.Reader)
		stop()
		attached.Close()
	}
	if err == nil && pid.pid == 0 {
		err = errors.New("no process ID")
	}
	code, ended := e.endSourceExec(containerID, created.ID, pid.pid, err != nil || ctx.Err() != nil)
	switch {
	case !ended:
		return &processLeft{what: command[0] + " in container " + containerID[:min(12, len(containerID))] + " could not be confirmed ended", container: containerID}
	case err != nil || ctx.Err() != nil || code != 0:
		return errors.New("data tool failed")
	}
	return nil
}

// endSourceExec waits for an exec to end and returns its exit code. When the agent stopped
// reading it, or it outlives its output, the agent asks it to stop, then kills it.
func (e *Engine) endSourceExec(containerID, id string, pid int, stopped bool) (int, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	signals := []string{"", "TERM", "KILL"}
	if stopped {
		signals = signals[1:]
	}
	for _, signal := range signals {
		// The PID is the exec's only while Docker reports it running, which is checked first.
		if v, err := e.Docker.ExecInspect(ctx, id, client.ExecInspectOptions{}); err == nil && v.Running && signal != "" && pid > 1 {
			if kill, err := e.Docker.ExecCreate(ctx, containerID, client.ExecCreateOptions{Cmd: []string{"sh", "-c", `kill -` + signal + ` "$1"`, "sh", strconv.Itoa(pid)}}); err == nil {
				_, _ = e.Docker.ExecStart(ctx, kill.ID, client.ExecStartOptions{Detach: true})
			}
		}
		for range 25 {
			v, err := e.Docker.ExecInspect(ctx, id, client.ExecInspectOptions{})
			if err == nil && !v.Running {
				return v.ExitCode, true
			}
			if !wait(ctx, 200*time.Millisecond) {
				return 0, false
			}
		}
	}
	return 0, false
}

// pidLine takes the PID the shell prints first, and passes everything after it to out.
type pidLine struct {
	out  io.Writer
	line []byte
	pid  int
}

func (p *pidLine) Write(b []byte) (int, error) {
	if p.pid != 0 {
		return p.out.Write(b)
	}
	end := bytes.IndexByte(b, '\n')
	if end < 0 {
		if p.line = append(p.line, b...); len(p.line) > 20 {
			return 0, errors.New("no process ID")
		}
		return len(b), nil
	}
	pid, err := strconv.Atoi(string(append(p.line, b[:end]...)))
	if err != nil || pid < 1 {
		return 0, errors.New("no process ID")
	}
	p.pid = pid
	if rest := b[end+1:]; len(rest) > 0 {
		if _, err = p.out.Write(rest); err != nil {
			return 0, err
		}
	}
	return len(b), nil
}
