package agent

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/client"
)

// errUnsupportedPostgresImage refuses a database whose image is not one of Astilba's pinned
// PostgreSQL images, such as a retired pin: its helper would have to run that image.
var errUnsupportedPostgresImage = errors.New("the database's PostgreSQL image is not one Astilba supports")

// pgClient is where the agent runs PostgreSQL's client tools. It is a container the agent owns:
// either a database container, reached over its Unix socket, or a helper that shares a database
// container's network namespace and reaches it over TCP with the database's credential. The
// agent never runs anything in a database container it doesn't own.
type pgClient struct {
	container string
	env       []string
}

// postgresHelper starts a helper for the database in container source, from that database's own
// pinned image so its tools match the server. The helper runs as the postgres user with nothing
// writable, sleeping only until ctx's deadline and a minute more, so one an agent crash leaves
// behind stops by itself but never before the work it serves. The credential goes only in each
// command's environment, never in the helper's configuration. The caller removes the helper.
func (e *Engine) postgresHelper(ctx context.Context, name string, labels map[string]string, source, image, password string) (pgClient, error) {
	if image != domain.PostgresImage && image != domain.Postgres17Image {
		return pgClient{}, errUnsupportedPostgresImage
	}
	if password == "" {
		return pgClient{}, errors.New("database initialization credential unavailable")
	}
	end, bounded := ctx.Deadline()
	if !bounded {
		return pgClient{}, errors.New("a database helper needs a bounded command")
	}
	sleep := strconv.Itoa(int(time.Until(end).Seconds()) + 60)
	// pg_dump's memory grows with the database's objects, so the helper gets the database's own
	// memory limit, none when it has none, as when the tools ran inside the database container.
	database, err := e.Docker.ContainerInspect(ctx, source, client.ContainerInspectOptions{})
	if err != nil {
		return pgClient{}, err
	}
	// The image declares its data directory a volume. A read-only tmpfs there keeps Docker from
	// giving each helper a writable anonymous volume that removing the helper would leave behind.
	data := mount.Mount{Type: mount.TypeTmpfs, Target: domain.PostgresMount(image), ReadOnly: true, TmpfsOptions: &mount.TmpfsOptions{SizeBytes: 1 << 20}}
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: name, Config: &container.Config{Image: image, User: "postgres", Cmd: []string{"sleep", sleep}, Labels: labels}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode("container:" + source), ReadonlyRootfs: true, Mounts: []mount.Mount{data}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Resources: container.Resources{Memory: database.Container.HostConfig.Memory, PidsLimit: ptr(int64(32))}}})
	if err != nil {
		return pgClient{}, err
	}
	// A backend checks its client is still there while it waits, such as on a lock, so removing
	// the helper ends it within a second rather than once it next writes.
	helper := pgClient{container: created.ID, env: []string{"PGHOST=127.0.0.1", "PGPASSWORD=" + password, "PGOPTIONS=-c client_connection_check_interval=1000"}}
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
	created, err := e.Docker.ExecCreate(ctx, db.container, client.ExecCreateOptions{AttachStdout: true, Env: db.env, Cmd: command})
	if err != nil {
		return errors.New("data tool failed")
	}
	attached, err := e.Docker.ExecAttach(ctx, created.ID, client.ExecAttachOptions{})
	if err != nil {
		return errors.New("data tool failed")
	}
	stop := context.AfterFunc(ctx, attached.Close)
	_, err = stdcopy.StdCopy(out, io.Discard, attached.Reader)
	stop()
	attached.Close()
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
