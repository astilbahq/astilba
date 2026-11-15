package agent

import (
	"context"
	"errors"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/moby/moby/client"
	"path/filepath"
)

// Preflight advertises continuation only after this journal opened successfully.
func (j *Journal) Preflight(ctx context.Context, docker *client.Client, version, dir string, cli ...string) api.AgentHeartbeat {
	h := Preflight(ctx, docker, version, dir, cli...)
	if j.formatVersion.Load() >= localStateVersion && h.Capabilities.CrossHost != nil && *h.Capabilities.CrossHost {
		yes := true
		h.Capabilities.MaintenanceContinuation = &yes
	}
	j.reportTroubleshootingConsent(&h)
	return h
}

func Preflight(ctx context.Context, docker *client.Client, version, dir string, cli ...string) api.AgentHeartbeat {
	h := api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Os: runtime.GOOS, Arch: api.AgentCapabilitiesArch(runtime.GOARCH), Protocol: 1, Version: version}, Preflight: api.HostPreflight{Notes: []string{}}}
	credentialFreeCancel := true
	h.Capabilities.CredentialFreeVisualCancel = &credentialFreeCancel
	machineBoundRotation := true
	h.Capabilities.MachineBoundRotation = &machineBoundRotation
	acknowledgesReleases := true
	h.Capabilities.AcknowledgesReleases = &acknowledgesReleases
	raw, _ := os.ReadFile("/etc/os-release")
	var id, release string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "ID=") {
			id = strings.Trim(strings.TrimPrefix(line, "ID="), "\"")
		}
		if strings.HasPrefix(line, "VERSION_ID=") {
			release = strings.Trim(strings.TrimPrefix(line, "VERSION_ID="), "\"")
		}
	}
	h.Preflight.OsRelease = id + " " + release
	if runtime.GOOS != "linux" || id != "ubuntu" || release != "24.04" {
		h.Preflight.Notes = append(h.Preflight.Notes, "Supported deployment host: Ubuntu 24.04 LTS.")
	}
	runtimeReady := false
	if docker != nil {
		v, versionErr := docker.ServerVersion(ctx, client.ServerVersionOptions{})
		if versionErr == nil {
			h.Capabilities.Docker = true
			h.Preflight.DockerVersion = v.Version
			runtimeReady = troubleshootingRuntime(v.Components)
		}
		if info, e := docker.Info(ctx, client.InfoOptions{}); e == nil {
			if note := imageStoreNote(info.Info.DriverStatus, v.APIVersion, versionErr == nil); note != "" {
				h.Preflight.Notes = append(h.Preflight.Notes, note)
			}
		}
		list, e := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Limit: 1001})
		if e == nil {
			h.Preflight.ExistingContainers = len(list.Items)
		}
	}
	h.Preflight.FreeBytes = freeBytes(dir)
	busy := func(port string) bool {
		l, e := net.Listen("tcp", ":"+port)
		if e != nil {
			return true
		}
		l.Close()
		return false
	}
	h.Preflight.Port80Busy = busy("80")
	h.Preflight.Port443Busy = busy("443")
	if h.Preflight.Port80Busy || h.Preflight.Port443Busy {
		h.Preflight.Notes = append(h.Preflight.Notes, "Ingress ports are occupied or unavailable. Existing workloads will not be adopted.")
	}
	if h.Preflight.ExistingContainers > 0 {
		h.Preflight.Notes = append(h.Preflight.Notes, "Existing containers stay unmanaged.")
	}
	if !h.Capabilities.Docker {
		h.Preflight.Notes = append(h.Preflight.Notes, "Docker is unavailable; install or configure it explicitly.")
	}
	h.Preflight.Ready = h.Capabilities.Docker && h.Preflight.FreeBytes >= 256<<20 && id == "ubuntu" && release == "24.04" && (runtime.GOARCH == "amd64" || runtime.GOARCH == "arm64")
	dockerCLI := "docker"
	if len(cli) > 0 {
		dockerCLI = cli[0]
	}
	check, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	compose, err := composeVersion(check, dockerCLI)
	h.Preflight.ComposeVersion = &compose
	visual := h.Preflight.Ready && err == nil
	// A build runs only behind the build network policy, so a host that can't install it
	// offers none. The check has its own time, so a slow Docker can't make it look missing.
	policyCtx, policyDone := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	buildNetwork := (hostnetwork.Host{Dir: dir}).BuildPolicySupported(policyCtx)
	policyDone()
	if h.Preflight.Ready && !buildNetwork {
		h.Preflight.Notes = append(h.Preflight.Notes, buildPolicyUnsupported)
	}
	// A build runs only in the unprivileged builder, so a host that can't confine it offers none.
	rootless := !builderUnsupported()
	if h.Preflight.Ready && !rootless {
		h.Preflight.Notes = append(h.Preflight.Notes, builderUnsupportedNote)
	}
	build := h.Preflight.Ready && buildNetwork && rootless && dockerFreeBytes(ctx, docker) >= 4<<30
	if raw, err := os.ReadFile("/etc/docker/daemon.json"); build && err == nil && dockerFirewallOff(raw) {
		h.Preflight.Notes = append(h.Preflight.Notes, dockerFirewallOffNote)
	}
	if runtime.GOOS == "linux" && instanceMetadataAnswers(ctx, instanceMetadataEndpoints, time.Now()) {
		h.Preflight.Notes = append(h.Preflight.Notes, instanceMetadataNote)
	}
	h.Capabilities.Build = &build
	h.Capabilities.BuildNetwork = &buildNetwork
	h.Capabilities.RootlessBuilds = &build
	h.Capabilities.BuildControls = &build
	h.Capabilities.BuildCache = &build
	if build {
		version := pipeline.RailpackVersion
		h.Capabilities.RailpackVersion = &version
		inputs := pipeline.RailpackFor(runtime.GOARCH).Digest()
		h.Capabilities.RailpackInputs = &inputs
	}
	h.Capabilities.SafeReleases = &visual
	h.Capabilities.Replicas = &visual
	h.Capabilities.Previews = &visual
	h.Capabilities.Retirement = &visual
	h.Capabilities.Purge = &visual
	h.Capabilities.ServiceOutputs = &visual
	h.Capabilities.Visual = &visual
	h.Capabilities.Compose = &visual
	switch {
	case visual && runtimeReady:
		h.Capabilities.Troubleshooting = &visual
	case visual:
		unavailable := api.Runtime
		h.Capabilities.TroubleshootingUnavailable = &unavailable
	default:
		unavailable := api.Host
		h.Capabilities.TroubleshootingUnavailable = &unavailable
	}
	resticCLI := "restic"
	if len(cli) > 1 {
		resticCLI = cli[1]
	}
	versionCheck, stop := context.WithTimeout(ctx, 2*time.Second)
	restic, resticErr := exec.CommandContext(versionCheck, resticCLI, "version").Output()
	stop()
	backups := visual && resticErr == nil && strings.HasPrefix(string(restic), "restic "+backup.ResticVersion+" ")
	h.Capabilities.Backups = &backups
	h.Capabilities.Schedules = &backups
	h.Capabilities.Upgrades = &backups

	if err != nil {
		h.Preflight.Notes = append(h.Preflight.Notes, "Visual deployment requires Docker Compose 5.6.1.")
	}

	networkCtx, done := context.WithTimeout(ctx, 2*time.Second)
	defer done()
	host := hostnetwork.Host{Dir: dir}
	marker, markerErr := os.Lstat(filepath.Join(dir, "network-enabled"))
	crossHost := visual && markerErr == nil && marker.Mode().IsRegular() && marker.Mode().Perm()&0077 == 0 && host.Supported(networkCtx)
	if crossHost && docker != nil {
		var inventory api.CrossHostInventory
		inventory, err = capacityInventory(networkCtx, docker)
		if err == nil {
			h.Preflight.CrossHostInventory = &inventory
		} else {
			crossHost = false
		}
	}
	h.Capabilities.CrossHost = &crossHost
	if crossHost {
		h.Capabilities.PrimaryIngress = &crossHost
	}
	return h
}

func capacityInventory(ctx context.Context, docker *client.Client) (api.CrossHostInventory, error) {
	info, infoErr := docker.Info(ctx, client.InfoOptions{})
	containers, listErr := docker.ContainerList(ctx, client.ContainerListOptions{Limit: 1001})
	known := infoErr == nil && listErr == nil && len(containers.Items) <= 1000 && info.Info.NCPU > 0 && info.Info.MemTotal > 0
	unbounded := []string{}
	if known {
		for _, row := range containers.Items {
			c, err := docker.ContainerInspect(ctx, row.ID, client.ContainerInspectOptions{})
			if err != nil {
				known = false
				break
			}
			if c.Container.HostConfig == nil || c.Container.HostConfig.Memory <= 0 || c.Container.HostConfig.NanoCPUs <= 0 {
				unbounded = append(unbounded, row.ID)
			}
		}
	}
	if !known {
		return api.CrossHostInventory{}, errors.New("capacity inventory unavailable")
	}
	ingressReady := ingressOnGateway(ctx, docker)
	return api.CrossHostInventory{IngressReady: &ingressReady, CpuMillis: info.Info.NCPU * 1000, MemoryMiB: int(info.Info.MemTotal >> 20), UnboundedInventoryHash: hostnetwork.StableInventory(unbounded)}, nil
}
