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

// An agent refuses a release older than the minimum version of the one it runs, and a release
// whose binary was built from another commit, or with another minimum version, than it was
// signed with.
func TestInstallRefusesRollbackBelowItsFloorAndMisdescribedReleases(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	release := func(version string, change func(*api.AgentRelease)) (api.AgentRelease, []byte) {
		body := fixtureAgent(version, false)
		sum := sha256.Sum256(body)
		r := api.AgentRelease{Version: version, MinimumVersion: fixtureFloor, Commit: fixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
		change(&r)
		r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
		return r, body
	}
	for _, test := range []struct {
		name    string
		running Running
		version string
		change  func(*api.AgentRelease)
		refusal string
	}{
		{"below the floor", Running{MinimumVersion: "0.3.0"}, "0.2.9", func(*api.AgentRelease) {}, "older than 0.3.0"},
		{"a pre-release of the floor", Running{MinimumVersion: "0.3.0"}, "0.3.0-rc.1", func(*api.AgentRelease) {}, "older than 0.3.0"},
		{"another commit", permitted, "0.3.0", func(r *api.AgentRelease) { r.Commit = strings.Repeat("f", 40) }, "identity"},
		{"another minimum version", permitted, "0.3.0", func(r *api.AgentRelease) { r.MinimumVersion = "0.1.0" }, "identity"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "agent")
			current := []byte("current executable")
			if err := os.WriteFile(path, current, 0755); err != nil {
				t.Fatal(err)
			}
			r, body := release(test.version, test.change)
			if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, test.running); err == nil || !strings.Contains(err.Error(), test.refusal) {
				t.Fatalf("Install: %v, want a refusal naming %q", err, test.refusal)
			}
			if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
				t.Fatal("the refused release replaced the agent")
			}
		})
	}
	r, body := release("0.3.0", func(*api.AgentRelease) {})
	path := filepath.Join(t.TempDir(), "agent")
	if err := os.WriteFile(path, []byte("current executable"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, Running{MinimumVersion: "0.3.0"}); err != nil {
		t.Fatalf("a release at the floor: %v", err)
	}
}
