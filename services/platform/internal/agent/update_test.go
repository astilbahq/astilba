package agent

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

func TestAtomicVerifiedReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	old := []byte("old executable")
	if e := os.WriteFile(path, old, 0755); e != nil {
		t.Fatal(e)
	}
	body := fixtureAgent("0.1.0-fixture.2", false)
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(body)
	r := api.AgentRelease{Version: "0.1.0-fixture.2", MinimumVersion: fixtureFloor, Commit: fixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
	corrupt := append([]byte{}, body...)
	corrupt[0] ^= 1
	if Install(context.Background(), r, pub, bytes.NewReader(corrupt), path, permitted) == nil {
		t.Fatal("tampered executable installed")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, old) {
		t.Fatal("failed update changed installed agent")
	}
	if e := Install(context.Background(), r, pub, bytes.NewReader(body), path, permitted); e != nil {
		t.Fatal(e)
	}
	if previous, _ := os.ReadFile(path + ".previous"); !bytes.Equal(previous, old) {
		t.Fatal("previous executable unavailable")
	}
	// Recover an effect-before-ack interruption by recognizing the installed digest.
	if e := Install(context.Background(), r, pub, bytes.NewReader(nil), path, permitted); e != nil {
		t.Fatal(e)
	}
}

// An agent refuses a release whose minimum version is below that of the release it runs, so its
// floor never drops, even one update at a time. It also refuses a release whose binary was built
// from another commit, or with another minimum version, than it was signed with.
func TestInstallKeepsItsFloorAndRefusesMisdescribedReleases(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	release := func(version, floor string, change func(*api.AgentRelease)) (api.AgentRelease, []byte) {
		body := fixtureAgentWithFloor(version, floor, false)
		sum := sha256.Sum256(body)
		r := api.AgentRelease{Version: version, MinimumVersion: floor, Commit: fixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
		change(&r)
		r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
		return r, body
	}
	same := func(*api.AgentRelease) {}
	for _, test := range []struct {
		name           string
		floor          string // The minimum version of the release the agent runs.
		version, below string
		change         func(*api.AgentRelease)
		refusal        string
	}{
		{"older than the floor", "0.3.0", "0.2.9", "0.0.0", same, "below 0.3.0"},
		// Its version is fine, but once installed it would let the agent go back below 0.3.0.
		{"a step down", "0.3.0", "0.3.0", "0.0.0", same, "below 0.3.0"},
		{"another commit", fixtureFloor, "0.3.0", fixtureFloor, func(r *api.AgentRelease) { r.Commit = strings.Repeat("f", 40) }, "identity"},
		{"another minimum version", fixtureFloor, "0.3.0", "0.1.0", func(r *api.AgentRelease) { r.MinimumVersion = "0.2.0" }, "identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			current := []byte("current executable")
			if err := os.WriteFile(path, current, 0755); err != nil {
				t.Fatal(err)
			}
			r, body := release(test.version, test.below, test.change)
			if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, Running{MinimumVersion: test.floor}); err == nil || !strings.Contains(err.Error(), test.refusal) {
				t.Fatalf("Install: %v, want a refusal naming %q", err, test.refusal)
			}
			if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
				t.Fatal("the refused release replaced the agent")
			}
		})
	}
	// Rolling back to an earlier release with the same floor stays possible.
	r, body := release("0.3.0", "0.3.0", same)
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("current executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, Running{MinimumVersion: "0.3.0"}); err != nil {
		t.Fatalf("a release with the same floor: %v", err)
	}
}
