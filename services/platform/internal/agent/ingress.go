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
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/pkg/stdcopy"
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
	return e.publishRoutes(ctx, p, false, true)
}
func (e *Engine) publishReleaseRoutes(ctx context.Context, p deployment.Plan) error {
	return e.publishRoutes(ctx, p, true, true)
}

// publishRoutesBeforeServers publishes p's routes while its servers are stopped, as an
// upgrade does before it starts its writers again; confirmRouteServers waits for them
// once they run.
func (e *Engine) publishRoutesBeforeServers(ctx context.Context, p deployment.Plan) error {
	return e.publishRoutes(ctx, p, false, false)
}

// confirmRouteServers waits until the proxy routes every health-checked pool of unit's
// published routes, which the caller has found settled and in place.
func (e *Engine) confirmRouteServers(ctx context.Context, unit uuid.UUID) error {
	state, err := e.Journal.routing()
	if err != nil {
		return err
	}
	raw, err := renderSnapshot(state.Current)
	if err != nil {
		return err
	}
	return e.awaitRouteServers(ctx, state.Current, raw, unit.String())
}

func (e *Engine) publishRoutes(ctx context.Context, p deployment.Plan, stableProxy, servers bool) error {
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
	return e.attemptRoutes(ctx, state, next, drift, routeWait{unit: p.UnitID.String(), servers: servers}, stableProxy)
}

// routesServe says whether the host already serves p's routes as publishing them would:
// its settled routes, which the running proxy serves as the journal records, would render
// the same route file and proxy if p were published, but for the route revision. It changes
// nothing, and a pending attempt or drift is never taken as serving.
func (e *Engine) routesServe(ctx context.Context, p deployment.Plan) (bool, error) {
	state, err := e.Journal.routing()
	if err != nil || state.Pending {
		return false, err
	}
	if drift, err := e.ingressDrift(ctx, state); err != nil || drift {
		return false, err
	}
	next, err := e.candidateRoutes(state.Current, p)
	if err != nil {
		// Publishing p would fail the same way.
		return false, nil
	}
	next.Revision = state.Current.Revision
	want, err := renderSnapshot(next)
	if err != nil {
		return false, err
	}
	have, err := renderSnapshot(state.Current)
	if err != nil {
		return false, err
	}
	if !bytes.Equal(want, have) || !slices.Equal(next.Ports, state.Current.Ports) || !slices.Equal(proxyArgs(next), proxyArgs(state.Current)) || proxyWanted(next) != proxyWanted(state.Current) || next.ManagedEgress != state.Current.ManagedEgress {
		return false, nil
	}
	if len(state.Current.Ports) == 0 {
		return true, nil
	}
	return e.verifyRoutes(ctx, state.Current) == nil, nil
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

// routeWait is what a publish waits for besides its routes: the publishing unit's
// certificates, and its servers unless they are not running yet. It names no unit when
// routes are restored or only the proxy changes. Only the publishing unit is waited for:
// another environment's certificate, which depends on where that environment's domain
// points, or its servers, down or not, must not stop this publish.
type routeWait struct {
	unit    string
	servers bool
}

// applyRoutes publishes s and waits for the proxy to report it and for what wait names,
// and says whether it created the proxy.
func (e *Engine) applyRoutes(ctx context.Context, s routeSnapshot, policy proxyPolicy, wait routeWait) (created bool, err error) {
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
	if wait.servers {
		if err = e.awaitRouteServers(ctx, s, raw, wait.unit); err != nil {
			return created, err
		}
	}
	return created, e.verifyCertificates(ctx, s.Sets[wait.unit])
}

// errRouteDisabled is a route the running proxy loaded from the published file and did
// not enable.
var errRouteDisabled = errors.New("reviewed route is not enabled")

// ingressRuntime is what the running proxy reports of each kind of runtime object, such
// as its routers and services, by name. A health-checked service reports the status of
// each of its servers, by URL.
type ingressRuntime map[string]map[string]struct {
	Status       string            `json:"status"`
	ServerStatus map[string]string `json:"serverStatus"`
}

// errProxyOutputLimit is a proxy command that wrote more than its caller reads.
var errProxyOutputLimit = errors.New("ingress observation exceeds limit")

// proxyExec runs cmd in the proxy and returns what it wrote to each stream, refusing more
// than limit bytes in all, and whether it ended with status 0.
func (e *Engine) proxyExec(ctx context.Context, cmd []string, limit int) (stdout, stderr []byte, ok bool, err error) {
	exec, err := e.Docker.ExecCreate(ctx, proxyName, client.ExecCreateOptions{AttachStdout: true, AttachStderr: true, Cmd: cmd})
	if err != nil {
		return nil, nil, false, err
	}
	attached, err := e.Docker.ExecAttach(ctx, exec.ID, client.ExecAttachOptions{})
	if err != nil {
		return nil, nil, false, err
	}
	defer attached.Close()
	// The client doesn't tie a hijacked stream to ctx, and a frozen exec holds the engine lock.
	defer context.AfterFunc(ctx, func() { attached.Close() })()
	var out, errOut bytes.Buffer
	// The stream frames each write, so a read that reaches the limit is cut short.
	stream := &io.LimitedReader{R: attached.Reader, N: int64(limit)}
	if _, err = stdcopy.StdCopy(&out, &errOut, stream); err != nil {
		return nil, nil, false, err
	}
	if stream.N == 0 {
		return nil, nil, false, errProxyOutputLimit
	}
	status, err := e.Docker.ExecInspect(ctx, exec.ID, client.ExecInspectOptions{})
	if err != nil {
		return nil, nil, false, err
	}
	return out.Bytes(), errOut.Bytes(), !status.Running && status.ExitCode == 0, nil
}

// readIngressRuntime asks the running proxy what it loaded.
func (e *Engine) readIngressRuntime(ctx context.Context) (ingressRuntime, error) {
	output, _, ok, err := e.proxyExec(ctx, []string{"wget", "-T", "2", "-qO-", "http://127.0.0.1:8080/api/rawdata"}, maxProxyObservation)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("ingress runtime API is unavailable")
	}
	var runtime ingressRuntime
	if err = json.Unmarshal(output, &runtime); err != nil {
		return nil, err
	}
	return runtime, nil
}

func (e *Engine) verifyRoutes(ctx context.Context, s routeSnapshot) error {
	runtime, err := e.readIngressRuntime(ctx)
	if err != nil {
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
