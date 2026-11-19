//go:build linux

package hostnetwork_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/agent"
	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// A private-link key sealed with the host's real systemd-creds opens on its own machine, as the
// boot restore opens it, and not with another machine's host key.
func TestNativeNetworkKeySealedToTheMachine(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires an explicitly disposable Ubuntu systemd host")
	}
	ctx := context.Background()
	if err := agent.SetupSystemKeyStore(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := agent.SystemKeyStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	member := uuid.New()
	if _, err = (hostnetwork.Host{Dir: dir, Keys: store}).Identity(ctx, member); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "network", member.String()+".key"))
	if err != nil || !strings.HasPrefix(string(raw), "{") {
		t.Fatal("the private-link key wasn't sealed", err)
	}
	if err = (hostnetwork.Host{Dir: dir, Keys: agent.OnDemandKeyStore(agent.SystemKeyStore)}).OpenKey(ctx, member); err != nil {
		t.Fatal("the sealed key doesn't open on its own machine", err)
	}
	if _, err := os.Stat("/dev/tpmrm0"); err == nil {
		t.Log("this host seals with its TPM, so another host key can't stand for another machine")
		return
	}
	other := filepath.Join(t.TempDir(), "credential.secret")
	if out, err := exec.Command("env", "SYSTEMD_CREDENTIAL_SECRET="+other, "systemd-creds", "setup").CombinedOutput(); err != nil {
		t.Fatalf("could not create another host key: %v %s", err, out)
	}
	t.Setenv("SYSTEMD_CREDENTIAL_SECRET", other)
	// The other machine's store works, so the key is blamed, not the store.
	if err = (hostnetwork.Host{Dir: dir, Keys: agent.OnDemandKeyStore(agent.SystemKeyStore)}).OpenKey(ctx, member); !errors.Is(err, hostnetwork.ErrSealedKey) {
		t.Fatal("a copied private-link key opened with another machine's host key, or failed otherwise:", err)
	}
}
