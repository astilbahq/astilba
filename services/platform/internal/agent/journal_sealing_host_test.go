package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The journal's keyring sealed with the host's real systemd-creds: upgraded from a keyring in the
// clear, rekeyed, cut short mid-rekey by a process crash, and copied to a machine with another
// host key.
func TestNativeJournalSealedToTheMachine(t *testing.T) {
	if testing.Short() || os.Getenv("ASTILBA_NATIVE_AGENT") != "1" {
		t.Skip("requires an explicitly disposable Ubuntu systemd host")
	}
	ctx := context.Background()
	if err := SetupSystemKeyStore(ctx); err != nil {
		t.Fatal(err)
	}
	store, err := SystemKeyStore(ctx)
	if err != nil {
		t.Fatal(err)
	}
	dir := privateDir(t)
	// The agent before this one leaves its keyring in the clear.
	j, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetConfig(Config{Credential: sealingCanary}); err != nil {
		t.Fatal(err)
	}
	if err = j.SetConfig(Config{Credential: "replaced"}); err != nil {
		t.Fatal(err)
	}
	if err = j.markKeyed(); err != nil {
		t.Fatal(err)
	}
	clear := j.keys.Load()
	j.Close()

	j, err = OpenSealed(ctx, dir, SystemKeyStore)
	if err != nil {
		t.Fatal(err)
	}
	// The service seals it when it rekeys with its store.
	if err = j.Rekey(ctx, store); err != nil {
		t.Fatal(err)
	}
	if state := readLocalState(t, dir); state.Version != sealedStateVersion || state.Keyring != nil {
		t.Fatal("upgrading didn't seal the keyring with systemd-creds", state.Version)
	}
	if j.KeyStore().Protection() != store.Protection() {
		t.Fatal("the journal is sealed with another protection than the agent key")
	}
	if openableCanaries(t, dir, clear) != 0 || openableCanaries(t, dir, j.keys.Load()) != 0 {
		t.Fatal("a key opens a version the rekey replaced")
	}
	j.Close()

	for _, point := range rekeyPoints {
		cmd := exec.Command(os.Args[0], "-test.run=^TestRekeyCrashHelper$")
		cmd.Env = append(os.Environ(), "ASTILBA_TEST_REKEY_DIR="+dir, "ASTILBA_TEST_REKEY_POINT="+point, "ASTILBA_TEST_REKEY_SEALED=1")
		if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 87 {
			t.Fatalf("expected the rekey to crash at %s: %v %s", point, err, out)
		}
		j, err = OpenSealed(ctx, dir, SystemKeyStore)
		if err != nil {
			t.Fatalf("a rekey cut short at %s left a journal that can't open: %v", point, err)
		}
		if cfg, err := j.Config(); err != nil || cfg.Credential != "replaced" {
			t.Fatalf("a rekey cut short at %s lost a record: %v", point, err)
		}
		if err = j.Rekey(ctx, nil); err != nil {
			t.Fatal(err)
		}
		if len(j.keys.Load().Keys) != 1 {
			t.Fatalf("the rekey after one cut short at %s kept a replaced key", point)
		}
		j.Close()
	}

	if _, err := os.Stat("/dev/tpmrm0"); err == nil {
		t.Log("this host seals with its TPM, so another host key can't stand for another machine")
		return
	}
	// Another machine is one with another systemd host key.
	other := filepath.Join(privateDir(t), "credential.secret")
	if out, err := exec.Command("env", "SYSTEMD_CREDENTIAL_SECRET="+other, "systemd-creds", "setup").CombinedOutput(); err != nil {
		t.Fatalf("could not create another host key: %v %s", err, out)
	}
	copied := copyStateDirectory(t, dir)
	t.Setenv("SYSTEMD_CREDENTIAL_SECRET", other)
	// The other machine's store works; it just isn't the one the keyring was sealed to.
	if _, err := SystemKeyStore(ctx); err != nil {
		t.Fatal("the other host key's store doesn't open", err)
	}
	if copy, err := OpenSealed(ctx, copied, SystemKeyStore); err == nil {
		copy.Close()
		t.Fatal("a copied state directory opened with another machine's host key")
	} else if !errors.Is(err, ErrSealedJournal) || !strings.Contains(err.Error(), "can't read it") {
		t.Fatal("a copied state directory failed for another reason:", err)
	}
}
