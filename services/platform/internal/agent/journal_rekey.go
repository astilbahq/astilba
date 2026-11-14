package agent

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"maps"
	"sync/atomic"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/cryptobox"
	bolt "go.etcd.io/bbolt"
)

// journalRekeyInterval is how often a running agent rekeys a journal that changed since its last
// rekey.
var journalRekeyInterval = time.Hour

// journalRekeyCheck is how often a running agent checks whether its journal is due a rekey.
var journalRekeyCheck = time.Minute

// maxJournalKeys is the most keys a cryptobox keyring holds.
const maxJournalKeys = 32

// Rekey re-seals every journal record under a new key, then destroys the keys it replaces.
//
// The journal file never clears a page it frees. Until it reuses the page, the page keeps the
// sealed version of the record that was there, including values the agent has since removed from
// finished records. Once the key that sealed that version is gone, nothing the agent holds opens
// it, and neither does a later copy of the state directory.
//
// store, for a journal that seals its keyring and isn't sealed yet, seals it from now on.
//
// Each step is durable before the next. An interrupted rekey leaves keys that open every record,
// and the next one finishes it.
func (j *Journal) Rekey(ctx context.Context, store KeyStore) error {
	return j.rekey(ctx, store, nil)
}

func (j *Journal) rekey(ctx context.Context, store KeyStore, point func(string) error) error {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	adopting := j.sealing && j.store == nil && store != nil
	if adopting {
		j.store = store
	}
	err := j.rotate(ctx, point)
	// Once a sealed keyring has replaced the file, the journal stays sealed whatever failed after.
	if err != nil && adopting && j.formatVersion.Load() != sealedStateVersion {
		j.store = nil
	}
	return err
}

// rotate replaces the journal's keys with a new one. A keyring that rekeys cut short have filled
// is first left holding its active key alone, so that the new key still replaces every other.
func (j *Journal) rotate(ctx context.Context, point func(string) error) error {
	if keys := j.keys.Load(); len(keys.Keys) >= maxJournalKeys {
		if err := j.resealUnder(ctx, keys, keys.Active, point); err != nil {
			return err
		}
	}
	next, id, err := withNewKey(j.keys.Load())
	if err != nil {
		return err
	}
	return j.resealUnder(ctx, next, id, point)
}

// resealUnder makes next durable, re-seals every record under its key id, then keeps id alone.
func (j *Journal) resealUnder(ctx context.Context, next *cryptobox.Keyring, id string, point func(string) error) error {
	at := func(name string) error {
		if point != nil {
			return point(name)
		}
		return nil
	}
	// The new key is durable before anything is sealed with it.
	if err := j.writeKeys(ctx, next, int(j.formatVersion.Load()), point); err != nil {
		return err
	}
	j.keys.Store(next)
	if err := at("rekey-key-added"); err != nil {
		return err
	}
	var resealed uint64
	if err := j.db.Update(func(tx *bolt.Tx) error {
		resealed = uint64(tx.ID())
		return resealAll(tx, next)
	}); err != nil {
		return err
	}
	if err := at("rekey-resealed"); err != nil {
		return err
	}
	final := &cryptobox.Keyring{Active: id, Keys: map[string][]byte{id: next.Keys[id]}}
	if err := j.writeKeys(ctx, final, int(j.formatVersion.Load()), point); err != nil {
		return err
	}
	j.rekeyedAt.Store(resealed)
	if err := at("rekey-keys-dropped"); err != nil {
		return err
	}
	// A read that began before the records were re-sealed may still open one under a replaced
	// key, so the journal holds those keys until no such read can remain.
	if !j.db.awaitReads(ctx) {
		return nil
	}
	j.keys.Store(final)
	return nil
}

// withNewKey adds a new active key to keys, keeping every other: until the records are re-sealed,
// any of them may still need one.
func withNewKey(keys *cryptobox.Keyring) (*cryptobox.Keyring, string, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return nil, "", err
	}
	id := "k-" + hex.EncodeToString(random)
	fresh, err := cryptobox.New(id)
	if err != nil {
		return nil, "", err
	}
	next := &cryptobox.Keyring{Active: id, Keys: maps.Clone(keys.Keys)}
	next.Keys[id] = fresh.Keys[id]
	return next, id, nil
}

// journalDB is the journal's database. It counts the reads open on it, which bbolt's own count
// takes only after a read has its snapshot.
type journalDB struct {
	*bolt.DB
	reads atomic.Int64
}

// View counts the read before it takes its snapshot, and until it ends.
func (d *journalDB) View(fn func(*bolt.Tx) error) error {
	d.reads.Add(1)
	defer d.reads.Add(-1)
	return d.DB.View(fn)
}

// awaitReads waits until no read is open, so that none began before it was called. It reports
// false if ctx ends first.
func (d *journalDB) awaitReads(ctx context.Context) bool {
	for d.reads.Load() > 0 {
		if !wait(ctx, 10*time.Millisecond) {
			return false
		}
	}
	return true
}

// resealAll seals every record again under the active key, each with a new data key.
func resealAll(tx *bolt.Tx, keys *cryptobox.Keyring) error {
	type row struct{ bucket, key []byte }
	var rows []row
	if err := tx.ForEach(func(name []byte, b *bolt.Bucket) error {
		return b.ForEach(func(k, v []byte) error {
			if v == nil {
				return errors.New("the agent journal holds a nested bucket it can't re-seal")
			}
			rows = append(rows, row{bytes.Clone(name), bytes.Clone(k)})
			return nil
		})
	}); err != nil {
		return err
	}
	// Writing while iterating would move the cursor, so the rows are collected first.
	for _, r := range rows {
		b := tx.Bucket(r.bucket)
		identity := string(r.bucket) + ":" + string(r.key)
		plain, err := keys.Open(b.Get(r.key), identity)
		if err != nil {
			return errors.New("an agent journal record can't be opened, so it can't be re-sealed: " + identity)
		}
		sealed, err := keys.Seal(plain, identity)
		if err != nil {
			return err
		}
		if err = b.Put(r.key, sealed); err != nil {
			return err
		}
	}
	return nil
}

// changedSinceRekey reports whether anything has written to the journal since it was last rekeyed.
func (j *Journal) changedSinceRekey() bool {
	var last uint64
	_ = j.db.View(func(tx *bolt.Tx) error { last = uint64(tx.ID()); return nil })
	return last != j.rekeyedAt.Load()
}

// sealPending reports whether the journal seals its keyring, isn't sealed yet, and store could.
func (j *Journal) sealPending(store KeyStore) bool {
	j.stateMu.Lock()
	defer j.stateMu.Unlock()
	return j.sealing && j.store == nil && store != nil
}

// keepJournalRekeyed rekeys the journal when the agent starts, as soon as the key store opens for
// a journal not yet sealed to it, and each hour the journal has changed since its last rekey. A
// failure is tried again an hour later.
func (c *Client) keepJournalRekeyed(ctx context.Context) {
	var last time.Time
	sealTried := false
	for {
		// The first rekey waits for the key store's first attempt, so that it can seal at once.
		if !c.keyStoreSettled() {
			if !wait(ctx, journalRekeyCheck) {
				return
			}
			continue
		}
		store := c.keyStore()
		seal := c.Journal.sealPending(store)
		due := last.IsZero() || seal && !sealTried || time.Since(last) >= journalRekeyInterval && (seal || c.Journal.changedSinceRekey())
		if due {
			last, sealTried = time.Now(), sealTried || seal
			if err := c.Journal.Rekey(ctx, store); err != nil && ctx.Err() == nil {
				slog.Warn("the agent could not rekey its journal; it tries again within the hour", "error", err)
			}
		}
		if !wait(ctx, journalRekeyCheck) {
			return
		}
	}
}
