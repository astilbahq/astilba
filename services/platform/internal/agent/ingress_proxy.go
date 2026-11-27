package agent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

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

// pullProxyImage makes sure the host has the proxy image. A command pulls it while it publishes
// routes and keeps the engine meanwhile, unlike a deployment's images, see lend: the publication
// is under way, and work lent the engine, such as restoring private links, would meet routes and
// a proxy part-way through changing. Docker's pull still ends as stalled, and within
// dockerPullTime, see pullImageAuth.
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

// portsFree checks that the host can listen on each of ports that which picks.
func portsFree(ports []int, which func(int) bool) error {
	for _, port := range ports {
		if !which(port) {
			continue
		}
		listener, err := net.Listen("tcp", ":"+strconv.Itoa(port))
		if err != nil {
			return fmt.Errorf("ingress port %d is already in use", port)
		}
		listener.Close()
	}
	return nil
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
		// Only the ports the running proxy holds wait for its removal to be checked.
		held := map[int]bool{}
		if p.exists && v.Container.HostConfig != nil {
			for _, bindings := range v.Container.HostConfig.PortBindings {
				for _, binding := range bindings {
					if port, err := strconv.Atoi(binding.HostPort); err == nil {
						held[port] = true
					}
				}
			}
		}
		if err = portsFree(s.Ports, func(port int) bool { return !held[port] }); err != nil {
			return created, err
		}
		if p.exists {
			if _, err = e.Docker.ContainerRemove(ctx, v.Container.ID, client.ContainerRemoveOptions{Force: true}); err != nil {
				return created, err
			}
		}
		if err = portsFree(s.Ports, func(port int) bool { return held[port] }); err != nil {
			return created, err
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
