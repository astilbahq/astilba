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
	snapshots, failures := h.held(ctx, missing)
	for _, s := range snapshots {
		if err := h.apply(ctx, s.Manifest, s.Bridges, false); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// held lists the snapshots of the networks this host holds, or with missing only those whose links
// aren't in place, and every snapshot it couldn't read.
func (h Host) held(ctx context.Context, missing bool) ([]Snapshot, []error) {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "network"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, []error{err}
	}
	var out []Snapshot
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
		if e != nil {
			failures = append(failures, e)
			continue
		}
		// A removal that was applied is in place whatever the interface holds: it carries nothing.
		if missing && s.AppliedHash == Hash(s.Manifest) && (s.Manifest.Disabled || h.holds(ctx, s.Manifest)) {
			continue
		}
		out = append(out, s)
	}
	return out, failures
}

// OpenMissingKeys opens the keys of the links RestoreMissing would put back, without taking the
// host lock or changing anything, so that RestoreMissing can then replay it from a Prepared store.
func (h Host) OpenMissingKeys(ctx context.Context) error {
	snapshots, failures := h.held(ctx, true)
	for _, s := range snapshots {
		if s.Manifest.Disabled || h.liveKey(ctx, s.Manifest) != nil {
			continue
		}
		if _, err := h.privateKey(ctx, s.Manifest.MembershipID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// holds reports whether m's interface is there and carries m's key.
func (h Host) holds(ctx context.Context, m Manifest) bool {
	actual, err := h.run(ctx, "wg", nil, "show", m.Interface(), "public-key")
	return err == nil && strings.TrimSpace(string(actual)) == m.PublicKey
}
