package hostnetwork

import (
	"errors"
	"math"
	"slices"

	"github.com/google/uuid"
)

func sameLocalIdentity(a, b Manifest) bool {
	return a.Version == b.Version && a.NetworkID == b.NetworkID && a.MembershipID == b.MembershipID && a.ServerID == b.ServerID && a.Prefix == b.Prefix && a.Address == b.Address && a.PublicKey == b.PublicKey && a.ListenPort == b.ListenPort
}

func sameEndpoint(a, b Endpoint) bool {
	return a.ID == b.ID && a.UnitID == b.UnitID && a.ResourceID == b.ResourceID && a.InstanceID == b.InstanceID && a.Port == b.Port && a.ContainerPort == b.ContainerPort
}

func samePeerIdentity(a, b Peer) bool {
	return a.ServerID == b.ServerID && a.MembershipID == b.MembershipID && a.PublicKey == b.PublicKey
}

func hasPeerIdentity(peers []Peer, peer Peer) bool {
	return slices.ContainsFunc(peers, func(p Peer) bool { return samePeerIdentity(p, peer) })
}

// ReconcileRestriction subtracts frozen removals from the current admitted
// authority. A parent owns only its local unit's grants, never peer membership.
// A nil unit scopes a standalone revocation to its complete frozen baseline.
func ReconcileRestriction(current, baseline, wanted Manifest, unit uuid.UUID, controller int64) (Manifest, error) {
	if current.Validate() != nil || baseline.Validate() != nil || wanted.Validate() != nil || !sameLocalIdentity(current, baseline) || !sameLocalIdentity(baseline, wanted) || controller < current.ControllerGeneration || controller < baseline.ControllerGeneration || controller < wanted.ControllerGeneration {
		return Manifest{}, errors.New("network restriction identity changed")
	}
	next := current
	next.Peers = slices.Clone(current.Peers)[:0]
	next.Exports = slices.Clone(current.Exports)[:0]
	next.Egress = slices.Clone(current.Egress)[:0]
	for _, peer := range current.Peers {
		if unit == uuid.Nil && hasPeerIdentity(baseline.Peers, peer) && !hasPeerIdentity(wanted.Peers, peer) {
			continue
		}
		next.Peers = append(next.Peers, peer)
	}
	retainedPeer := func(id uuid.UUID) bool {
		return slices.ContainsFunc(next.Peers, func(p Peer) bool { return p.ServerID == id })
	}
	knownPeer := func(id uuid.UUID) bool {
		return slices.ContainsFunc(current.Peers, func(p Peer) bool { return p.ServerID == id && hasPeerIdentity(baseline.Peers, p) })
	}
	wantedPeer := func(id uuid.UUID) bool {
		return slices.ContainsFunc(current.Peers, func(p Peer) bool { return p.ServerID == id && hasPeerIdentity(wanted.Peers, p) })
	}
	for _, endpoint := range current.Exports {
		var prior, desired *Endpoint
		if unit == uuid.Nil || endpoint.UnitID == unit {
			for i := range baseline.Exports {
				if sameEndpoint(endpoint, baseline.Exports[i]) {
					prior = &baseline.Exports[i]
				}
			}
			for i := range wanted.Exports {
				if sameEndpoint(endpoint, wanted.Exports[i]) {
					desired = &wanted.Exports[i]
				}
			}
		}
		endpoint.Sources = slices.DeleteFunc(slices.Clone(endpoint.Sources), func(source Source) bool {
			return !retainedPeer(source.ServerID) || prior != nil && knownPeer(source.ServerID) && slices.Contains(prior.Sources, source) && (desired == nil || !wantedPeer(source.ServerID) || !slices.Contains(desired.Sources, source))
		})
		if len(endpoint.Sources) > 0 {
			next.Exports = append(next.Exports, endpoint)
		}
	}
	for _, grant := range current.Egress {
		if !retainedPeer(grant.ServerID) || (unit == uuid.Nil || grant.UnitID == unit) && knownPeer(grant.ServerID) && slices.Contains(baseline.Egress, grant) && (!wantedPeer(grant.ServerID) || !slices.Contains(wanted.Egress, grant)) {
			continue
		}
		next.Egress = append(next.Egress, grant)
	}
	if unit == uuid.Nil && wanted.Disabled && len(next.Peers)+len(next.Exports)+len(next.Egress) == 0 {
		next.Disabled = true
	}
	next.ControllerGeneration = controller
	next.Sort()
	// Scoped no-ops leave their placement's frozen links generation available.
	if Hash(next) == Hash(current) && (unit != uuid.Nil || wanted.Generation <= current.Generation) {
		return current, nil
	}
	if current.Generation == math.MaxInt64 {
		return Manifest{}, errors.New("network generation exhausted")
	}
	next.Generation = max(current.Generation+1, wanted.Generation)
	if !Restricts(current, next) {
		return Manifest{}, errors.New("network restriction expands admitted authority")
	}
	return next, nil
}
