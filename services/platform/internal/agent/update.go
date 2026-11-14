package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

// VerifyInstalledUpdate validates the same signed host artifact as Install,
// without downloading or replacing anything.
func VerifyInstalledUpdate(r api.AgentRelease, key ed25519.PublicKey, executable string) error {
	if r.Arch != api.AgentReleaseArch(runtime.GOARCH) {
		return errors.New("wrong host architecture")
	}
	if err := agentrelease.Validate(r, key); err != nil {
		return err
	}
	current, err := os.Open(executable)
	if err != nil {
		return err
	}
	defer current.Close()
	return agentrelease.Verify(r, key, current)
}

// Running is the agent that installs a release, as far as it bounds what it may install.
type Running struct {
	// Keyed is set once the agent holds a key. It then only installs a release that
	// authenticates with keys: an earlier one would present the bearer its bind retired.
	Keyed bool
	// MinimumVersion is the minimum version of the release this agent runs. It never installs
	// an older release.
	MinimumVersion string
	// StateVersion is the format of the agent's journal. It only installs a release that reads it:
	// an earlier one would refuse to start.
	StateVersion int
}

// Install only replaces the agent executable. It never stops a workload or edits Docker.
// The process supervisor restarts it after the current update operation is journaled.
// Install verifies and installs a signed release that the running agent permits.
func Install(ctx context.Context, r api.AgentRelease, key ed25519.PublicKey, source io.Reader, executable string, running Running) error {
	if r.Arch != api.AgentReleaseArch(runtime.GOARCH) {
		return errors.New("wrong host architecture")
	}
	if e := agentrelease.Validate(r, key); e != nil {
		return e
	}
	if e := agentrelease.Permits(running.MinimumVersion, r); e != nil {
		return e
	}
	current, e := os.Open(executable)
	if e != nil {
		return e
	}
	already := agentrelease.Verify(r, key, current) == nil
	current.Close()
	if already {
		return nil
	}
	dir := filepath.Dir(executable)
	temp, e := os.CreateTemp(dir, ".astilba-agent-next-")
	if e != nil {
		return e
	}
	defer os.Remove(temp.Name())
	defer temp.Close()
	if _, e = io.Copy(temp, io.LimitReader(source, r.Size+1)); e != nil {
		return e
	}
	if _, e = temp.Seek(0, 0); e != nil {
		return e
	}
	if e = agentrelease.Verify(r, key, temp); e != nil {
		return e
	}
	if e = temp.Chmod(0755); e != nil {
		return e
	}
	if e = temp.Sync(); e != nil {
		return e
	}
	if e = temp.Close(); e != nil {
		return e
	}
	// Probe the signed executable to verify its compiled identity before replacement.
	probe := exec.CommandContext(ctx, temp.Name(), "describe")
	output, e := probe.Output()
	if e != nil {
		return errors.New("verified agent cannot run on this host")
	}
	var identity agentrelease.Identity
	if json.Unmarshal(output, &identity) != nil || !identity.Describes(r) {
		return errors.New("artifact identity does not match manifest")
	}
	if running.Keyed && identity.KeyAuth < 1 {
		return errors.New("this agent authenticates with a key, and this release can't; install a release that can")
	}
	// Releases from before the field read every format up to the keyed one, which Keyed covers.
	if running.StateVersion > keyedStateVersion && identity.StateVersion < running.StateVersion {
		return errors.New("this agent's journal is sealed to this machine, and this release can't read it; install a release that can")
	}
	backup := temp.Name() + ".previous"
	if e = os.Link(executable, backup); e != nil {
		return e
	}
	defer os.Remove(backup)
	if e = os.Rename(backup, executable+".previous"); e != nil {
		return e
	}

	if e = os.Rename(temp.Name(), executable); e != nil {
		return e
	}
	parent, e := os.Open(dir)
	if e != nil {
		return e
	}
	defer parent.Close()
	return parent.Sync()
}

// RemoteUpdate downloads and installs a release management sent. minimumVersion is that of the
// release this agent runs.
func RemoteUpdate(ctx context.Context, c *Client, r api.AgentRelease, executable, minimumVersion string) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	key := ed25519.PublicKey(cfg.PublicKey)
	if VerifyInstalledUpdate(r, key, executable) == nil {
		return nil
	}
	if e = agentrelease.Validate(r, key); e != nil {
		return e
	}
	req, e := http.NewRequestWithContext(ctx, "GET", cfg.Origin+"/agent-artifacts/"+r.Sha256, nil)
	if e != nil {
		return e
	}
	response, e := c.HTTP.Do(req)
	if e != nil {
		return e
	}
	defer response.Body.Close()
	if response.StatusCode != 200 {
		return errors.New("artifact unavailable")
	}
	return Install(ctx, r, key, response.Body, executable, Running{Keyed: c.Journal.HoldsKey(cfg), MinimumVersion: minimumVersion, StateVersion: c.Journal.StateVersion()})
}
