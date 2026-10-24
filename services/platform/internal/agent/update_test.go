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

func TestAtomicVerifiedReplacement(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent")
	old := []byte("old executable")
	if e := os.WriteFile(path, old, 0755); e != nil {
		t.Fatal(e)
	}
	body := []byte("#!/bin/sh\nprintf '%s\\n' '{\"Version\":\"fixture.2\",\"Protocol\":1,\"Arch\":\"" + runtime.GOARCH + "\"}'\n")
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(body)
	r := api.AgentRelease{Version: "fixture.2", Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
	corrupt := append([]byte{}, body...)
	corrupt[0] ^= 1
	if Install(context.Background(), r, pub, bytes.NewReader(corrupt), path) == nil {
		t.Fatal("tampered executable installed")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, old) {
		t.Fatal("failed update changed installed agent")
	}
	if e := Install(context.Background(), r, pub, bytes.NewReader(body), path); e != nil {
		t.Fatal(e)
	}
	if previous, _ := os.ReadFile(path + ".previous"); !bytes.Equal(previous, old) {
		t.Fatal("previous executable unavailable")
	}
	// Recover an effect-before-ack interruption by recognizing the installed digest.
	if e := Install(context.Background(), r, pub, bytes.NewReader(nil), path); e != nil {
		t.Fatal(e)
	}
}
