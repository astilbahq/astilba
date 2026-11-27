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
// and kills it after $1 seconds, since wget's own timeout only bounds each read. With $2
// "first" it prints the number of the first probe that fails and stops. With "every" it
// prints the number of each probe that fails and goes on, and wget writes nothing. The
// shell, the container's first process, waits for each probe itself: busybox's timeout
// would leave a watcher running for a second after every probe, and a round of quick
// probes would reach the container's process limit. The URLs are arguments, never part of
// the script.
const routeProbeScript = `limit=$1; mode=$2; shift 2; if [ "$mode" = every ]; then exec 2>/dev/null; fi; n=0; status=0; for probe; do n=$((n + 1)); wget -q --spider -T "$limit" "$probe" & pid=$!; ticks=0; while kill -0 "$pid" 2>/dev/null; do if [ "$ticks" -ge "$((limit * 50))" ]; then kill -9 "$pid"; break; fi; sleep 0.02; ticks=$((ticks + 1)); done; wait "$pid" || { echo "$n"; if [ "$mode" != every ]; then exit 1; fi; status=1; }; done; exit $status`

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
	out, stderr, err := e.runProbes(ctx, probes, "first")
	if err != nil || out == nil {
		return -1, nil, err
	}
	return out[0], stderr, nil
}

// probeEach sends every probe, as probeRound does, and returns the index of each that
// failed.
func (e *Engine) probeEach(ctx context.Context, probes []string) ([]int, error) {
	failed, _, err := e.runProbes(ctx, probes, "every")
	return failed, err
}

// runProbes runs a round of probes in mode, see routeProbeScript, and returns the index of
// each probe that failed, nil when every one passed, and what wget wrote.
func (e *Engine) runProbes(ctx context.Context, probes []string, mode string) (failed []int, stderr []byte, err error) {
	cfg, err := e.Journal.Config()
	if err != nil {
		return nil, nil, err
	}
	proxy, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		return nil, nil, err
	}
	cleanup := context.WithoutCancel(ctx)
	if err = e.removeRouteProbe(ctx, cleanup, cfg.ServerID.String()); err != nil {
		return nil, nil, err
	}
	limit := strconv.Itoa(int(deployment.PoolHealthTimeout / time.Second))
	created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: routeProbeName,
		Config: &container.Config{
			Image:        proxy.Container.Image,
			Entrypoint:   []string{"sh", "-c", routeProbeScript, "probe"},
			Cmd:          append([]string{limit, mode}, probes...),
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
		return nil, nil, err
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
		return nil, nil, err
	}
	defer attached.Close()
	// The client doesn't tie a hijacked stream to ctx.
	defer context.AfterFunc(ctx, func() { attached.Close() })()
	// The client hands the wait's result to a goroutine that waits for it to be taken, so
	// a round that ends early stops the wait and takes what it gave, before removal.
	waitCtx, stopWait := context.WithCancel(ctx)
	wait := e.Docker.ContainerWait(waitCtx, created.ID, client.ContainerWaitOptions{Condition: container.WaitConditionRemoved})
	waited := false
	defer func() {
		stopWait()
		if !waited {
			select {
			case <-wait.Result:
			case <-wait.Error:
			}
		}
	}()
	if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
		return nil, nil, err
	}
	var out, errOut bytes.Buffer
	stream := &io.LimitedReader{R: attached.Reader, N: maxProbeOutput}
	if _, err = stdcopy.StdCopy(&out, &errOut, stream); err != nil {
		// Closing the stream is how a round that ctx ended stops.
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, err
	}
	if stream.N == 0 {
		return nil, nil, errProxyOutputLimit
	}
	var status int64
	waited = true
	select {
	case result := <-wait.Result:
		if result.Error != nil {
			return nil, nil, errors.New(result.Error.Message)
		}
		status = result.StatusCode
	case err = <-wait.Error:
		return nil, nil, err
	}
	if status == 0 {
		return nil, nil, nil
	}
	for line := range strings.FieldsSeq(out.String()) {
		n, parseErr := strconv.Atoi(line)
		if parseErr != nil || n < 1 || n > len(probes) {
			return nil, nil, fmt.Errorf("route probe ended with status %d", status)
		}
		failed = append(failed, n-1)
	}
	if len(failed) == 0 {
		return nil, nil, fmt.Errorf("route probe ended with status %d", status)
	}
	return failed, errOut.Bytes(), nil
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
