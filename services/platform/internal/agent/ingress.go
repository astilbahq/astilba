package agent

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json/v2"
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
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const proxyName = "astilba-ingress"

// A host routes at most maxRoutedUnits units, each holding at most maxUnitRoutes bytes of
// routes. No limit counts routes across units, so one environment at its limits leaves
// every other environment's room as it was. A specification holds at most 128 KiB and a
// unit's routes add only their backends to it, so no reviewed plan reaches maxUnitRoutes;
// it keeps every routing journal record within its limit.
const (
	maxRoutedUnits = 50
	maxUnitRoutes  = 256 << 10
)

// maxProxyObservation is above what the proxy can report for the most routes a host may
// have, which it reports in at most about twice the bytes they are recorded in, so no
// unit's routes can make a host's route checks fail.
const maxProxyObservation = 2 * maxRoutedUnits * maxUnitRoutes

type routeSnapshot struct {
	ManagedEgress bool                           `json:"managedEgress,omitempty"`
	Sets          map[string]deployment.RouteSet `json:"sets"`
	Ports         []int                          `json:"ports"`
	Revision      string                         `json:"revision"`
	Email         string                         `json:"email"`
	ProxyHash     string                         `json:"proxyHash"`
	// QualifiedBackends is false only in a snapshot an older agent wrote, whose route
	// file must keep comparing equal until QualifyRouteBackends replaces it.
	QualifiedBackends bool `json:"qualifiedBackends,omitempty"`
}
type routeAttempt struct {
	Previous  routeSnapshot `json:"previous"`
	Candidate routeSnapshot `json:"candidate"`
}

// routeAttempt is how preceding agents recorded an attempt; see legacyRouteState. They
// stored a replica attempt as a tuple, so that agents before them refused the record.
func (a *routeAttempt) UnmarshalJSON(raw []byte) error {
	if bytes.HasPrefix(bytes.TrimSpace(raw), []byte("[")) {
		var pair []routeSnapshot
		if err := json.Unmarshal(raw, &pair); err != nil {
			return err
		}
		if len(pair) != 2 {
			return errors.New("invalid pending route tuple")
		}
		a.Previous, a.Candidate = pair[0], pair[1]
		return nil
	}
	type object routeAttempt
	return json.Unmarshal(raw, (*object)(a))
}

func (e *Engine) routePath() string { return filepath.Join(e.Journal.dir, "ingress", "runtime.yaml") }
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
	if state.Pending {
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
	return e.publishRoutes(ctx, p, false)
}
func (e *Engine) publishReleaseRoutes(ctx context.Context, p deployment.Plan) error {
	return e.publishRoutes(ctx, p, true)
}
func (e *Engine) publishRoutes(ctx context.Context, p deployment.Plan, stableProxy bool) error {
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	if state.Pending {
		if stableProxy {
			v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
			if err != nil || proxyFingerprint(v) != state.Current.ProxyHash {
				return errors.New("release proxy recovery requires an unchanged owned proxy")
			}
		}
		// An interrupted agent cannot tell what changed the proxy since it was recorded.
		if err = e.restoreRoutes(ctx, state.Current, false, stableProxy); err != nil {
			return fmt.Errorf("previous ingress recovery requires attention: %w", err)
		}
		if err = e.rememberProxy(ctx, &state.Current); err != nil {
			return err
		}
		state.Pending = false
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
	next, err := e.candidateRoutes(state.Current, p)
	if err != nil {
		return err
	}
	state.Pending = true
	if err = e.Journal.saveRouting(state); err != nil {
		return err
	}
	if created, err := e.applyRoutes(ctx, next, drift, p.UnitID.String()); err != nil {
		reloadErr := err
		// The proxy is the one the journal records unless drift was accepted, and one this
		// attempt created is new.
		recovery := e.restoreRoutes(ctx, state.Current, !drift || created, stableProxy)
		if recovery != nil {
			return fmt.Errorf("ingress reload and previous-route recovery require attention: %w", recovery)
		}
		state.Pending = false
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
	state.Pending = false
	e.point("route-verified")
	return e.Journal.saveRouting(state)
}

// candidateRoutes is current with p's routes in place of its unit's, checked against the
// host's limits and settings before anything is written.
func (e *Engine) candidateRoutes(current routeSnapshot, p deployment.Plan) (routeSnapshot, error) {
	// The clone keeps nil collections, because route sets enter StateHash.
	raw, _ := stablejson.Marshal(current)
	var next routeSnapshot
	if err := json.Unmarshal(raw, &next); err != nil {
		return routeSnapshot{}, err
	}
	if next.Sets == nil {
		next.Sets = map[string]deployment.RouteSet{}
	}
	if len(p.Specification.Routes) == 0 {
		delete(next.Sets, p.UnitID.String())
	} else {
		set := deployment.RouteSet{UnitID: p.UnitID, EnvironmentID: p.EnvironmentID, Routes: p.Specification.Routes, Backends: map[string]deployment.Backend{}}
		for _, a := range p.Specification.Applications {
			set.Backends[a.ID.String()] = p.Backend(a.ID)
		}
		raw, err := stablejson.Marshal(set)
		if err != nil {
			return routeSnapshot{}, err
		}
		if len(raw) > maxUnitRoutes {
			return routeSnapshot{}, errors.New("this environment's routes exceed what a host can record")
		}
		next.Sets[p.UnitID.String()] = set
	}
	if len(next.Sets) > maxRoutedUnits && len(next.Sets) > len(current.Sets) {
		return routeSnapshot{}, errors.New("host ingress limit exceeded: the host already routes the most environments it can")
	}
	next.Ports = routePorts(next)
	if !reflect.DeepEqual(next.Ports, current.Ports) && !p.HostChanges {
		return routeSnapshot{}, errors.New("host port changes require owner review")
	}
	if p.HostChanges && e.ACMEEmail != "" {
		next.Email = e.ACMEEmail
	}
	for _, set := range next.Sets {
		for _, r := range set.Routes {
			if r.TLS == "managed" && next.Email == "" {
				return routeSnapshot{}, errors.New("configure the host certificate contact email and review host changes before enabling TLS")
			}
		}
	}
	if p.HostChanges {
		next.ManagedEgress = true
	}
	next.QualifiedBackends = true
	next.Revision = uuid.NewString()
	if _, err := renderSnapshot(next); err != nil {
		return routeSnapshot{}, err
	}
	return next, nil
}

// restoreRoutes puts back routes the journal records as working after an attempt to
// change them failed or was interrupted. Rewriting the file reloads the proxy in place, so
// environments the attempt did not change keep their connections, and a proxy setting the
// attempt changed recreates it only as ensureProxy decides. A proxy left running that is
// not vouched for must be the one the journal recorded: otherwise a visual publish
// recreates it, and a release or preview, whose recovery never recreates the proxy, fails
// and leaves the attempt pending. A visual publish also recreates a proxy that did not load the file. A
// route the proxy loaded but left disabled is not the proxy's fault, and recreating it
// would interrupt every environment again without changing that.
func (e *Engine) restoreRoutes(ctx context.Context, previous routeSnapshot, vouched, stableProxy bool) error {
	created, err := e.applyRoutes(ctx, previous, false, "")
	if errors.Is(err, errRouteDisabled) || err != nil && stableProxy {
		return err
	}
	if err == nil {
		// A proxy just created from previous is the one a rebuild would create.
		if vouched || created || len(previous.Ports) == 0 {
			return nil
		}
		v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
		if err != nil {
			return err
		}
		if proxyFingerprint(v) == previous.ProxyHash {
			return nil
		}
		if stableProxy {
			return errors.New("release proxy recovery requires an unchanged owned proxy")
		}
	}
	_, err = e.applyRoutes(ctx, previous, true, "")
	return err
}

// applyRoutes publishes s and waits for the proxy to report it, and says whether it
// created the proxy. It waits for the certificates of the publishing unit only, if any:
// another environment's certificate, which depends on where that environment's domain
// points, must not stop this publish.
func (e *Engine) applyRoutes(ctx context.Context, s routeSnapshot, rebuild bool, unit string) (created bool, err error) {
	raw, err := renderSnapshot(s)
	if err != nil {
		return false, err
	}
	e.point("route-publishing")
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		return false, err
	}
	e.point("route-published")
	if created, err = e.ensureProxy(ctx, s, rebuild); err != nil {
		return created, err
	}
	if len(s.Ports) == 0 {
		return created, nil
	}
	if err = e.awaitRoutes(ctx, s, raw); err != nil {
		return created, err
	}
	return created, e.verifyCertificates(ctx, s.Sets[unit])
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

// ensureProxy runs the proxy s needs, joined to its units' networks, and says whether it
// created the container.
func (e *Engine) ensureProxy(ctx context.Context, s routeSnapshot, rebuild bool) (created bool, err error) {
	cfg, err := e.Journal.Config()
	if err != nil {
		return false, err
	}
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	exists := err == nil
	if err != nil && !errdefs.IsNotFound(err) {
		return created, err
	}
	if exists && (v.Container.Config.Labels[labelServer] != cfg.ServerID.String() || v.Container.Config.Labels[labelKind] != "ingress-v1") {
		return created, errors.New("an existing ingress container is unmanaged or changed")
	}
	bindings := network.PortMap{}
	exposed := network.PortSet{}
	for _, p := range s.Ports {
		port, err := network.ParsePort(strconv.Itoa(p) + "/tcp")
		if err != nil {
			return created, err
		}
		exposed[port] = struct{}{}
		bindings[port] = []network.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: strconv.Itoa(p)}}
	}
	if len(s.Ports) == 0 {
		if exists {
			_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
			if err != nil {
				return created, err
			}
		}
		return created, nil
	}
	args := proxyArgs(s)
	// A proxy keeps the egress gateway only while the routes it runs say so, so restoring
	// routes from before managed egress also takes it away.
	gateway := 0
	if s.ManagedEgress {
		gateway = 100
	}
	current := 0
	if exists && v.Container.NetworkSettings != nil && v.Container.NetworkSettings.Networks["astilba-ingress-network"] != nil {
		current = v.Container.NetworkSettings.Networks["astilba-ingress-network"].GwPriority
	}
	gatewayChange := exists && current != gateway
	if exists && (rebuild || gatewayChange || v.Container.Config.Image != deployment.ProxyImage || !reflect.DeepEqual(v.Container.Config.Cmd, args) || !reflect.DeepEqual(v.Container.HostConfig.PortBindings, bindings)) {
		_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
		if err != nil {
			return created, err
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
		return created, bridgeErr
	}
	if bridge.Network.Labels[labelServer] != cfg.ServerID.String() || bridge.Network.Labels[labelKind] != "ingress-v1" || bridge.Network.Driver != "bridge" {
		return created, errors.New("ingress network ownership mismatch")
	}
	if !exists {
		for _, p := range s.Ports {
			listener, err := net.Listen("tcp", ":"+strconv.Itoa(p))
			if err != nil {
				return created, fmt.Errorf("ingress port %d is already in use", p)
			}
			listener.Close()
		}
		if _, err = e.Docker.ImageInspect(ctx, deployment.ProxyImage); errdefs.IsNotFound(err) {
			if err = e.pullImage(ctx, deployment.ProxyImage); err != nil {
				return created, err
			}
		} else if err != nil {
			return created, err
		}

		data := filepath.Join(e.Journal.dir, "ingress-data")
		if err = os.MkdirAll(data, 0700); err != nil {
			return created, err
		}
		info, err := os.Lstat(data)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return created, errors.New("ingress data directory is not private")
		}
		networking := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{bridgeName: {}}}
		if s.ManagedEgress {
			networking.EndpointsConfig[bridgeName].GwPriority = 100
		}
		container, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{NetworkingConfig: networking, Name: proxyName, Config: &container.Config{Image: deployment.ProxyImage, Cmd: args, ExposedPorts: exposed, Labels: map[string]string{labelServer: cfg.ServerID.String(), labelKind: "ingress-v1"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(bridgeName), PortBindings: bindings, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: filepath.Dir(e.routePath()), Target: "/etc/astilba", ReadOnly: true}, {Type: mount.TypeBind, Source: data, Target: "/data"}}, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}}})
		if err != nil {
			return created, err
		}
		created = true
		v, err = e.Docker.ContainerInspect(ctx, container.ID, client.ContainerInspectOptions{})
		if err != nil {
			return created, err
		}
	}
	for key, set := range s.Sets {
		n, err := e.Docker.NetworkInspect(ctx, deployment.NetworkName(set.UnitID), client.NetworkInspectOptions{})
		if err != nil {
			return created, err
		}
		if key != set.UnitID.String() || n.Network.Labels[labelServer] != cfg.ServerID.String() || n.Network.Labels["com.astilba.unit"] != key || n.Network.Labels[labelEnvironment] != set.EnvironmentID.String() {
			return created, errors.New("ingress network ownership mismatch")
		}
		connected := false
		for id := range n.Network.Containers {
			connected = connected || id == v.Container.ID
		}
		if !connected {
			if _, err = e.Docker.NetworkConnect(ctx, n.Network.ID, client.NetworkConnectOptions{Container: v.Container.ID}); err != nil {
				return created, err
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
			return created, err
		}
		if n.Network.Labels[labelServer] != cfg.ServerID.String() || n.Network.Labels[labelKind] != "visual-v1" {
			return created, errors.New("unexpected ingress network")
		}
		if _, err = e.Docker.NetworkDisconnect(ctx, n.Network.ID, client.NetworkDisconnectOptions{Container: v.Container.ID, Force: false}); err != nil {
			return created, err
		}
	}
	if !v.Container.State.Running {
		_, err = e.Docker.ContainerStart(ctx, v.Container.ID, client.ContainerStartOptions{})
	}
	return created, err
}

// errRouteDisabled is a route the running proxy loaded from the published file and did
// not enable.
var errRouteDisabled = errors.New("reviewed route is not enabled")

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
	// The client doesn't tie a hijacked stream to ctx, and a frozen exec holds the engine lock.
	defer context.AfterFunc(ctx, func() { attached.Close() })()
	var output bytes.Buffer
	if _, err = stdcopy.StdCopy(&output, io.Discard, io.LimitReader(attached.Reader, maxProxyObservation+1)); err != nil {
		return err
	}
	if output.Len() > maxProxyObservation {
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
				return fmt.Errorf("%w: %s (%s/%s)", errRouteDisabled, name, runtime[routerKey][name].Status, runtime[serviceKey][name].Status)
			}
		}
	}
	return nil
}

func (e *Engine) verifyCertificates(ctx context.Context, set deployment.RouteSet) error {
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
	return nil
}
