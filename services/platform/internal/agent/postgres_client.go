package agent

import (
	"context"
	"errors"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// errUnsupportedPostgresImage refuses a database whose image is not a PostgreSQL release Astilba
// has pinned, current or retired: its helper would have to run that image.
var errUnsupportedPostgresImage = errors.New("the database's PostgreSQL image is not one Astilba supports")

// pgClient is where the agent runs PostgreSQL's client tools. It is a container the agent owns:
// either a database container, reached over its Unix socket, or a helper that shares a database
// container's network namespace and reaches it over TCP on the loopback address, which the
// cluster trusts without a password, as the pinned image's initdb sets it up. Only the database
// and the agent's helpers are in that namespace, so only they can connect as the cluster's
// superuser. The agent never runs anything in a database container it doesn't own.
type pgClient struct {
	container string
	env       []string
}

// sessionOptions are the settings every session the agent opens to a managed database starts with.
// A client's options outrank the database's and its roles' settings, which in the owner model the
// database's user sets, and the agent's sessions are often the cluster's superuser. So none of
// those settings decides who a session runs as (role), which functions and operators its SQL
// resolves to (search_path), what a quoted literal means (standard_conforming_strings), what is
// loaded into it (local_preload_libraries), whether it can write (default_transaction_read_only),
// or how it encodes text (client_encoding). How it shows dates and times stays the database's, so
// a restore loads them as its archive's dump did.
const sessionOptions = identityOptions + " -c standard_conforming_strings=on -c client_encoding=UTF8"

// identityOptions are sessionOptions less how a session quotes literals and encodes text, for a
// session as the database's user that reads how the user's own sessions do both.
const identityOptions = "-c role=none -c search_path=pg_catalog,pg_temp -c local_preload_libraries= -c default_transaction_read_only=off"

// pgOptions is the PGOPTIONS a client of a managed database runs with: sessionOptions and then
// each of extra, a name=value setting. PGOPTIONS replaces any a container already has, so every
// client gets it whole.
func pgOptions(extra ...string) string {
	options := "PGOPTIONS=" + sessionOptions
	for _, setting := range extra {
		options += " -c " + setting
	}
	return options
}

// dumpOptions are the settings, for pgOptions, that a session dumps with as the dump that recorded
// settings did. A later setting in PGOPTIONS replaces an earlier one, so they also replace
// sessionOptions' encoding and quoting. PGOPTIONS splits at spaces and takes a backslash as
// escaping the next character.
func dumpOptions(settings map[string]string) []string {
	escape := strings.NewReplacer(`\`, `\\`, " ", `\ `)
	options := make([]string, 0, len(backup.DumpSettings))
	for _, name := range backup.DumpSettings {
		options = append(options, name+"="+escape.Replace(settings[name]))
	}
	return options
}

// postgresHelper starts a helper for the database in container source, from that database's own
// pinned image so its tools match the server. The helper runs as the postgres user with nothing
// writable, sleeping only until ctx's deadline and a minute more, so one an agent crash leaves
// behind stops by itself but never before the work it serves. The caller removes the helper.
func (e *Engine) postgresHelper(ctx context.Context, name string, labels map[string]string, source, image string) (pgClient, error) {
	if _, ok := domain.PostgresReleaseOf(image); !ok {
		return pgClient{}, errUnsupportedPostgresImage
	}
	end, bounded := ctx.Deadline()
	if !bounded {
		return pgClient{}, errors.New("a database helper needs a bounded command")
	}
	sleep := strconv.Itoa(int(time.Until(end).Seconds()) + 60)
	// pg_dump's memory grows with the database's objects, so the helper gets the database's own
	// memory and swap limits, none when it has none, as when the tools ran inside the database
	// container.
	database, err := e.Docker.ContainerInspect(ctx, source, client.ContainerInspectOptions{})
	if err != nil {
		return pgClient{}, err
	}
	// The image declares its data directory a volume. A read-only tmpfs there keeps Docker from
	// giving each helper a writable anonymous volume that removing the helper would leave behind.
	data := mount.Mount{Type: mount.TypeTmpfs, Target: domain.PostgresMount(image), ReadOnly: true, TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 1 << 20}}
	// sleep, as the container's first process, ignores the stop signal Docker sends by default,
	// so stopping the helper, or Docker itself, would wait out the stop timeout. The helper keeps
	// nothing a stop could save, so it is killed at once.
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: image, User: "postgres", Cmd: []string{"sleep", sleep}, StopSignal: "SIGKILL", Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + source), ReadonlyRootfs: true, Mounts: []mount.Mount{data}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: database.Container.HostConfig.Memory, MemorySwap: database.Container.HostConfig.MemorySwap, PidsLimit: ptr(int64(32))}}})
	if err != nil {
		return pgClient{}, err
	}
	// A backend checks its client is still there while it waits, such as on a lock, so removing
	// the helper ends it within a second rather than once it next writes.
	helper := pgClient{container: created.ID, env: []string{"PGHOST=127.0.0.1", pgOptions("client_connection_check_interval=1000")}}
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		e.removeHelper(helper)
		return pgClient{}, err
	}
	return helper, nil
}

// removeHelper force-removes a helper, which ends anything still running in it. The database ends
// their connections within a second.
func (e *Engine) removeHelper(helper pgClient) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_, _ = e.Docker.ContainerRemove(ctx, helper.container, client.ContainerRemoveOptions{Force: true})
}

// containerExec runs command in db's container and writes its output to out. It returns once the
// output ends and Docker reports the command exited 0. Stopped part way, it returns at once and
// the command may still be running in the container, which its owner removes.
func (e *Engine) containerExec(ctx context.Context, db pgClient, out io.Writer, command ...string) error {
	return e.containerExecInput(ctx, db, nil, out, command...)
}

// containerExecInput is containerExec with input, if any, as the command's standard input. Input
// carries bytes as they are, where a command's arguments go through JSON. It is written while the
// output is read, so a command that writes before it reads can't stall either, and input not all
// written fails the command. Closing the input once written is best effort, since a command that
// has read what it needs may already have exited.
func (e *Engine) containerExecInput(ctx context.Context, db pgClient, input io.Reader, out io.Writer, command ...string) error {
	created, err := e.Docker.ExecCreate(ctx, db.container, client.ExecCreateOptions{AttachStdin: input != nil, AttachStdout: true, Env: db.env, Cmd: command})
	if err != nil {
		return errors.New("data tool failed")
	}
	attached, err := e.Docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return errors.New("data tool failed")
	}
	stop := context.AfterFunc(ctx, attached.Close)
	written := make(chan error, 1)
	if input == nil {
		written <- nil
	} else {
		go func() {
			_, err := io.Copy(attached.Conn, input)
			if err == nil {
				_ = attached.CloseWrite()
			}
			written <- err
		}()
	}
	_, err = stdcopy.StdCopy(out, io.Discard, attached.Reader)
	stop()
	attached.Close()
	if writeErr := <-written; err == nil {
		err = writeErr
	}
	// Docker may report the command running for a moment after its output ends.
	for err == nil && ctx.Err() == nil {
		v, inspectErr := e.Docker.ExecInspect(ctx, created.ID, client.ExecInspectOptions{})
		switch {
		case inspectErr != nil:
			err = inspectErr
		case !v.Running && v.ExitCode == 0:
			return nil
		case !v.Running:
			err = errors.New("exit " + strconv.Itoa(v.ExitCode))
		default:
			wait(ctx, 100*time.Millisecond)
		}
	}
	return errors.New("data tool failed")
}
