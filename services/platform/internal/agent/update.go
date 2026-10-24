package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
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

// Install only replaces the agent executable. It never stops a workload or edits Docker.
// The process supervisor restarts it after the current update operation is journaled.
func Install(ctx context.Context, r api.AgentRelease, key ed25519.PublicKey, source io.Reader, executable string) error {
	if r.Arch != api.AgentReleaseArch(runtime.GOARCH) {
		return errors.New("wrong host architecture")
	}
	if e := agentrelease.Validate(r, key); e != nil {
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
	var identity struct {
		Version  string
		Protocol int
		Arch     string
	}
	if json.Unmarshal(output, &identity) != nil || identity.Version != r.Version || identity.Protocol != int(r.Protocol) || identity.Arch != string(r.Arch) {
		return errors.New("artifact identity does not match manifest")
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
func RemoteUpdate(ctx context.Context, c *Client, r api.AgentRelease, executable string) error {
	cfg, e := c.Journal.Config()
	if e != nil {
		return e
	}
	key := ed25519.PublicKey(cfg.PublicKey)
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
	return Install(ctx, r, key, response.Body, executable)
}
