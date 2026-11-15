package hostnetwork

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// Restore puts back every private link this host holds, as at boot. A network that fails doesn't
// stop the others; Restore reports every failure.
func (h Host) Restore(ctx context.Context) error {
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return h.restore(ctx, false)
}

// RestoreMissing puts back the private links this host holds that aren't in place, as after a boot
// restore that couldn't open the key store. A link whose interface carries its key and whose
// last apply finished is left alone. A host that has never held managed network state has nothing
// to restore.
func (h Host) RestoreMissing(ctx context.Context) error {
	if _, err := os.Lstat(filepath.Join(h.Dir, "network")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return h.restore(ctx, true)
}

func (h Host) restore(ctx context.Context, missing bool) error {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "network"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var failures []error
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id, e := uuid.Parse(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			failures = append(failures, errors.New("invalid network state filename"))
			continue
		}
		s, e := h.Snapshot(id)
		if e == nil && missing && s.AppliedHash == Hash(s.Manifest) && h.holds(ctx, s.Manifest) {
			continue
		}
		if e == nil {
			e = h.apply(ctx, s.Manifest, s.Bridges, false)
		}
		if e != nil {
			failures = append(failures, e)
		}
	}
	return errors.Join(failures...)
}

// holds reports whether m's interface is there and carries m's key.
func (h Host) holds(ctx context.Context, m Manifest) bool {
	actual, err := h.run(ctx, "wg", nil, "show", m.Interface(), "public-key")
	return err == nil && strings.TrimSpace(string(actual)) == m.PublicKey
}
