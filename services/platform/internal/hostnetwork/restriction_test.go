package hostnetwork

import (
	"slices"
	"testing"

	"github.com/google/uuid"
)

func TestNoOpGenerationAcknowledgementKeepsPlacementPhaseAvailable(t *testing.T) {
	current := fixture(t)
	wanted := current
	wanted.Generation++
	for _, test := range []struct {
		name string
		unit uuid.UUID
		want Manifest
	}{
		{"standalone acknowledges owner intent", uuid.Nil, wanted},
		{"placement keeps frozen links generation", current.Exports[0].UnitID, current},
	} {
		t.Run(test.name, func(t *testing.T) {
			next, err := ReconcileRestriction(current, current, wanted, test.unit, current.ControllerGeneration)
			if err != nil || Hash(next) != Hash(test.want) || !Restricts(current, next) {
				t.Fatal("no-op acknowledgement changed grants or consumed the wrong generation", err)
			}
			again, err := ReconcileRestriction(next, current, wanted, test.unit, current.ControllerGeneration)
			if err != nil || Hash(again) != Hash(next) {
				t.Fatal("acknowledged no-op did not remain idempotent", err)
			}
		})
	}
}

func TestFrozenRestrictionPreservesOtherUnitsAndNewerAuthority(t *testing.T) {
	baseline := fixture(t)
	unit := baseline.Exports[0].UnitID
	oldSource := baseline.Exports[0].Sources[0]
	oldGrant := Egress{UnitID: unit, ServerID: oldSource.ServerID, EndpointID: uuid.New(), Port: 20002}
	baseline.Egress = []Egress{oldGrant}
	wanted := baseline
	wanted.Generation = 3
	wanted.Exports, wanted.Egress = nil, nil
	current := baseline
	current.Generation = 8
	current.ControllerGeneration = 1
	current.Exports = slices.Clone(baseline.Exports)
	unknownSource := Source{ServerID: oldSource.ServerID, UnitID: uuid.New()}
	current.Exports[0].Sources = append(slices.Clone(baseline.Exports[0].Sources), unknownSource)
	other := baseline.Exports[0]
	other.ID, other.UnitID, other.Port = uuid.New(), uuid.New(), 20003
	current.Exports = append(current.Exports, other)
	unknownGrant := oldGrant
	unknownGrant.EndpointID, unknownGrant.Port = uuid.New(), 20004
	otherGrant := oldGrant
	otherGrant.UnitID = other.UnitID
	current.Egress = append(slices.Clone(baseline.Egress), unknownGrant, otherGrant)
	current.Sort()
	next, err := ReconcileRestriction(current, baseline, wanted, unit, 2)
	if err != nil || !Restricts(current, next) || next.Generation != 9 || next.ControllerGeneration != 2 {
		t.Fatal("frozen cleanup did not remain restrictive under current authority", err)
	}
	if len(next.Exports) != 2 || !slices.Contains(next.Egress, unknownGrant) || !slices.Contains(next.Egress, otherGrant) || slices.Contains(next.Egress, oldGrant) {
		t.Fatal("cleanup removed unrelated grants or kept its old egress")
	}
	for _, endpoint := range next.Exports {
		if endpoint.UnitID == unit && !slices.Equal(endpoint.Sources, []Source{unknownSource}) || endpoint.UnitID == other.UnitID && !slices.Equal(endpoint.Sources, other.Sources) {
			t.Fatal("cleanup exceeded its exact unit/source scope")
		}
	}
	again, err := ReconcileRestriction(next, baseline, wanted, unit, 2)
	if err != nil || Hash(again) != Hash(next) {
		t.Fatal("repeated cleanup changed an already applied restriction", err)
	}
	// A newer host revocation stays absent even when the old wanted frame
	// still contains its grant.
	revoked := current
	revoked.Exports, revoked.Egress = nil, nil
	replayed, err := ReconcileRestriction(revoked, baseline, baseline, unit, 1)
	if err != nil || len(replayed.Exports)+len(replayed.Egress) != 0 {
		t.Fatal("stale intent restored revoked authority", err)
	}
}

func TestFrozenPeerRevocationDoesNotRevokeRenewedIdentity(t *testing.T) {
	baseline := fixture(t)
	wanted := baseline
	wanted.Generation = 2
	wanted.Peers, wanted.Exports = nil, nil
	old, err := ReconcileRestriction(baseline, baseline, wanted, uuid.Nil, 0)
	if err != nil || len(old.Peers)+len(old.Exports) != 0 {
		t.Fatal("standalone revocation did not remove the exact old peer", err)
	}
	moved := baseline
	moved.Generation = 3
	moved.Peers = slices.Clone(baseline.Peers)
	moved.Peers[0].Endpoint = "192.0.2.3:51820"
	revoked, err := ReconcileRestriction(moved, baseline, wanted, uuid.Nil, 0)
	if err != nil || len(revoked.Peers)+len(revoked.Exports) != 0 {
		t.Fatal("a transport update exempted the same membership/key from revocation", err)
	}
	renewed := baseline
	renewed.Generation = 4
	renewed.Peers = slices.Clone(baseline.Peers)
	renewed.Peers[0].MembershipID, renewed.Peers[0].PublicKey = uuid.New(), public(t)
	kept, err := ReconcileRestriction(renewed, baseline, wanted, uuid.Nil, 0)
	if err != nil || Hash(kept) != Hash(renewed) {
		t.Fatal("old revocation changed a renewed peer's authority", err)
	}
	renewed.ControllerGeneration = 2
	if _, err := ReconcileRestriction(renewed, baseline, wanted, uuid.Nil, 1); err == nil {
		t.Fatal("restriction reduced the host's controller authority")
	}
}
