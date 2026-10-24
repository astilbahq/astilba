package agent

import (
	"context"
	"github.com/astilbahq/astilba/services/platform/internal/backup"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/moby/moby/client"
)

func Preflight(ctx context.Context, docker *client.Client, version, dir string, cli ...string) api.AgentHeartbeat {
	h := api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Os: runtime.GOOS, Arch: api.AgentCapabilitiesArch(runtime.GOARCH), Protocol: 1, Version: version}, Preflight: api.HostPreflight{Notes: []string{}}}
	credentialFreeCancel := true
	h.Capabilities.CredentialFreeVisualCancel = &credentialFreeCancel
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
	if docker != nil {
		v, e := docker.ServerVersion(ctx, client.ServerVersionOptions{})
		if e == nil {
			h.Capabilities.Docker = true
			h.Preflight.DockerVersion = v.Version
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
	build := h.Preflight.Ready && dockerFreeBytes(ctx, docker) >= 4<<30
	h.Capabilities.Build = &build
	h.Capabilities.BuildControls = &build
	h.Capabilities.BuildCache = &build
	h.Capabilities.Visual = &visual
	h.Capabilities.Compose = &visual
	resticCLI := "restic"
	if len(cli) > 1 {
		resticCLI = cli[1]
	}
	versionCheck, stop := context.WithTimeout(ctx, 2*time.Second)
	restic, resticErr := exec.CommandContext(versionCheck, resticCLI, "version").Output()
	stop()
	backups := visual && resticErr == nil && strings.HasPrefix(string(restic), "restic "+backup.ResticVersion+" ")
	h.Capabilities.Backups = &backups
	h.Capabilities.Upgrades = &backups

	if err != nil {
		h.Preflight.Notes = append(h.Preflight.Notes, "Visual deployment requires Docker Compose 5.6.1.")
	}
	return h
}
