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
	ManagedEgress bool `json:"managedEgress,omitempty"`
	// PrivateIngress keeps the proxy running on the egress gateway, which a coordinated
	// primary's private links need, even while no route needs a port. It implies
	// ManagedEgress.
	PrivateIngress bool                           `json:"privateIngress,omitempty"`
	Sets           map[string]deployment.RouteSet `json:"sets"`
	Ports          []int                          `json:"ports"`
	Revision       string                         `json:"revision"`
	Email          string                         `json:"email"`
	ProxyHash      string                         `json:"proxyHash"`
	// QualifiedBackends is false only in a snapshot an older agent wrote, whose route
	// file must keep comparing equal until QualifyRouteBackends replaces it.
	QualifiedBackends bool `json:"qualifiedBackends,omitempty"`
}

// proxyWanted says whether s needs the proxy running: for its routes' ports, or for a
// coordinated primary's private links, which reach other hosts through it before it
// routes anything.
func proxyWanted(s routeSnapshot) bool { return len(s.Ports) > 0 || s.PrivateIngress }

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
	if !proxyWanted(state.Current) && os.IsNotExist(err) {
		fileDrift = false
	}
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return fileDrift || proxyWanted(state.Current), nil
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
	if !proxyWanted(*s) {
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
	state, err := e.settledRoutes(ctx, stableProxy)
	if err != nil {
		return err
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
	return e.attemptRoutes(ctx, state, next, drift, p.UnitID.String(), stableProxy)
}

// candidateRoutes is current with p's routes in place of its unit's, checked against the
// host's limits and settings before anything is written.
func (e *Engine) candidateRoutes(current routeSnapshot, p deployment.Plan) (routeSnapshot, error) {
	next, err := cloneRoutes(current)
	if err != nil {
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

// cloneRoutes keeps nil collections, because route sets enter StateHash.
func cloneRoutes(current routeSnapshot) (routeSnapshot, error) {
	raw, _ := stablejson.Marshal(current)
	var next routeSnapshot
	return next, json.Unmarshal(raw, &next)
}

// proxyPolicy is when applyRoutes replaces a running proxy.
type proxyPolicy int

const (
	// proxyAsNeeded replaces the proxy when it differs from the one the routes need.
	proxyAsNeeded proxyPolicy = iota
	// proxyRebuild replaces the proxy whatever it is.
	proxyRebuild
	// proxyRestore replaces the proxy as needed, except that it keeps the proxy the routes
	// record when the host can't pull the image to replace it with. That proxy served those
	// routes before, so putting them back on it restores them.
	proxyRestore
)

// applyRoutes publishes s and waits for the proxy to report it, and says whether it
// created the proxy. It waits for the certificates of the publishing unit only, if any:
// another environment's certificate, which depends on where that environment's domain
// points, must not stop this publish.
func (e *Engine) applyRoutes(ctx context.Context, s routeSnapshot, policy proxyPolicy, unit string) (created bool, err error) {
	raw, err := renderSnapshot(s)
	if err != nil {
		return false, err
	}
	proxy, err := e.prepareProxy(ctx, s, policy)
	if err != nil {
		return false, err
	}
	e.point("route-publishing")
	if err = atomicRouteFile(e.routePath(), raw); err != nil {
		return false, err
	}
	e.point("route-published")
	if created, err = e.ensureProxy(ctx, s, proxy); err != nil {
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

// proxyImage is the image the agent runs the proxy from.
func (e *Engine) proxyImage() string {
	if e.ingressImage != "" {
		return e.ingressImage
	}
	return deployment.ProxyImage
}

// proxyImageUnavailable is a proxy image the host couldn't pull. The agent pulls it before
// changing the proxy or its routes, so both are as they were.
type proxyImageUnavailable struct {
	image string
	err   error
}

func (u *proxyImageUnavailable) Error() string {
	return fmt.Sprintf("this host could not pull the ingress proxy image %s: %v", u.image, u.err)
}
func (u *proxyImageUnavailable) Unwrap() error { return u.err }

// proxyImageNote is the note for a route publish that failed: what the host couldn't pull
// followed by after when it couldn't pull the proxy image, and otherwise otherwise.
func proxyImageNote(err error, otherwise, after string) string {
	if unavailable := new(*proxyImageUnavailable); errors.As(err, unavailable) {
		return fmt.Sprintf("This host could not pull the ingress proxy image %s: %v.%s", (*unavailable).image, (*unavailable).err, after)
	}
	return otherwise
}

// pullProxyImage makes sure the host has the proxy image.
func (e *Engine) pullProxyImage(ctx context.Context) error {
	image := e.proxyImage()
	_, err := e.Docker.ImageInspect(ctx, image)
	if !errdefs.IsNotFound(err) {
		return err
	}
	if err = e.pullImage(ctx, image); err == nil {
		// A pull can end without an error and without the image.
		if _, err = e.Docker.ImageInspect(ctx, image); errdefs.IsNotFound(err) {
			err = errors.New("the pull ended without the image")
		}
	}
	if err != nil && ctx.Err() == nil {
		return &proxyImageUnavailable{image: image, err: err}
	}
	return err
}

// preparedProxy is the proxy on the host, and what ensureProxy is to do with it.
type preparedProxy struct {
	cfg             Config
	v               client.ContainerInspectResult
	exists, replace bool
	args            []string
	exposed         network.PortSet
	bindings        network.PortMap
	gatewayPriority int
}

// prepareProxy compares the proxy on the host with the one s needs, and pulls the image of
// any proxy it will create, before anything changes. A host that can't pull the image keeps
// its proxy, and the routes it serves, until a later publish can.
func (e *Engine) prepareProxy(ctx context.Context, s routeSnapshot, policy proxyPolicy) (preparedProxy, error) {
	var p preparedProxy
	var err error
	if p.cfg, err = e.Journal.Config(); err != nil {
		return p, err
	}
	p.v, err = e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	p.exists = err == nil
	if err != nil && !errdefs.IsNotFound(err) {
		return p, err
	}
	if p.exists && (p.v.Container.Config.Labels[labelServer] != p.cfg.ServerID.String() || p.v.Container.Config.Labels[labelKind] != "ingress-v1") {
		return p, errors.New("an existing ingress container is unmanaged or changed")
	}
	if !proxyWanted(s) {
		return p, nil
	}
	p.bindings, p.exposed = network.PortMap{}, network.PortSet{}
	for _, port := range s.Ports {
		parsed, err := network.ParsePort(strconv.Itoa(port) + "/tcp")
		if err != nil {
			return p, err
		}
		p.exposed[parsed] = struct{}{}
		p.bindings[parsed] = []network.PortBinding{{HostIP: netip.MustParseAddr("0.0.0.0"), HostPort: strconv.Itoa(port)}}
	}
	p.args = proxyArgs(s)
	// A proxy keeps the egress gateway only while the routes it runs say so, so restoring
	// routes from before managed egress also takes it away.
	if s.ManagedEgress {
		p.gatewayPriority = 100
	}
	if !p.exists {
		return p, e.pullProxyImage(ctx)
	}
	current := 0
	if p.v.Container.NetworkSettings != nil && p.v.Container.NetworkSettings.Networks["astilba-ingress-network"] != nil {
		current = p.v.Container.NetworkSettings.Networks["astilba-ingress-network"].GwPriority
	}
	p.replace = policy == proxyRebuild || current != p.gatewayPriority || p.v.Container.Config.Image != e.proxyImage() || !reflect.DeepEqual(p.v.Container.Config.Cmd, p.args) || !reflect.DeepEqual(p.v.Container.HostConfig.PortBindings, p.bindings)
	if !p.replace {
		return p, nil
	}
	err = e.pullProxyImage(ctx)
	if policy == proxyRestore && s.ProxyHash != "" && proxyFingerprint(p.v) == s.ProxyHash && errors.As(err, new(*proxyImageUnavailable)) {
		p.replace, err = false, nil
	}
	return p, err
}

// ensureProxy runs the proxy s needs, as prepareProxy prepared it, joined to its units'
// networks, and says whether it created the container. It removes a proxy it replaces only
// once nothing else stands in the way of its replacement.
func (e *Engine) ensureProxy(ctx context.Context, s routeSnapshot, p preparedProxy) (created bool, err error) {
	// Pulling the image can take minutes, so the proxy must still be the one prepared.
	v, err := e.Docker.ContainerInspect(ctx, proxyName, client.ContainerInspectOptions{})
	if err != nil && !errdefs.IsNotFound(err) {
		return false, err
	}
	if (err == nil) != p.exists || p.exists && (v.Container.ID != p.v.Container.ID || proxyFingerprint(v) != proxyFingerprint(p.v)) {
		return false, errors.New("the ingress proxy changed while this publish prepared it")
	}
	err = nil
	if !proxyWanted(s) {
		if p.exists {
			_, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true})
		}
		return false, err
	}
	bridgeName := "astilba-ingress-network"
	bridge, bridgeErr := e.Docker.NetworkInspect(ctx, bridgeName, client.NetworkInspectOptions{})
	if errdefs.IsNotFound(bridgeErr) {
		bridgeErr = e.createNetworks(ctx, []networkRequest{ingressNetwork(p.cfg.ServerID)})
		if bridgeErr == nil {
			bridge, bridgeErr = e.Docker.NetworkInspect(ctx, bridgeName, client.NetworkInspectOptions{})
		}
	}
	if bridgeErr != nil {
		return created, bridgeErr
	}
	if bridge.Network.Labels[labelServer] != p.cfg.ServerID.String() || bridge.Network.Labels[labelKind] != "ingress-v1" || bridge.Network.Driver != "bridge" {
		return created, errors.New("ingress network ownership mismatch")
	}
	if !p.exists || p.replace {
		data := filepath.Join(e.Journal.dir, "ingress-data")
		if err = os.MkdirAll(data, 0700); err != nil {
			return created, err
		}
		info, err := os.Lstat(data)
		if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
			return created, errors.New("ingress data directory is not private")
		}
		if p.exists {
			if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
				return created, err
			}
		}
		for _, port := range s.Ports {
			listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
			if err != nil {
				return created, fmt.Errorf("ingress port %d is already in use", port)
			}
			listener.Close()
		}
		networking := &network.NetworkingConfig{EndpointsConfig: map[string]*network.EndpointSettings{bridgeName: {GwPriority: p.gatewayPriority}}}
		container, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{NetworkingConfig: networking, Name: proxyName, Config: &container.Config{Image: e.proxyImage(), Cmd: p.args, ExposedPorts: p.exposed, Labels: map[string]string{labelServer: p.cfg.ServerID.String(), labelKind: "ingress-v1"}}, HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(bridgeName), PortBindings: p.bindings, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: filepath.Dir(e.routePath()), Target: "/etc/astilba", ReadOnly: true}, {Type: mount.TypeBind, Source: data, Target: "/data"}}, RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}}})
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
		if key != set.UnitID.String() || n.Network.Labels[labelServer] != p.cfg.ServerID.String() || n.Network.Labels["com.astilba.unit"] != key || n.Network.Labels[labelEnvironment] != set.EnvironmentID.String() {
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
		if n.Network.Labels[labelServer] != p.cfg.ServerID.String() || n.Network.Labels[labelKind] != "visual-v1" {
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
