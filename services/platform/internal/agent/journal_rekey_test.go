package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	bolt "go.etcd.io/bbolt"
)

// sealingCanary stands for a value the journal held and has since replaced.
const sealingCanary = "journal-sealing-canary-4c1e"

func privateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// storeOpener opens store, or fails as an unavailable key store does when store is nil.
func storeOpener(store KeyStore) func(context.Context) (KeyStore, error) {
	return func(context.Context) (KeyStore, error) {
		if store == nil {
			return nil, errors.New("the key store is unavailable")
		}
		return store, nil
	}
}

func readLocalState(t *testing.T, dir string) localStateKeys {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "keyring.json"))
	if err != nil {
		t.Fatal(err)
	}
	var envelope localStateKeys
	if err = json.Unmarshal(raw, &envelope, json.RejectUnknownMembers(true)); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func copyStateDirectory(t *testing.T, from string) string {
	t.Helper()
	to := privateDir(t)
	for _, name := range []string{"journal.db", "keyring.json"} {
		src, err := os.Open(filepath.Join(from, name))
		if err != nil {
			t.Fatal(err)
		}
		dst, err := os.OpenFile(filepath.Join(to, name), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		src.Close()
		dst.Close()
	}
	return to
}

// sealedVersions finds every sealed value in the journal file's bytes, live or in a freed page.
var sealedVersions = regexp.MustCompile(`\{"version":1,"keyId":"[A-Za-z0-9_-]+","key":"[A-Za-z0-9+/=]+","data":"[A-Za-z0-9+/=]+"\}`)

// openableCanaries counts the sealed versions of the journal's configuration, anywhere in its file,
// that keys open and that hold the canary.
func openableCanaries(t *testing.T, dir string, keys *cryptobox.Keyring) int {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "journal.db"))
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, blob := range sealedVersions.FindAll(raw, -1) {
		if plain, err := keys.Open(blob, "identity:config"); err == nil && bytes.Contains(plain, []byte(sealingCanary)) {
			found++
		}
	}
	return found
}

// openRows is every sealed record of an open journal, by identity.
func openRows(t *testing.T, j *Journal) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	if err := j.db.View(func(tx *bolt.Tx) error {
		return tx.ForEach(func(name []byte, b *bolt.Bucket) error {
			return b.ForEach(func(k, v []byte) error {
				out[string(name)+":"+string(k)] = bytes.Clone(v)
				return nil
			})
		})
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

// rowsOpen reports whether keys open every record of an open journal.
func rowsOpen(t *testing.T, j *Journal, keys *cryptobox.Keyring) bool {
	t.Helper()
	for k, v := range openRows(t, j) {
		if _, err := keys.Open(v, k); err != nil {
			return false
		}
	}
	return true
}

func TestCopiedStateDirectoryOpensNothingOnAnotherMachine(t *testing.T) {
	here, elsewhere := InsecureKeyStore(), InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(context.Background(), dir, storeOpener(here))
	if err != nil {
		t.Fatal(err)
	}
	if err = j.SetConfig(Config{Credential: sealingCanary}); err != nil {
		t.Fatal(err)
	}
	keys := j.keys.Load()
	if err = j.Close(); err != nil {
		t.Fatal(err)
	}
	// A new journal is sealed from the start, and keyring.json holds no key in the clear.
	if state := readLocalState(t, dir); state.Version != sealedStateVersion || state.Keyring != nil || len(state.SealedKeyring) == 0 {
		t.Fatalf("a new journal's keyring isn't sealed: %+v", state)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "keyring.json"))
	for _, key := range keys.Keys {
		if bytes.Contains(raw, key) || strings.Contains(string(raw), base64.StdEncoding.EncodeToString(key)) {
			t.Fatal("keyring.json holds a journal key in the clear")
		}
	}

	copied := copyStateDirectory(t, dir)
	before, _ := os.ReadFile(filepath.Join(copied, "journal.db"))
	for name, open := range map[string]func() (*Journal, error){
		"another machine's store": func() (*Journal, error) { return OpenSealed(context.Background(), copied, storeOpener(elsewhere)) },
		"no store":                func() (*Journal, error) { return Open(copied) },
		"an unavailable store":    func() (*Journal, error) { return OpenSealed(context.Background(), copied, storeOpener(nil)) },
	} {
		if opened, err := open(); err == nil {
			opened.Close()
			t.Fatalf("a copied state directory opened with %s", name)
		} else if !errors.Is(err, ErrSealedJournal) {
			t.Fatalf("a copied state directory with %s failed for another reason: %v", name, err)
		}
	}
	if after, _ := os.ReadFile(filepath.Join(copied, "journal.db")); !bytes.Equal(before, after) {
		t.Fatal("refusing a copied state directory wrote to its journal")
	}

	// On the machine it was sealed on, the same directory opens.
	j, err = OpenSealed(context.Background(), copied, storeOpener(here))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if cfg, err := j.Config(); err != nil || cfg.Credential != sealingCanary {
		t.Fatal("the machine that sealed the journal can't read it", err)
	}
}

func TestJournalInTheClearIsSealedOnUpgrade(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		dir, legacy := legacyJournal(t, func(j *Journal) {
			if err := j.SetConfig(Config{Credential: sealingCanary}); err != nil {
				t.Fatal(err)
			}
		})
		// The agent before this one left a versioned envelope, keyed or not.
		j, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if keyed {
			if err = j.markKeyed(); err != nil {
				t.Fatal(err)
			}
		}
		j.Close()
		before := encryptedJournalRows(t, dir)

		// The key store can't seal yet, so the keyring stays as it was, and the agent runs.
		j, err = OpenSealed(context.Background(), dir, storeOpener(nil))
		if err != nil {
			t.Fatal(err)
		}
		if j.StateVersion() == sealedStateVersion || j.KeyStore() != nil {
			t.Fatal("a journal was sealed without a key store")
		}
		j.Close()
		if state := readLocalState(t, dir); state.Version == sealedStateVersion {
			t.Fatal("an unavailable store sealed the keyring")
		}

		store := InsecureKeyStore()
		j, err = OpenSealed(context.Background(), dir, storeOpener(store))
		if err != nil {
			t.Fatal(err)
		}
		if j.StateVersion() != sealedStateVersion || !j.HoldsKey(Config{}) {
			t.Fatal("upgrading didn't seal the journal or mark it keyed")
		}
		legacyKeys, _ := cryptobox.Parse(legacy)
		if !rowsOpen(t, j, legacyKeys) || !unchangedRows(before, openRows(t, j)) {
			t.Fatal("sealing the keyring changed records")
		}
		if cfg, err := j.Config(); err != nil || cfg.Credential != sealingCanary {
			t.Fatal("the sealed journal lost a record", err)
		}
		// The service rekeys once it runs: the key that sat in the clear opens nothing after.
		if err = j.Rekey(context.Background(), store); err != nil {
			t.Fatal(err)
		}
		j.Close()
		for k, v := range encryptedJournalRows(t, dir) {
			if _, err := legacyKeys.Open(v, k); err == nil {
				t.Fatal("the key the journal held in the clear still opens a record:", k)
			}
		}
		if openableCanaries(t, dir, legacyKeys) != 0 {
			t.Fatal("the key the journal held in the clear still opens an earlier version")
		}
		j, err = OpenSealed(context.Background(), dir, storeOpener(store))
		if err != nil {
			t.Fatal(err)
		}
		if cfg, err := j.Config(); err != nil || cfg.Credential != sealingCanary {
			t.Fatal("the rekeyed journal lost a record", err)
		}
		j.Close()
	}
}

func TestRekeyLeavesNoKeyForFreedPages(t *testing.T) {
	store := InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(context.Background(), dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	if err = j.SetConfig(Config{Credential: sealingCanary}); err != nil {
		t.Fatal(err)
	}
	if err = j.SetConfig(Config{Credential: "replaced"}); err != nil {
		t.Fatal(err)
	}
	// The premise: the journal file still holds the replaced version, and the key opens it.
	held := j.keys.Load()
	if openableCanaries(t, dir, held) == 0 {
		t.Fatal("the journal file no longer holds the replaced version; the test proves nothing")
	}
	if err = j.Rekey(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	keys := j.keys.Load()
	if len(keys.Keys) != 1 || keys.Active == held.Active {
		t.Fatalf("the rekey kept a replaced key: %v", keys.Active)
	}
	if openableCanaries(t, dir, keys) != 0 {
		t.Fatal("a key the agent holds still opens the replaced version")
	}
	if !rowsOpen(t, j, keys) {
		t.Fatal("the rekey left a record the new key can't open")
	}
	if cfg, err := j.Config(); err != nil || cfg.Credential != "replaced" {
		t.Fatal("the rekey lost a record", err)
	}
	if j.changedSinceRekey() {
		t.Fatal("the rekey counts as a change")
	}
	if err = j.SetConfig(Config{Credential: "later"}); err != nil {
		t.Fatal(err)
	}
	if !j.changedSinceRekey() {
		t.Fatal("a later write doesn't count as a change")
	}
}

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

func TestRekeyKeepsReplacedKeysForReadsAlreadyOpen(t *testing.T) {
	j, _ := testVisualJournal(t)
	if err := j.SetConfig(Config{Credential: sealingCanary}); err != nil {
		t.Fatal(err)
	}
	// The journal file only remaps to grow while no read is open, so a read held across the rekey,
	// as here, would wait for it forever. Free space first, so that the rekey's write fits.
	if err := j.db.Update(func(tx *bolt.Tx) error {
		b, err := tx.CreateBucket([]byte("scratch"))
		if err != nil {
			return err
		}
		for i := range 64 {
			if err = b.Put([]byte{byte(i)}, make([]byte, 64<<10)); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := j.db.Update(func(tx *bolt.Tx) error { return tx.DeleteBucket([]byte("scratch")) }); err != nil {
		t.Fatal(err)
	}
	started, dropped, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- j.db.View(func(tx *bolt.Tx) error {
			close(started)
			<-dropped
			// This read began before the re-sealing, so it sees records under the replaced key.
			var cfg Config
			if err := j.get(tx, "identity", "config", &cfg); err != nil {
				return err
			}
			if cfg.Credential != sealingCanary {
				return errors.New("the read saw another configuration")
			}
			return nil
		})
	}()
	<-started
	rekeyed := make(chan error, 1)
	go func() {
		rekeyed <- j.rekey(context.Background(), nil, func(at string) error {
			if at == "rekey-keys-dropped" {
				close(dropped)
			}
			return nil
		})
	}()
	if err := <-finished; err != nil {
		t.Fatal("a read already open lost a key it needed:", err)
	}
	if err := <-rekeyed; err != nil {
		t.Fatal(err)
	}
	if len(j.keys.Load().Keys) != 1 {
		t.Fatal("the journal kept a replaced key once no read needed it")
	}
}

func TestAgentRekeysItsJournalAndSealsItOnceTheStoreOpens(t *testing.T) {
	period, check := journalRekeyInterval, journalRekeyCheck
	journalRekeyInterval, journalRekeyCheck = 50*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { journalRekeyInterval, journalRekeyCheck = period, check })
	dir := privateDir(t)
	j, err := OpenSealed(context.Background(), dir, storeOpener(nil))
	if err != nil {
		t.Fatal(err)
	}
	defer j.Close()
	initial := j.keys.Load().Active
	c := NewClient(j, nil)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { c.keepJournalRekeyed(ctx); close(stopped) }()
	defer func() { cancel(); <-stopped }()
	eventually := func(what string, done func() bool) {
		t.Helper()
		for deadline := time.Now().Add(10 * time.Second); !done(); {
			if time.Now().After(deadline) {
				t.Fatal(what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	eventually("the agent didn't rekey its journal when it started", func() bool { return j.keys.Load().Active != initial })
	if j.StateVersion() == sealedStateVersion {
		t.Fatal("the journal was sealed without a key store")
	}
	store := InsecureKeyStore()
	c.keysMu.Lock()
	c.Keys = store
	c.keysMu.Unlock()
	eventually("the journal wasn't sealed once the key store opened", func() bool { return j.KeyStore() == store })
	if state := readLocalState(t, dir); state.Version != sealedStateVersion {
		t.Fatal("keyring.json wasn't sealed", state.Version)
	}
	// A changed journal is rekeyed again; an unchanged one isn't.
	sealedKey := j.keys.Load().Active
	time.Sleep(4 * journalRekeyInterval)
	if j.keys.Load().Active != sealedKey {
		t.Fatal("an unchanged journal was rekeyed")
	}
	if err = j.SetConfig(Config{Credential: "changed"}); err != nil {
		t.Fatal(err)
	}
	eventually("a changed journal wasn't rekeyed", func() bool { return j.keys.Load().Active != sealedKey })
}

func TestJournalOpenedWithoutSealingNeverSeals(t *testing.T) {
	j, _ := testVisualJournal(t)
	if err := j.Rekey(context.Background(), InsecureKeyStore()); err != nil {
		t.Fatal(err)
	}
	if j.KeyStore() != nil || j.StateVersion() == sealedStateVersion {
		t.Fatal("a journal opened for tests was sealed")
	}
}

func TestSealedValuesCannotStandInForEachOther(t *testing.T) {
	store := InsecureKeyStore()
	sealed, err := store.Seal(context.Background(), credentialName, []byte("agent key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Unseal(context.Background(), journalCredentialName, sealed); err == nil {
		t.Fatal("a sealed agent key opened as the journal keyring")
	}
	dir := privateDir(t)
	j, err := OpenSealed(context.Background(), dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	// keyring.json holding a sealed agent key, as a swapped file would, doesn't open.
	body, _ := stablejson.Marshal(localStateKeys{Version: sealedStateVersion, SealedKeyring: sealed})
	if err = os.WriteFile(filepath.Join(dir, "keyring.json"), body, 0600); err != nil {
		t.Fatal(err)
	}
	if j, err = OpenSealed(context.Background(), dir, storeOpener(store)); err == nil {
		j.Close()
		t.Fatal("a journal opened with another sealed value as its keyring")
	}
}

func TestSealedLocalStateRejectsMixedEnvelopes(t *testing.T) {
	store := InsecureKeyStore()
	dir := privateDir(t)
	j, err := OpenSealed(context.Background(), dir, storeOpener(store))
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := stablejson.Marshal(j.keys.Load())
	j.Close()
	sealed := readLocalState(t, dir).SealedKeyring
	for _, invalid := range []localStateKeys{
		{Version: sealedStateVersion, Keyring: keys, SealedKeyring: sealed},
		{Version: sealedStateVersion, Keyring: keys},
		{Version: keyedStateVersion, SealedKeyring: sealed},
		{Version: sealedStateVersion + 1, SealedKeyring: sealed},
	} {
		body, _ := stablejson.Marshal(invalid)
		if err = os.WriteFile(filepath.Join(dir, "keyring.json"), body, 0600); err != nil {
			t.Fatal(err)
		}
		if j, err := OpenSealed(context.Background(), dir, storeOpener(store)); err == nil {
			j.Close()
			t.Fatalf("an invalid envelope opened: version %d", invalid.Version)
		}
	}
}

func TestPreflightSaysWhyASealedJournalCannotOpen(t *testing.T) {
	here := InsecureKeyStore()
	dir := privateDir(t)
	if note := SealedJournalNote(context.Background(), dir, storeOpener(here)); note != "" {
		t.Fatal("a host without a journal has a note:", note)
	}
	j, err := OpenSealed(context.Background(), dir, storeOpener(here))
	if err != nil {
		t.Fatal(err)
	}
	j.Close()
	if note := SealedJournalNote(context.Background(), dir, storeOpener(here)); note != "" {
		t.Fatal("a journal the store opens has a note:", note)
	}
	if note := SealedJournalNote(context.Background(), dir, storeOpener(nil)); !strings.Contains(note, "waits for it") {
		t.Fatal("an unavailable store isn't explained:", note)
	}
	note := SealedJournalNote(context.Background(), dir, storeOpener(InsecureKeyStore()))
	if !strings.Contains(note, "enroll this host as a new server with a fresh state directory") {
		t.Fatal("a store that can't read the keyring doesn't say how to recover:", note)
	}
	if _, err = OpenSealed(context.Background(), dir, storeOpener(InsecureKeyStore())); err == nil || err.Error() != note {
		t.Fatal("preflight and the agent disagree about why the journal can't open", err)
	}
	clear := privateDir(t)
	if j, err = Open(clear); err != nil {
		t.Fatal(err)
	}
	j.Close()
	if note := SealedJournalNote(context.Background(), clear, storeOpener(nil)); note != "" {
		t.Fatal("a journal in the clear has a note:", note)
	}
}
