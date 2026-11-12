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

func signedRelease(t *testing.T, version, describe string) (api.AgentRelease, ed25519.PublicKey, []byte) {
	t.Helper()
	body := []byte("#!/bin/sh\nprintf '%s\\n' '" + describe + "'\n")
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	sum := sha256.Sum256(body)
	r := api.AgentRelease{Version: version, Protocol: 1, Arch: api.AgentReleaseArch(runtime.GOARCH), Size: int64(len(body)), Sha256: hex.EncodeToString(sum[:])}
	r.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(key, agentrelease.Message(r)))
	return r, pub, body
}

func TestAgentHoldingAKeyInstallsOnlyAReleaseThatAuthenticatesWithKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent")
	current := []byte("current executable")
	if err := os.WriteFile(path, current, 0755); err != nil {
		t.Fatal(err)
	}
	r, pub, body := signedRelease(t, "fixture.2", `{"Version":"fixture.2","Protocol":1,"Arch":"`+runtime.GOARCH+`"}`)
	if Install(context.Background(), r, pub, bytes.NewReader(body), path, true) == nil {
		t.Fatal("an agent holding a key installed a release that would present its retired bearer")
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, current) {
		t.Fatal("the refused release replaced the agent")
	}
	r, pub, body = signedRelease(t, "fixture.3", `{"Version":"fixture.3","Protocol":1,"Arch":"`+runtime.GOARCH+`","KeyAuth":1}`)
	if err := Install(context.Background(), r, pub, bytes.NewReader(body), path, true); err != nil {
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
