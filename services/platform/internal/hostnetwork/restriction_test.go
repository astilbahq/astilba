package hostnetwork

import (
	"math"
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
	if err != nil || !Restricts(current, next) || next.Generation != wanted.Generation || next.ControllerGeneration != 2 {
		t.Fatal("frozen cleanup did not remain restrictive under current authority", err)
	}
	if same, err := ReconcileRestriction(current, baseline, wanted, unit, 1); err != nil || same.Generation != 9 {
		t.Fatal("a removal under the same controller did not move past what the host holds", err)
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

// A restored management has forgotten what it issued, so a host can hold any generation when its
// local owner transfers it. Nothing the host held may stand in the way of the new controller.
func TestNewerControllerSupersedesWhatAHostHolds(t *testing.T) {
	held := fixture(t)
	held.ControllerGeneration, held.Generation = 1, 1<<40
	for _, test := range []struct {
		name                string
		controller          int64
		generation          int64
		admitted, restricts bool
	}{
		{"newer controller, lower generation", 2, 5, true, true},
		{"newer controller, same generation", 2, held.Generation, true, true},
		{"same controller, lower generation", 1, 5, false, false},
		{"same controller, same generation with other content", 1, held.Generation, false, false},
		{"same controller, later generation", 1, held.Generation + 1, true, true},
		{"older controller, later generation", 0, held.Generation + 1, false, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			next := held
			next.ControllerGeneration, next.Generation = test.controller, test.generation
			next.Exports = nil
			if Admits(held, next) != test.admitted {
				t.Errorf("Admits = %v", !test.admitted)
			}
			if Restricts(held, next) != test.restricts {
				t.Errorf("Restricts = %v", !test.restricts)
			}
		})
	}
	if !Admits(held, held) {
		t.Error("a host refused to reapply what it holds")
	}
	other := held
	other.ServerID = uuid.New()
	other.ControllerGeneration++
	if Admits(held, other) {
		t.Error("a manifest for another server was admitted")
	}
	grown := held
	grown.ControllerGeneration++
	grown.Generation = 5
	grown.Peers = append(slices.Clone(held.Peers), Peer{ServerID: uuid.New(), MembershipID: uuid.New(), Address: "10.251.0.9", PublicKey: public(t), Endpoint: "192.0.2.9:51820"})
	if Restricts(held, grown) {
		t.Error("a newer controller's generation let a removal add a peer")
	}
}

// A removal under a newer controller reports the generation it was sent, so the host's number
// cannot move management's counter, and even a host at the limit can take it.
func TestRestrictionUnderNewerControllerClaimsNothing(t *testing.T) {
	for _, generation := range []int64{1 << 40, math.MaxInt64} {
		held := fixture(t)
		held.ControllerGeneration, held.Generation = 1, generation
		unknown := Peer{ServerID: uuid.New(), MembershipID: uuid.New(), Address: "10.251.0.9", PublicKey: public(t), Endpoint: "192.0.2.9:51820"}
		baseline := held
		baseline.Generation = 4
		held.Peers = append(slices.Clone(held.Peers), unknown)
		held.Sort()
		wanted := baseline
		wanted.Generation, wanted.ControllerGeneration = 5, 2
		next, err := ReconcileRestriction(held, baseline, wanted, uuid.Nil, 2)
		if err != nil || next.Generation != 5 || next.ControllerGeneration != 2 || !Restricts(held, next) {
			t.Fatalf("generation %d: %+v %v", generation, next, err)
		}
		if !slices.Contains(next.Peers, unknown) {
			t.Fatal("a removal scoped by a stale baseline removed a peer outside that scope")
		}
		wanted.ControllerGeneration, wanted.Peers, wanted.Exports = 1, nil, nil
		if _, err = ReconcileRestriction(held, baseline, wanted, uuid.Nil, 1); generation == math.MaxInt64 && err == nil {
			t.Fatal("the same controller moved past the bigint limit")
		}
	}
}
