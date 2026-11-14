package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
)

func TestRekeyCrashHelper(t *testing.T) {
	dir, point := os.Getenv("ASTILBA_TEST_REKEY_DIR"), os.Getenv("ASTILBA_TEST_REKEY_POINT")
	if dir == "" {
		return
	}
	open := Open
	if os.Getenv("ASTILBA_TEST_REKEY_SEALED") == "1" {
		open = func(dir string) (*Journal, error) { return OpenSealed(context.Background(), dir, SystemKeyStore) }
	}
	j, err := open(dir)
	if err != nil {
		t.Fatal(err)
	}
	err = j.rekey(context.Background(), nil, func(at string) error {
		if at == point {
			os.Exit(87)
		}
		return nil
	})
	t.Fatalf("the rekey did not crash at %s: %v", point, err)
}

// rekeyPoints are where a rekey can be cut short, in order.
var rekeyPoints = []string{"local-state-written", "local-state-renamed", "rekey-key-added", "rekey-resealed", "rekey-keys-dropped"}

func TestInterruptedRekeyIsFinishedByTheNext(t *testing.T) {
	for _, point := range rekeyPoints {
		t.Run(point, func(t *testing.T) {
			j, _ := testVisualJournal(t)
			if err := j.SetConfig(Config{Credential: sealingCanary}); err != nil {
				t.Fatal(err)
			}
			if err := j.SetConfig(Config{Credential: "replaced"}); err != nil {
				t.Fatal(err)
			}
			held := j.keys.Load()
			if err := j.Close(); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(os.Args[0], "-test.run=^TestRekeyCrashHelper$")
			cmd.Env = append(os.Environ(), "ASTILBA_TEST_REKEY_DIR="+j.dir, "ASTILBA_TEST_REKEY_POINT="+point)
			if out, err := cmd.CombinedOutput(); err == nil || cmd.ProcessState.ExitCode() != 87 {
				t.Fatalf("expected the rekey to crash: %v %s", err, out)
			}
			// Whatever the crash left opens every record.
			j, err := Open(j.dir)
			if err != nil {
				t.Fatal("an interrupted rekey left a journal that can't open", err)
			}
			defer j.Close()
			if cfg, err := j.Config(); err != nil || cfg.Credential != "replaced" {
				t.Fatal("an interrupted rekey lost a record", err)
			}
			if !rowsOpen(t, j, j.keys.Load()) {
				t.Fatal("an interrupted rekey left a record its keys can't open")
			}
			if err = j.Rekey(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			keys := j.keys.Load()
			if len(keys.Keys) != 1 || keys.Keys[held.Active] != nil || openableCanaries(t, j.dir, keys) != 0 {
				t.Fatal("the next rekey left a replaced key")
			}
			if state := readLocalState(t, j.dir); state.Version != localStateVersion {
				t.Fatal("a rekey changed the journal's format", state.Version)
			}
		})
	}
}

func TestInterruptedSealedRekeyIsFinishedByTheNext(t *testing.T) {
	store := InsecureKeyStore()
	for _, point := range rekeyPoints {
		t.Run(point, func(t *testing.T) {
			dir := privateDir(t)
			j, err := OpenSealed(context.Background(), dir, storeOpener(store))
			if err != nil {
				t.Fatal(err)
			}
			if err = j.SetConfig(Config{Credential: sealingCanary}); err != nil {
				t.Fatal(err)
			}
			if err = j.SetConfig(Config{Credential: "replaced"}); err != nil {
				t.Fatal(err)
			}
			held := j.keys.Load()
			cut := errors.New("cut short")
			if err = j.rekey(context.Background(), nil, func(at string) error {
				if at == point {
					return cut
				}
				return nil
			}); !errors.Is(err, cut) {
				t.Fatal("the rekey wasn't cut short", err)
			}
			j.Close()
			j, err = OpenSealed(context.Background(), dir, storeOpener(store))
			if err != nil {
				t.Fatal("an interrupted rekey left a journal that can't open", err)
			}
			defer j.Close()
			if cfg, err := j.Config(); err != nil || cfg.Credential != "replaced" {
				t.Fatal("an interrupted rekey lost a record", err)
			}
			if err = j.Rekey(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			keys := j.keys.Load()
			if len(keys.Keys) != 1 || keys.Keys[held.Active] != nil || openableCanaries(t, dir, keys) != 0 {
				t.Fatal("the next rekey left a replaced key")
			}
			if state := readLocalState(t, dir); state.Version != sealedStateVersion || state.Keyring != nil {
				t.Fatal("a rekey unsealed the keyring")
			}
		})
	}
}

func TestRekeyThatFailsAfterSealingLeavesTheJournalSealed(t *testing.T) {
	store := InsecureKeyStore()
	j, err := OpenSealed(context.Background(), privateDir(t), storeOpener(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.Rekey(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// Syncing the directory fails once the sealed keyring has replaced the old one.
	synced := errors.New("directory sync failed")
	if err = j.rekey(context.Background(), store, func(at string) error {
		if at == "local-state-renamed" {
			return synced
		}
		return nil
	}); !errors.Is(err, synced) {
		t.Fatal("the rekey didn't fail", err)
	}
	if readLocalState(t, j.dir).Version != sealedStateVersion {
		t.Fatal("the test proves nothing: the keyring isn't sealed on disk")
	}
	if j.KeyStore() != store || j.StateVersion() != sealedStateVersion {
		t.Fatal("the journal forgot that its keyring is sealed on disk")
	}
	if err = j.Rekey(context.Background(), nil); err != nil || readLocalState(t, j.dir).Version != sealedStateVersion {
		t.Fatal("the next rekey wrote the keyring in the clear", err)
	}
}

func TestFullKeyringStillLeavesOnlyANewKey(t *testing.T) {
	j, _ := testVisualJournal(t)
	if err := j.SetConfig(Config{Credential: sealingCanary}); err != nil {
		t.Fatal(err)
	}
	if err := j.SetConfig(Config{Credential: "replaced"}); err != nil {
		t.Fatal(err)
	}
	// Rekeys cut short after adding their key fill the keyring.
	for len(j.keys.Load().Keys) < maxJournalKeys {
		cut := errors.New("cut short")
		if err := j.rekey(context.Background(), nil, func(at string) error {
			if at == "rekey-key-added" {
				return cut
			}
			return nil
		}); !errors.Is(err, cut) {
			t.Fatal(err)
		}
	}
	full := j.keys.Load()
	if err := j.Rekey(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	keys := j.keys.Load()
	if len(keys.Keys) != 1 || full.Keys[keys.Active] != nil {
		t.Fatal("a full keyring kept a key it held before the rekey")
	}
	if openableCanaries(t, j.dir, keys) != 0 || !rowsOpen(t, j, keys) {
		t.Fatal("the rekey of a full keyring left versions open or records closed")
	}
}

func TestRekeyStoppedWhileReducingAFullKeyringLeavesItReadable(t *testing.T) {
	j, _ := testVisualJournal(t)
	if err := j.SetConfig(Config{Credential: "kept"}); err != nil {
		t.Fatal(err)
	}
	for len(j.keys.Load().Keys) < maxJournalKeys {
		cut := errors.New("cut short")
		if err := j.rekey(context.Background(), nil, func(at string) error {
			if at == "rekey-key-added" {
				return cut
			}
			return nil
		}); !errors.Is(err, cut) {
			t.Fatal(err)
		}
	}
	// The service stops while a read is open, as the full keyring is reduced to its active key.
	j.db.reads.Add(1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := j.Rekey(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("a rekey stopped part way reported success", err)
	}
	j.db.reads.Add(-1)
	if len(j.keys.Load().Keys) > maxJournalKeys {
		t.Fatal("the stopped rekey grew the keyring past what can be read")
	}
	j.Close()
	j, err := Open(j.dir)
	if err != nil {
		t.Fatal("a rekey stopped part way left a keyring that can't be read", err)
	}
	defer j.Close()
	if cfg, err := j.Config(); err != nil || cfg.Credential != "kept" {
		t.Fatal("a rekey stopped part way lost a record", err)
	}
}
