package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// routeProbeName is the container that sends a round of route probes. The engine lock
// keeps to one round at a time.
const routeProbeName = "astilba-route-probe"

// routeProbeScript sends each probe in turn as the proxy does, once and reading no body,
// and kills it after $1 seconds, since wget's own timeout only bounds each read. It prints
// the number of the first probe that fails and stops. The URLs are arguments, never part
// of the script.
const routeProbeScript = `limit=$1; shift; n=0; for probe; do n=$((n + 1)); timeout -s KILL "$limit" wget -q --spider -T "$limit" "$probe" || { echo "$n"; exit 1; }; done`

// probeRound sends probes from a container of their own in the proxy's network namespace,
// so they take the proxy's path, and resolve names as the proxy does, without running
// anything in the proxy, which reaps no process it did not start: wget leaves its TLS
// helper behind. The container runs the proxy's own image, so nothing is pulled, and every
// process of the round ends with it. It removes itself when the round ends, the agent
// removes it if ctx ends first, and a round removes one an interrupted agent left.
//
// It returns the index of the first probe that failed and what wget wrote about it, or -1
// when every probe passed.
func (e *Engine) probeRound(ctx context.Context, probes []string) (failed int, stderr []byte, err error) {
	cfg, err := e.Journal.Config()
	if err != nil {
		return 0, nil, err
	}
	proxy, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		return 0, nil, err
	}
	cleanup := context.WithoutCancel(ctx)
	if err = e.removeRouteProbe(ctx, cleanup, cfg.ServerID.String()); err != nil {
		return 0, nil, err
	}
	limit := strconv.Itoa(int(deployment.PoolHealthTimeout / time.Second))
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: routeProbeName,
		Config: &container.Config{
			Image:        proxy.Container.Image,
			Entrypoint:   []string{"sh", "-c", routeProbeScript, "probe"},
			Cmd:          append([]string{limit}, probes...),
			Labels:       map[string]string{labelServer: cfg.ServerID.String(), labelKind: "route-probe-v1"},
			AttachStdout: true,
			AttachStderr: true,
		},
		HostConfig: &container.HostConfig{
			NetworkMode:    container.NetworkMode("container:" + proxyName),
			AutoRemove:     true,
			ReadonlyRootfs: true,
			CapDrop:        []string{"ALL"},
			SecurityOpt:    []string{"no-new-privileges"},
			Resources:      container.Resources{Memory: 32 << 20, NanoCPUs: 500_000_000, PidsLimit: ptr(int64(16))},
		},
	})
	if err != nil {
		return 0, nil, err
	}
	defer func() {
		remove, cancel := context.WithTimeout(cleanup, 30*time.Second)
		defer cancel()
		if _, removeErr := e.Docker.ContainerRemove(remove, created.ID, client.ContainerRemoveOptions{Force: true}); removeErr != nil && !errdefs.IsNotFound(removeErr) && !errdefs.IsConflict(removeErr) && err == nil {
			err = removeErr
		}
	}()
	attached, err := e.Docker.ContainerAttach(ctx, created.ID, client.ContainerAttachOptions{Stream: true, Stdout: true, Stderr: true})
	if err != nil {
		return 0, nil, err
	}
	defer attached.Close()
	// The client doesn't tie a hijacked stream to ctx.
	defer context.AfterFunc(ctx, func() { attached.Close() })()
	wait := e.Docker.ContainerWait(ctx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return 0, nil, err
	}
	var out, errOut bytes.Buffer
	stream := &io.LimitedReader{R: attached.Reader, N: maxProbeOutput}
	if _, err = stdcopy.StdCopy(&out, &errOut, stream); err != nil {
		// Closing the stream is how a round that ctx ended stops.
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return 0, nil, err
	}
	if stream.N == 0 {
		return 0, nil, errProxyOutputLimit
	}
	var status int64
	select {
	case result := <-wait.Result:
		if result.Error != nil {
			return 0, nil, errors.New(result.Error.Message)
		}
		status = result.StatusCode
	case err = <-wait.Error:
		return 0, nil, err
	}
	if status == 0 {
		return -1, nil, nil
	}
	n, parseErr := strconv.Atoi(strings.TrimSpace(out.String()))
	if parseErr != nil || n < 1 || n > len(probes) {
		return 0, nil, fmt.Errorf("route probe ended with status %d", status)
	}
	return n - 1, errOut.Bytes(), nil
}

// removeRouteProbe removes a probe container an interrupted round left, if it is one.
func (e *Engine) removeRouteProbe(ctx, cleanup context.Context, server string) error {
	v, err := e.Docker.ContainerInspect(ctx, routeProbeName, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if v.Container.Config.Labels[labelServer] != server || v.Container.Config.Labels[labelKind] != "route-probe-v1" {
		return errors.New("route probe ownership conflict")
	}
	if _, err = e.Docker.ContainerRemove(cleanup, v.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) && !errdefs.IsConflict(err) {
		return err
	}
	return nil
}
