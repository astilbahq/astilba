package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

const proxyName = "astilba-ingress"

type routeSnapshot struct {
	Sets      map[string]deployment.RouteSet `json:"sets"`
	Ports     []int                          `json:"ports"`
	Revision  string                         `json:"revision"`
	Email     string                         `json:"email"`
	ProxyHash string                         `json:"proxyHash"`
}
type routeAttempt struct {
	Previous  routeSnapshot `json:"previous"`
	Candidate routeSnapshot `json:"candidate"`
}
type routeState struct {
	Current routeSnapshot `json:"current"`
	Pending *routeAttempt `json:"pending,omitempty"`
}

func (j *Journal) routing() (routeState, error) {
	var v routeState
	err := j.db.View(func(tx *bolt.Tx) error { return j.get(tx, "routing", "host", &v) })
	if errors.Is(err, os.ErrNotExist) {
		return routeState{Current: routeSnapshot{Sets: map[string]deployment.RouteSet{}, Ports: []int{}}}, nil
	}
	return v, err
}
func (j *Journal) saveRouting(v routeState) error {
	return j.db.Update(func(tx *bolt.Tx) error { return j.put(tx, "routing", "host", v) })
}
func (e *Engine) routePath() string { return filepath.Join(e.Journal.dir, "ingress", "runtime.yaml") }
func renderSnapshot(s routeSnapshot) ([]byte, error) {
	raw, err := deployment.RouteConfig(s.Sets)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err = json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if s.Revision != "" {
		http, _ := out["http"].(map[string]any)
		if http == nil {
			http = map[string]any{}
			out["http"] = http
		}
		routers, _ := http["routers"].(map[string]any)
		if routers == nil {
			routers = map[string]any{}
			http["routers"] = routers
		}
		routers["activation-"+s.Revision] = map[string]any{"entryPoints": []string{"traefik"}, "rule": "Path(`/__astilba_activation`)", "service": "api@internal"}
	}
	return json.Marshal(out)
}
func atomicRouteFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("ingress directory must be private")
	}
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return errors.New("ingress file must be regular")
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	f, err := os.CreateTemp(dir, ".route-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func routePorts(s routeSnapshot) []int {
	set := map[int]bool{}
	for _, p := range s.Ports {
		set[p] = true
	}
	if len(s.Sets) > 0 {
		set[80] = true
		set[443] = true
	}
	for _, unit := range s.Sets {
		for _, r := range unit.Routes {
			if r.Protocol == "tcp" {
				set[r.ListenPort] = true
			}
		}
	}
	ports := []int{}
	for p := range set {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	return ports
}
func proxyFingerprint(v client.ContainerInspectResult) string {
	networks := []string{}
	if v.Container.NetworkSettings != nil {
		for name := range v.Container.NetworkSettings.Networks {
			networks = append(networks, name)
		}
	}
	sort.Strings(networks)
	return deployment.Hash([]any{v.Container.Config, v.Container.HostConfig, networks})
}
func (e *Engine) ingressDrift(ctx context.Context, state routeState) (bool, error) {
	if state.Pending != nil {
		return true, nil
	}
	raw, err := os.ReadFile(e.routePath())
	expected, renderErr := renderSnapshot(state.Current)
	if renderErr != nil {
		return false, renderErr
	}
	fileDrift := err != nil || !bytes.Equal(raw, expected)
	if len(state.Current.Ports) == 0 && os.IsNotExist(err) {
		fileDrift = false
	}
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return fileDrift || len(state.Current.Ports) > 0, nil
	}
	if err != nil {
		return false, err
	}
	cfg, err := e.Journal.Config()
	if err != nil {
		return false, err
	}
	if v.Container.Config.Labels[labelServer] != cfg.ServerID.String() || v.Container.Config.Labels[labelKind] != "ingress-v1" {
		return true, nil
	}
	return fileDrift || state.Current.ProxyHash == "" || proxyFingerprint(v) != state.Current.ProxyHash || !v.Container.State.Running, nil
}
func (e *Engine) rememberProxy(ctx context.Context, s *routeSnapshot) error {
	s.ProxyHash = ""
	if len(s.Ports) == 0 {
		return nil
	}
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil {
		return err
	}
	s.ProxyHash = proxyFingerprint(v)
	return nil
}
func (e *Engine) publishVisualRoutes(ctx context.Context, p deployment.Plan) error {
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	if state.Pending != nil {
		if err = e.applyRoutes(ctx, state.Pending.Previous, true); err != nil {
			return fmt.Errorf("previous ingress recovery requires attention: %w", err)
		}
		state.Current = state.Pending.Previous
		if err = e.rememberProxy(ctx, &state.Current); err != nil {
			return err
		}
		state.Pending = nil
		if err = e.Journal.saveRouting(state); err != nil {
			return err
		}
	}
	drift, err := e.ingressDrift(ctx, state)
	if err != nil {
		return err
	}
	if drift && !p.AcceptDrift {
		return errors.New("ingress changed outside Astilba; explicit reconciliation required")
	}
	if len(p.Specification.Routes) == 0 && len(state.Current.Sets) == 0 {
		return nil
	}
	raw, _ := json.Marshal(state.Current)
	var next routeSnapshot
	if err = json.Unmarshal(raw, &next); err != nil {
		return err
	}
	if next.Sets == nil {
		next.Sets = map[string]deployment.RouteSet{}
	}
	if len(p.Specification.Routes) == 0 {
		delete(next.Sets, p.UnitID.String())
	} else {
		set := deployment.RouteSet{UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: p.Specification.Routes, Backends: map[string]deployment.Backend{}}
		for _, a := range p.Specification.Applications {
			set.Backends[a.ID.String()] = deployment.Backend{Address: deployment.ServiceName(a.ID)}
		}
		next.Sets[p.UnitID.String()] = set
	}
	count := 0
	for _, s := range next.Sets {
		count += len(s.Routes)
	}
	if count > 100 || len(next.Sets) > 50 {
		return errors.New("host ingress limit exceeded")
	}
	next.Ports = routePorts(next)
	if !reflect.DeepEqual(next.Ports, state.Current.Ports) && !p.HostChanges {
		return errors.New("host port changes require owner review")
	}
	if p.HostChanges && e.ACMEEmail != "" {
		next.Email = e.ACMEEmail
	}
	for _, set := range next.Sets {
		for _, r := range set.Routes {
			if r.TLS == "managed" && next.Email == "" {
				return errors.New("configure the host certificate contact email and review host changes before enabling TLS")
			}
		}
	}
	next.Revision = uuid.NewString()
	if _, err = renderSnapshot(next); err != nil {
		return err
	}
	state.Pending = &routeAttempt{Previous: state.Current, Candidate: next}
	if err = e.Journal.saveRouting(state); err != nil {
		return err
	}
	if err = e.applyRoutes(ctx, next, drift); err != nil {
		reloadErr := err
		recovery := e.applyRoutes(ctx, state.Current, true)
		if recovery != nil {
			return fmt.Errorf("ingress reload and previous-route recovery require attention: %w", recovery)
		}
		state.Pending = nil
		if err = e.rememberProxy(ctx, &state.Current); err != nil {
			return err
		}
		if e2 := e.Journal.saveRouting(state); e2 != nil {
			return e2
		}
		return fmt.Errorf("ingress reload was rejected; previous routes were restored: %w", reloadErr)
	}
	if err = e.rememberProxy(ctx, &next); err != nil {
		return err
	}
	state.Current = next
	state.Pending = nil
	e.point("route-verified")
	return e.Journal.saveRouting(state)
}
func (e *Engine) applyRoutes(ctx context.Context, s routeSnapshot, rebuild bool) error {
	raw, err := renderSnapshot(s)
	if err != nil {
		return err
	}
	e.point("route-publishing")
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		return err
	}
	e.point("route-published")
	if err = e.ensureProxy(ctx, s, rebuild); err != nil {
		return err
	}
	if len(s.Ports) == 0 {
		return nil
	}
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	for {
		if err = e.verifyRoutes(deadline, s); err == nil {
			current, readErr := os.ReadFile(e.routePath())
			if readErr != nil || !bytes.Equal(current, raw) {
				return errors.New("published route file changed before activation")
			}
			return e.verifyCertificates(ctx, s)
		}
		select {
		case <-deadline.Done():
			return fmt.Errorf("route runtime verification failed: %w", err)
		case <-time.After(200 * time.Millisecond):
		}
	}
}
func proxyArgs(s routeSnapshot) []string {
	args := []string{"--providers.file.filename=/etc/astilba/runtime.yaml", "--providers.file.watch=true", "--providers.providersThrottleDuration=100ms", "--entrypoints.web.address=:80", "--entrypoints.websecure.address=:443", "--api.insecure=true", "--api.dashboard=false", "--entrypoints.traefik.address=127.0.0.1:8080"}
	for _, p := range s.Ports {
		if p != 80 && p != 443 {
			args = append(args, fmt.Sprintf("--entrypoints.tcp-%d.address=:%d", p, p))
		}
	}
	if s.Email != "" {
		args = append(args, "--certificatesresolvers.letsencrypt.acme.email="+s.Email, "--certificatesresolvers.letsencrypt.acme.storage=/data/acme.json", "--certificatesresolvers.letsencrypt.acme.httpchallenge.entrypoint=web")
	}
	return args
}
func (e *Engine) ensureProxy(ctx context.Context, s routeSnapshot, rebuild bool) error {
	cfg, err := e.Journal.Config()
	if err != nil {
		return err
	}
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	exists := err == nil
	if err != nil && !errdefs.IsNotFound(err) {
		return err
	}
	if exists && (v.Container.Config.Labels[labelServer] != cfg.ServerID.String() || v.Container.Config.Labels[labelKind] != "ingress-v1") {
		return errors.New("an existing ingress container is unmanaged or changed")
	}
	bindings := network.PortMap{}
	exposed := network.PortSet{}
	for _, p := range s.Ports {
		port, err := network.ParsePort(strconv.Itoa(p) + "/tcp")
		if err != nil {
			return err
		}
		exposed[port] = struct{}{}
		bindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: strconv.Itoa(p)}}
	}
	if len(s.Ports) == 0 {
		if exists {
			_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
			if err != nil {
				return err
			}
		}
		return nil
	}
	args := proxyArgs(s)
	if exists && (rebuild || v.Container.Config.Image != deployment.ProxyImage || !reflect.DeepEqual(v.Container.Config.Cmd, args) || !reflect.DeepEqual(v.Container.HostConfig.PortBindings, bindings)) {
		_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
		if err != nil {
			return err
		}
		exists = false
	}
	bridgeName := "astilba-ingress-network"
	bridge, bridgeErr := e.Docker.NetworkInspect(ctx, bridgeName, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(bridgeErr) {
		_, bridgeErr = e.Docker.NetworkCreate(ctx, bridgeName, client.NetworkCreateOptions{Driver: "bridge", Labels: map[string]string{labelServer: cfg.ServerID.String(), labelKind: "ingress-v1"}})
		if bridgeErr == nil {
			bridge, bridgeErr = e.Docker.NetworkInspect(ctx, bridgeName, client.NetworkInspectOptions{})
		}
	}
	if bridgeErr != nil {
		return bridgeErr
	}
	if bridge.Network.Labels[labelServer] != cfg.ServerID.String() || bridge.Network.Labels[labelKind] != "ingress-v1" || bridge.Network.Driver != "bridge" {
		return errors.New("ingress network ownership mismatch")
	}
	if !exists {
		for _, p := range s.Ports {
			listener, err := net.Listen("tcp", ":"+strconv.Itoa(p))
			if err != nil {
				return fmt.Errorf("ingress port %d is already in use", p)
			}
			listener.Close()
		}
		if err = e.pullImage(ctx, deployment.ProxyImage); err != nil {
			return err
		}

		data := filepath.Join(e.Journal.dir, "ingress-data")
		if err = os.MkdirAll(data, 0700); err != nil {
			return err
		}
		info, err := os.Lstat(data)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return errors.New("ingress data directory is not private")
		}
		created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Name: proxyName, Config: &container.Config{Image: deployment.ProxyImage, Cmd: args, ExposedPorts: exposed, Labels: map[string]string{labelServer: cfg.ServerID.String(), labelKind: "ingress-v1"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(bridgeName), PortBindings: bindings, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: filepath.Dir(e.routePath()), Target: "/etc/astilba", ReadOnly: true}, {Type: mount.TypeBind, Source: data, Target: "/data"}}, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}}})
		if err != nil {
			return err
		}
		v, err = e.Docker.ContainerInspect(ctx, created.ID, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
	}
	for key, set := range s.Sets {
		n, err := e.Docker.NetworkInspect(ctx, deployment.NetworkName(set.UnitID), client.NetworkInspectOptions{})
		if err != nil {
			return err
		}
		if key != set.UnitID.String() || n.Network.Labels[labelServer] != cfg.ServerID.String() || n.Network.Labels["com.astilba.unit"] != key || n.Network.Labels[labelEnvironment] != set.EnvironmentID.String() {
			return errors.New("ingress network ownership mismatch")
		}
		connected := false
		for id := range n.Network.Containers {
			connected = connected || id == v.Container.ID
		}
		if !connected {
			if _, err = e.Docker.NetworkConnect(ctx, n.Network.ID, client.NetworkConnectOptions{Container: v.Container.ID}); err != nil {
				return err
			}
		}
	}
	for name := range v.Container.NetworkSettings.Networks {
		if name == bridgeName {
			continue
		}
		keep := false
		for _, set := range s.Sets {
			keep = keep || name == deployment.NetworkName(set.UnitID)
		}
		if keep {
			continue
		}
		n, err := e.Docker.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
		if err != nil {
			return err
		}
		if n.Network.Labels[labelServer] != cfg.ServerID.String() || n.Network.Labels[labelKind] != "visual-v1" {
			return errors.New("unexpected ingress network")
		}
		if _, err = e.Docker.NetworkDisconnect(ctx, n.Network.ID, client.NetworkDisconnectOptions{Container: v.Container.ID, Force: false}); err != nil {
			return err
		}
	}
	if !v.Container.State.Running {
		_, err = e.Docker.ContainerStart(ctx, v.Container.ID, client.ContainerStartOptions{})
	}
	return err
}
func (e *Engine) verifyRoutes(ctx context.Context, s routeSnapshot) error {
	exec, err := e.Docker.ExecCreate(ctx, proxyName, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: []string{"wget", "-T", "2", "-qO-", "http://127.0.0.1:8080/api/rawdata"}})
	if err != nil {
		return err
	}
	attached, err := e.Docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer attached.Close()
	var output bytes.Buffer
	if _, err = stdcopy.StdCopy(&output, io.Discard, io.LimitReader(attached.Reader, 524289)); err != nil {
		return err
	}
	if output.Len() > 524288 {
		return errors.New("ingress observation exceeds limit")
	}
	status, err := e.Docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil || status.Running || status.ExitCode != 0 {
		return errors.New("ingress runtime API is unavailable")
	}
	var runtime map[string]map[string]struct {
		Status string `json:"status"`
	}
	if err = json.Unmarshal(output.Bytes(), &runtime); err != nil {
		return err
	}
	if runtime["routers"]["activation-"+s.Revision+"@file"].Status != "enabled" {
		return errors.New("new route revision is not active")
	}
	for _, set := range s.Sets {
		for _, r := range set.Routes {
			name := deployment.UnitName(set.UnitID) + "-" + r.ID.String() + "@file"
			routerKey, serviceKey := "routers", "services"
			if r.Protocol == "tcp" {
				routerKey = "tcpRouters"
				serviceKey = "tcpServices"
			}
			if runtime[routerKey][name].Status != "enabled" || runtime[serviceKey][name].Status != "enabled" {
				return fmt.Errorf("reviewed route %s is not enabled (%s/%s)", name, runtime[routerKey][name].Status, runtime[serviceKey][name].Status)
			}
		}
	}
	return nil
}

func (e *Engine) verifyCertificates(ctx context.Context, s routeSnapshot) error {
	for _, set := range s.Sets {
		for _, route := range set.Routes {
			if route.TLS != "managed" {
				continue
			}
			check, cancel := context.WithTimeout(ctx, 2*time.Minute)
			dialer := tls.Dialer{NetDialer: &net.Dialer{Timeout: 3 * time.Second}, Config: &tls.Config{ServerName: route.Domain, MinVersion: tls.VersionTLS12}}
			for {
				conn, err := dialer.DialContext(check, "tcp", "127.0.0.1:443")
				if err == nil {
					conn.Close()
					break
				}
				select {
				case <-check.Done():
					cancel()
					return errors.New("the managed TLS certificate is not ready or trusted")
				case <-time.After(time.Second):
				}
			}
			cancel()
		}
	}
	return nil
}
