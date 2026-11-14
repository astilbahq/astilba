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
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agentrelease"
	"github.com/astilbahq/astilba/services/platform/internal/api"
)

func signedRelease(t *testing.T, version string, keyAuth bool) (api.AgentRelease, ed25519.PublicKey, []byte) {
	t.Helper()
	return signedBody(t, version, fixtureAgent(version, keyAuth))
}

func signedBody(t *testing.T, version string, body []byte) (api.AgentRelease, ed25519.PublicKey, []byte) {
	t.Helper()
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(body)
	r := api.AgentRelease{Version: version, MinimumVersion: fixtureFloor, Commit: fixtureCommit, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
	return r, pub, body
}

func TestAgentHoldingAKeyInstallsOnlyAReleaseThatAuthenticatesWithKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	current := []byte("current executable")
	if err := os.WriteFile(path, current, 0755); err != nil {
		t.Fatal(err)
	}
	keyed := Running{Keyed: true, MinimumVersion: fixtureFloor}
	r, pub, body := signedRelease(t, "0.1.0-fixture.2", false)
	if Install(context.Background(), r, pub, bytes.NewReader(body), path, keyed) == nil {
		t.Fatal("an agent holding a key installed a release that would present its retired bearer")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
		t.Fatal("the refused release replaced the agent")
	}
	r, pub, body = signedRelease(t, "0.1.0-fixture.3", true)
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, keyed); err != nil {
		t.Fatal(err)
	}
}

func TestJournalStaysKeyedAfterItsKeyIsCleared(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if j.HoldsKey(Config{}) {
		t.Fatal("a new journal claims a key")
	}
	if err = j.markKeyed(); err != nil {
		t.Fatal(err)
	}
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	if j, err = Open(dir); err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	// A transfer clears the key, but an agent from before keys still can't read this journal.
	if !j.HoldsKey(Config{}) {
		t.Fatal("a keyed journal allowed a release without keys")
	}
}

func TestAgentWithASealedJournalInstallsOnlyAReleaseThatReadsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	current := []byte("current executable")
	if err := os.WriteFile(path, current, 0755); err != nil {
		t.Fatal(err)
	}
	sealed := Running{Keyed: true, MinimumVersion: fixtureFloor, StateVersion: sealedStateVersion}
	for _, earlier := range [][]byte{fixtureAgent("0.1.0-fixture.2", true), fixtureAgentReading("0.1.0-fixture.2", keyedStateVersion)} {
		r, pub, body := signedBody(t, "0.1.0-fixture.2", earlier)
		if Install(context.Background(), r, pub, bytes.NewReader(body), path, sealed) == nil {
			t.Fatal("an agent with a sealed journal installed a release that would refuse to start")
		}
		if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
			t.Fatal("the refused release replaced the agent")
		}
	}
	// A journal still in the clear keeps every keyed release installable.
	r, pub, body := signedRelease(t, "0.1.0-fixture.3", true)
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, Running{Keyed: true, MinimumVersion: fixtureFloor, StateVersion: keyedStateVersion}); err != nil {
		t.Fatal(err)
	}
	r, pub, body = signedBody(t, "0.1.0-fixture.4", fixtureAgentReading("0.1.0-fixture.4", StateVersion))
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, sealed); err != nil {
		t.Fatal(err)
	}
}
