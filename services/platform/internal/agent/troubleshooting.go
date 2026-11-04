package agent

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/containerd/errdefs"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/system"
	"github.com/moby/moby/client"
	bolt "go.etcd.io/bbolt"
)

// ErrTroubleshootingIneligible refuses a container whose shell would effectively be a
// host shell, or one the journaled plan does not expect.
var ErrTroubleshootingIneligible = errors.New("this container is not eligible for a terminal session")

const troubleshootingOptInKey = "troubleshooting-opt-in"

// troubleshootingConsent names the controller the host owner consented to, so that
// any change of authority, however it happens, withdraws consent.
type troubleshootingConsent struct {
	Origin     string
	PublicKey  []byte
	Generation int64
}

func consentFor(cfg Config) troubleshootingConsent {
	return troubleshootingConsent{cfg.Origin, cfg.PublicKey, cfg.ControllerGeneration}
}

// SetTroubleshooting records the host owner's consent for the current controller. It
// needs the exclusive journal lock, held only while the agent daemon is stopped, so
// changing it also ends sessions.
func (j *Journal) SetTroubleshooting(enabled bool) error {
	return j.db.Update(func(tx *bolt.Tx) error {
		if !enabled {
			return tx.Bucket([]byte("identity")).Delete([]byte(troubleshootingOptInKey))
		}
		var cfg Config
		if err := j.get(tx, "identity", "config", &cfg); err != nil {
			return errors.New("enroll this agent before enabling terminal sessions")
		}
		return j.put(tx, "identity", troubleshootingOptInKey, consentFor(cfg))
	})
}

func (j *Journal) TroubleshootingEnabled() (bool, error) {
	var consent troubleshootingConsent
	var cfg Config
	err := j.db.View(func(tx *bolt.Tx) error {
		if err := j.get(tx, "identity", troubleshootingOptInKey, &consent); err != nil {
			return err
		}
		return j.get(tx, "identity", "config", &cfg)
	})
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	current := consentFor(cfg)
	return consent.Origin == current.Origin && bytes.Equal(consent.PublicKey, current.PublicKey) && consent.Generation == current.Generation, nil
}

func (j *Journal) reportTroubleshootingConsent(h *api.AgentHeartbeat) {
	if h.Capabilities.Troubleshooting != nil {
		enabled, err := j.TroubleshootingEnabled()
		enabled = enabled && err == nil
		h.Capabilities.TroubleshootingEnabled = &enabled
	}
}

var runcVersion = regexp.MustCompile(`^(\d+)\.(\d+)\.(\d+)(-\d[0-9A-Za-z.+~]*)?$`)

// troubleshootingRuntime reports whether the daemon's default runtime is a runc with
// the fixes sessions rely on: CVE-2024-21626 and the November 2025 escapes
// (CVE-2025-31133, CVE-2025-52565, CVE-2025-52881), the last of which also hardens
// the terminal allocation an exec uses. It compares each branch's upstream version,
// allowing a distribution's revision suffix, and refuses anything else: another
// runtime, a pre-release, an unparseable version or a major version not yet reviewed.
func troubleshootingRuntime(components []system.ComponentVersion) bool {
	for _, component := range components {
		if component.Name != "runc" {
			continue
		}
		parts := runcVersion.FindStringSubmatch(component.Version)
		if parts == nil || parts[1] != "1" {
			return false
		}
		minor, minorErr := strconv.Atoi(parts[2])
		patch, patchErr := strconv.Atoi(parts[3])
		if minorErr != nil || patchErr != nil {
			return false
		}
		return minor > 3 || minor == 3 && patch >= 3
	}
	return false
}

// TroubleshootingTarget finds the running container a session may enter, from the
// unit's journaled plan and the container's own ownership labels and configuration.
func (e *Engine) TroubleshootingTarget(ctx context.Context, unit, app uuid.UUID, instance int) (string, error) {
	state, err := e.Journal.unit(unit)
	if err != nil {
		return "", err
	}
	instances := state.Plan.InstanceIDs(app)
	if instance < 1 || instance > len(instances) {
		return "", ErrTroubleshootingIneligible
	}
	inspected, err := e.Docker.ContainerInspect(ctx, deployment.UnitName(instances[instance-1]), client.ContainerInspectOptions{})
	if errdefs.IsNotFound(err) {
		return "", ErrTroubleshootingIneligible
	}
	if err != nil {
		return "", err
	}
	if !troubleshootingEligible(inspected.Container, state.Plan, app, e.Journal.dir) {
		return "", ErrTroubleshootingIneligible
	}
	return inspected.Container.ID, nil
}

func troubleshootingEligible(v container.InspectResponse, p deployment.Plan, app uuid.UUID, stateDir string) bool {
	if v.Config == nil || v.State == nil || v.HostConfig == nil || !v.State.Running || !visualOwned(v.Config.Labels, p) || v.Config.Labels[labelResource] != app.String() {
		return false
	}
	h := v.HostConfig
	if h.Privileged || len(h.CapAdd) > 0 || len(h.Devices) > 0 || len(h.DeviceCgroupRules) > 0 || len(h.DeviceRequests) > 0 || len(h.Sysctls) > 0 || h.NetworkMode.IsHost() || h.PidMode.IsHost() || h.IpcMode.IsHost() {
		return false
	}
	switch h.Runtime {
	case "", "runc", "io.containerd.runc.v2":
	default:
		return false
	}
	bundles := filepath.Join(stateDir, "bundles", p.UnitID.String()) + string(filepath.Separator)
	for _, m := range v.Mounts {
		switch m.Type {
		case mount.TypeVolume, mount.TypeTmpfs:
		case mount.TypeBind:
			if m.RW || !strings.HasPrefix(filepath.Clean(m.Source), bundles) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
