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
func (h Host) RestoreMissing(ctx context.Context, skip func(Snapshot) bool) error {
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

// NetworkError is why one network wasn't restored. NeedsKey says the attempt had to open the
// network's key from the key store, because no live link held it.
type NetworkError struct {
	NetworkID uuid.UUID
	NeedsKey  bool
	Err       error
}

func (e NetworkError) Error() string { return e.NetworkID.String() + ": " + e.Err.Error() }
func (e NetworkError) Unwrap() error { return e.Err }

func (h Host) restore(ctx context.Context, missing bool, skip func(Snapshot) bool) error {
	snapshots, failures := h.held(ctx, missing, skip)
	for _, s := range snapshots {
		needsKey := h.needsKey(ctx, s.Manifest)
		if err := h.apply(ctx, s.Manifest, s.Bridges, false, true); err != nil {
			failures = append(failures, NetworkError{s.Manifest.NetworkID, needsKey, err})
		}
	}
	return errors.Join(failures...)
}

// held lists the snapshots of the networks this host holds, or with missing only those whose links
// aren't in place, apart from those skip names, and every snapshot it couldn't read.
func (h Host) held(ctx context.Context, missing bool, skip func(Snapshot) bool) ([]Snapshot, []error) {
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
			failures = append(failures, NetworkError{NetworkID: id, Err: e})
			continue
		}
		if skip != nil && skip(s) {
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
func (h Host) OpenMissingKeys(ctx context.Context, skip func(Snapshot) bool) error {
	snapshots, failures := h.held(ctx, true, skip)
	for _, s := range snapshots {
		if !h.needsKey(ctx, s.Manifest) {
			continue
		}
		if _, err := h.privateKey(ctx, s.Manifest.MembershipID); err != nil {
			failures = append(failures, NetworkError{s.Manifest.NetworkID, true, err})
		}
	}
	return errors.Join(failures...)
}

// needsKey reports whether restoring m opens its key from the key store: m isn't a removal, which
// needs none, no live link holds the key, and the key is sealed.
func (h Host) needsKey(ctx context.Context, m Manifest) bool {
	if m.Disabled || h.Keys == nil || h.liveKey(ctx, m) != nil {
		return false
	}
	f, err := h.readKeyFile(m.MembershipID)
	return err == nil && f.clear == nil
}

// holds reports whether m's interface is there and carries m's key.
func (h Host) holds(ctx context.Context, m Manifest) bool {
	actual, err := h.run(ctx, "wg", nil, "show", m.Interface(), "public-key")
	return err == nil && strings.TrimSpace(string(actual)) == m.PublicKey
}

// routesAlready reports whether m's interface already routes every peer m names.
func (h Host) routesAlready(ctx context.Context, m Manifest) bool {
	routed, err := h.routedPeers(ctx, m)
	if err != nil {
		return false
	}
	for _, p := range m.Peers {
		if !routed[p.Address] {
			return false
		}
	}
	return true
}

// routedPeers is the set of addresses m's interface has a route to.
func (h Host) routedPeers(ctx context.Context, m Manifest) (map[string]bool, error) {
	raw, err := h.run(ctx, "ip", nil, "-j", "-4", "route", "show", "dev", m.Interface())
	if err != nil {
		return nil, err
	}
	var routes []struct {
		Dst string `json:"dst"`
	}
	if readToolJSON(raw, &routes) != nil {
		return nil, errors.New("cannot inspect managed network routes")
	}
	out := map[string]bool{}
	for _, r := range routes {
		out[strings.TrimSuffix(r.Dst, "/32")] = true
	}
	return out, nil
}
