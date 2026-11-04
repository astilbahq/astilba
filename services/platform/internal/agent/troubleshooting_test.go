package agent

import (
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/api"
	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/system"
)

func TestTroubleshootingRuntimeFloor(t *testing.T) {
	for version, want := range map[string]bool{
		"1.1.12":                   false,
		"1.1.12-0ubuntu3":          false, // Ubuntu 24.04's release pocket, without the 2025 fixes
		"1.2.9":                    false, // has the fixes, but the branch is unsupported
		"1.3.2":                    false,
		"1.3.3":                    true,
		"1.3.3-0ubuntu1~24.04.3":   true,
		"1.3.4-0ubuntu1~24.04.1":   true,
		"1.4.0-rc.2":               false,
		"1.4.0":                    true,
		"1.5.0-rc.3":               false,
		"1.5.2":                    true,
		"1.6.0-rc.1":               false,
		"1.10.0":                   true,
		"2.0.0":                    false,
		"":                         false,
		"unknown":                  false,
		"1.5":                      false,
		"1.5.2\n":                  false,
		"99999999999999999999.0.0": false,
	} {
		if got := troubleshootingRuntime([]system.ComponentVersion{{Name: "Engine", Version: "1.5.2"}, {Name: "runc", Version: version}}); got != want {
			t.Errorf("runc %q: got %v", version, got)
		}
	}
	// The component's name is the default runtime, and a shim runtime reports none.
	if troubleshootingRuntime([]system.ComponentVersion{{Name: "crun", Version: "1.5.2"}}) || troubleshootingRuntime(nil) {
		t.Fatal("a runtime other than runc must not pass")
	}
}

func TestTroubleshootingEligibility(t *testing.T) {
	plan := deployment.Plan{ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New()}
	app := uuid.New()
	state := "/var/lib/astilba-agent"
	eligible := func() container.InspectResponse {
		return container.InspectResponse{ID: "container", State: &container.State{Running: true}, Config: &container.Config{Labels: visualLabels(plan, app)}, HostConfig: &container.HostConfig{Runtime: "runc"}, Mounts: []container.MountPoint{{Type: mount.TypeVolume, RW: true}, {Type: mount.TypeTmpfs, RW: true}, {Type: mount.TypeBind, Source: state + "/bundles/" + plan.UnitID.String() + "/" + uuid.NewString() + "/files/config.yaml"}}}
	}
	if !troubleshootingEligible(eligible(), plan, app, state) {
		t.Fatal("managed container refused")
	}
	for name, change := range map[string]func(*container.InspectResponse){
		"stopped":          func(v *container.InspectResponse) { v.State.Running = false },
		"another resource": func(v *container.InspectResponse) { v.Config.Labels[labelResource] = uuid.NewString() },
		"another unit":     func(v *container.InspectResponse) { v.Config.Labels["com.astilba.unit"] = uuid.NewString() },
		"another kind":     func(v *container.InspectResponse) { v.Config.Labels[labelKind] = "pipeline-v1" },
		"privileged":       func(v *container.InspectResponse) { v.HostConfig.Privileged = true },
		"capabilities":     func(v *container.InspectResponse) { v.HostConfig.CapAdd = []string{"SYS_ADMIN"} },
		"devices": func(v *container.InspectResponse) {
			v.HostConfig.Devices = []container.DeviceMapping{{PathOnHost: "/dev/sda"}}
		},
		"device rules": func(v *container.InspectResponse) { v.HostConfig.DeviceCgroupRules = []string{"b 8:* rmw"} },
		"device requests": func(v *container.InspectResponse) {
			v.HostConfig.DeviceRequests = []container.DeviceRequest{{Driver: "cdi"}}
		},
		"kernel parameters": func(v *container.InspectResponse) {
			v.HostConfig.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"}
		},
		"host network":    func(v *container.InspectResponse) { v.HostConfig.NetworkMode = "host" },
		"host processes":  func(v *container.InspectResponse) { v.HostConfig.PidMode = "host" },
		"host IPC":        func(v *container.InspectResponse) { v.HostConfig.IpcMode = "host" },
		"another runtime": func(v *container.InspectResponse) { v.HostConfig.Runtime = "runsc" },
		"docker socket": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: "/var/run/docker.sock"})
		},
		"agent state": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state + "/journal.db"})
		},
		"writable bundle": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state + "/bundles/" + plan.UnitID.String() + "/file", RW: true})
		},
		"another unit's bundle": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state + "/bundles/" + uuid.NewString() + "/file"})
		},
		"path escaping state": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state + "/bundles/" + plan.UnitID.String() + "/../../../../../etc"})
		},
		"sibling of state": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state + "-other/file"})
		},
		"the state directory": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeBind, Source: state})
		},
		"unknown mount type": func(v *container.InspectResponse) {
			v.Mounts = append(v.Mounts, container.MountPoint{Type: mount.TypeCluster})
		},
	} {
		v := eligible()
		change(&v)
		if troubleshootingEligible(v, plan, app, state) {
			t.Errorf("%s: container accepted", name)
		}
	}
}

// Consent belongs to one controller: the host owner chooses again for any other.
func TestTroubleshootingConsentNamesItsController(t *testing.T) {
	key, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	other, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	j, _ := testVisualJournal(t)
	base := Config{Origin: "https://old.example.test", Enrolled: true, PublicKey: key, MachineID: uuid.New()}
	if err = j.SetConfig(base); err != nil {
		t.Fatal(err)
	}
	reported := func() *bool {
		h := api.AgentHeartbeat{Capabilities: api.AgentCapabilities{Troubleshooting: &[]bool{true}[0]}}
		j.reportTroubleshootingConsent(&h)
		return h.Capabilities.TroubleshootingEnabled
	}
	if v := reported(); v == nil || *v {
		t.Fatal("hosts must default to no consent")
	}
	if err = j.SetTroubleshooting(true); err != nil {
		t.Fatal(err)
	}
	if v := reported(); v == nil || !*v {
		t.Fatal("consent not reported")
	}
	for name, change := range map[string]func(*Config){
		"origin":     func(c *Config) { c.Origin = "https://new.example.test" },
		"key":        func(c *Config) { c.PublicKey = other },
		"generation": func(c *Config) { c.ControllerGeneration++ },
	} {
		changed := base
		change(&changed)
		if err = j.SetConfig(changed); err != nil {
			t.Fatal(err)
		}
		if enabled, err := j.TroubleshootingEnabled(); err != nil || enabled {
			t.Fatalf("%s change kept consent: %v", name, err)
		}
	}
	if err = j.SetConfig(base); err != nil {
		t.Fatal(err)
	}
	if err = j.Transfer("https://new.example.test", key, 1, strings.Repeat("t", 43)); err != nil {
		t.Fatal(err)
	}
	if enabled, err := j.TroubleshootingEnabled(); err != nil || enabled {
		t.Fatal("transfer kept consent for the new controller", enabled, err)
	}
	if err = j.SetTroubleshooting(true); err != nil {
		t.Fatal(err)
	}
	if err = j.SetTroubleshooting(false); err != nil {
		t.Fatal(err)
	}
	if enabled, err := j.TroubleshootingEnabled(); err != nil || enabled {
		t.Fatal("opt-out kept consent", enabled, err)
	}
}

// Older controllers reject unknown capability fields.
func TestTroubleshootingCapabilitiesNeedControllerSupport(t *testing.T) {
	yes := true
	unavailable := api.Runtime
	capabilities := api.AgentCapabilities{Troubleshooting: &yes, TroubleshootingEnabled: &yes, TroubleshootingUnavailable: &unavailable}
	c := &Client{}
	if v := c.advertisedCapabilities(capabilities); v.Troubleshooting != nil || v.TroubleshootingEnabled != nil || v.TroubleshootingUnavailable != nil {
		t.Fatal("advertised to a controller that does not support sessions")
	}
	c.troubleshooting.Store(true)
	if v := c.advertisedCapabilities(capabilities); v.Troubleshooting == nil || v.TroubleshootingEnabled == nil || v.TroubleshootingUnavailable == nil {
		t.Fatal("withheld from a controller that supports sessions")
	}
}
