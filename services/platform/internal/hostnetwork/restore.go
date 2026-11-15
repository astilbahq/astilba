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
	return h.restore(ctx, false, nil)
}

// RestoreMissing puts back the private links this host holds that aren't in place, as after a boot
// restore that couldn't open the key store. A link whose interface carries its key and whose
// last apply finished is left alone, and so is a network skip names. Each failure is a
// NetworkError. A host that has never held managed network state has nothing to restore.
func (h Host) RestoreMissing(ctx context.Context, skip func(uuid.UUID) bool) error {
	if _, err := os.Lstat(filepath.Join(h.Dir, "network")); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return h.restore(ctx, true, skip)
}

// NetworkError is why one network wasn't restored.
type NetworkError struct {
	NetworkID uuid.UUID
	Err       error
}

func (e NetworkError) Error() string { return e.NetworkID.String() + ": " + e.Err.Error() }
func (e NetworkError) Unwrap() error { return e.Err }

func (h Host) restore(ctx context.Context, missing bool, skip func(uuid.UUID) bool) error {
	snapshots, failures := h.held(ctx, missing, skip)
	for _, s := range snapshots {
		if err := h.apply(ctx, s.Manifest, s.Bridges, false); err != nil {
			failures = append(failures, NetworkError{s.Manifest.NetworkID, err})
		}
	}
	return errors.Join(failures...)
}

// held lists the snapshots of the networks this host holds, or with missing only those whose links
// aren't in place, apart from those skip names, and every snapshot it couldn't read.
func (h Host) held(ctx context.Context, missing bool, skip func(uuid.UUID) bool) ([]Snapshot, []error) {
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
		if skip != nil && skip(id) {
			continue
		}
		s, e := h.Snapshot(id)
		if e != nil {
			failures = append(failures, NetworkError{id, e})
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
func (h Host) OpenMissingKeys(ctx context.Context, skip func(uuid.UUID) bool) error {
	snapshots, failures := h.held(ctx, true, skip)
	for _, s := range snapshots {
		if s.Manifest.Disabled || h.liveKey(ctx, s.Manifest) != nil {
			continue
		}
		if _, err := h.privateKey(ctx, s.Manifest.MembershipID); err != nil {
			failures = append(failures, NetworkError{s.Manifest.NetworkID, err})
		}
	}
	return errors.Join(failures...)
}

// holds reports whether m's interface is there and carries m's key.
func (h Host) holds(ctx context.Context, m Manifest) bool {
	actual, err := h.run(ctx, "wg", nil, "show", m.Interface(), "public-key")
	return err == nil && strings.TrimSpace(string(actual)) == m.PublicKey
}
