package integration

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/hostnetwork"
	"github.com/google/uuid"
)

// A host refuses a change over a route or container network it already uses. Nothing changed on
// it and nothing about it is in doubt, so management names that conflict and its remedy, not a
// compromise. The same change would be refused the same way, so only removals go to the host at
// that generation, and revocations still reach it. The next generation tries the whole manifest
// again, which lands once the owner has removed the overlap, even for a host that never joined
// and so has nothing a removal could change.
func TestRouteConflictHoldsAMemberOnlyForItsGeneration(t *testing.T) {
	n := newAheadNetwork(t, 5)
	a, b, c, d, joining := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3], n.hosts[4]
	n.save(t, a, b, c, d)
	n.converge(t)
	a.conflict, joining.conflict = true, true
	n.save(t, a, b, c, d, joining)
	commands := func() int64 {
		return claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id IN ($1,$2) AND kind='network'`, a.id, joining.id)
	}
	n.until(t, func() bool { return n.note(t, a) != "" && n.note(t, joining) != "" })
	for _, h := range []*aheadHost{a, joining} {
		if note := n.note(t, h); !strings.Contains(note, "overlaps the network prefix") || strings.Contains(note, "prepare-recovery") || strings.Contains(note, "compromised") {
			t.Fatalf("note = %q", note)
		}
	}
	sent := commands()
	for range 3 {
		n.round(t)
	}
	if got := commands(); got != sent {
		t.Fatalf("%d more network commands went to the hosts after they refused", got-sent)
	}
	n.revoke(t, b)
	n.until(t, func() bool { return !holdsPeer(a, b.id) })
	if holdsPeer(a, joining.id) {
		t.Fatal("a removal granted a peer")
	}
	a.conflict, joining.conflict = false, false
	n.revoke(t, c)
	n.until(t, func() bool {
		return !holdsPeer(a, c.id) && holdsPeer(a, joining.id) && holdsPeer(joining, a.id) && n.note(t, a) == "" && n.note(t, joining) == ""
	})

	// A host can refuse anything, but claiming a route conflict for a removal is refused, and
	// the revoked server's other peers drop it whatever this host says.
	a.conflict, a.conflictRemovals = true, true
	n.revoke(t, d)
	n.until(t, func() bool { return a.rejected > 0 && !holdsPeer(joining, d.id) })
}

// A host that refuses every change over a route conflict holds back only itself. Each
// revocation still reaches it and every other member, the others converge with their notes
// untouched, and a refusal it claims in another member's name is refused, so its reason can't
// land on that member's note.
func TestRepeatedRouteConflictsHoldBackOnlyTheRefusingHost(t *testing.T) {
	n := newAheadNetwork(t, 5)
	a, b, c, d, added := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3], n.hosts[4]
	n.save(t, a, b, c, d)
	n.converge(t)
	a.conflict = true
	n.save(t, a, b, c, d, added)
	settled := func(hosts ...*aheadHost) bool {
		for _, h := range hosts {
			if n.note(t, h) != "" {
				t.Fatalf("another member's note changed: %q", n.note(t, h))
			}
			if claimCounter(t, n.f, `SELECT count(*) FROM network_members WHERE server_id=$1 AND acknowledged_generation=generation`, h.id) != 1 {
				return false
			}
		}
		return true
	}
	n.until(t, func() bool { return a.refused > 0 && settled(b, c, d, added) && holdsPeer(added, d.id) })
	for _, revoked := range []*aheadHost{b, c} {
		refused := a.refused
		n.revoke(t, revoked)
		n.until(t, func() bool {
			return a.refused > refused && !holdsPeer(a, revoked.id) && !holdsPeer(d, revoked.id) && !holdsPeer(added, revoked.id) && settled(d, added)
		})
		if holdsPeer(a, added.id) {
			t.Fatal("a removal granted a peer")
		}
	}
	a.claimMembership = d.held.MembershipID
	n.save(t, a, d, added)
	n.until(t, func() bool { return a.rejected > 0 && settled(d, added) })
}

// Management may have no record of what a host holds, after a lost receipt or a removal the host
// refused. Its whole manifest then carries a revocation, and a route conflict refuses that. The
// refusal says what the host holds, so the revoked peer is removed from it all the same. No host
// can hold a later generation than it was sent, so a refusal claiming one is refused. One naming
// a peer or grant management never made for this host stands, because an honest host can hold
// one after management is restored from an older backup, but its description is kept nowhere.
func TestRevocationReachesARefusingHostWithNoRecord(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
	n.save(t, a, b, c)
	n.converge(t)
	forgetInstalled(t, n, a)
	stored := func() bool {
		return claimCounter(t, n.f, `SELECT count(*) FROM network_members WHERE server_id=$1 AND installed_manifest IS NOT NULL`, a.id) != 0
	}
	a.conflict, a.claimRefused = true, true
	a.claim = func(held, sent hostnetwork.Manifest) hostnetwork.Manifest {
		held.Generation = sent.Generation + 1
		return held
	}
	n.revoke(t, b)
	n.until(t, func() bool { return a.rejected > 0 })
	if stored() {
		t.Fatal("a claim to hold a later generation was stored")
	}
	a.claimRefused = false
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
	for name, claim := range map[string]func(held, sent hostnetwork.Manifest) hostnetwork.Manifest{
		"a peer": func(held, _ hostnetwork.Manifest) hostnetwork.Manifest {
			held.Peers = append(slices.Clone(held.Peers), hostnetwork.Peer{ServerID: uuid.New(), MembershipID: uuid.New(), Address: "10.251.0.200", PublicKey: key, Endpoint: "192.0.2.200:51820"})
			return held
		},
		"an export": func(held, _ hostnetwork.Manifest) hostnetwork.Manifest {
			held.Exports = append(slices.Clone(held.Exports), hostnetwork.Endpoint{ID: uuid.New(), UnitID: uuid.New(), ResourceID: uuid.New(), InstanceID: uuid.New(), Port: 20999, ContainerPort: 80, Sources: []hostnetwork.Source{{ServerID: c.id, UnitID: uuid.New()}}})
			return held
		},
		"egress": func(held, _ hostnetwork.Manifest) hostnetwork.Manifest {
			held.Egress = append(slices.Clone(held.Egress), hostnetwork.Egress{UnitID: uuid.New(), ServerID: c.id, EndpointID: uuid.New(), Port: 20998})
			return held
		},
	} {
		a.claim = claim
		n.until(t, func() bool { return n.note(t, a) != "" })
		if stored() {
			t.Fatalf("a claim to hold %s management never made was stored", name)
		}
		// Only the next generation tries the change again.
		n.save(t, a, c)
	}
	if !holdsPeer(a, b.id) {
		t.Fatal("the fixture removed the peer without management")
	}
	a.claim = nil
	n.until(t, func() bool { return !holdsPeer(a, b.id) })
	if !holdsPeer(a, c.id) {
		t.Fatal("the removal dropped a peer that was not revoked")
	}
}

// What a refusing host says it holds scopes only its own removals. A host that leaves a peer out
// keeps that peer itself, but the revoked key still leaves every honest member, and none of them
// is sent anything more or noted for it.
func TestMisdescribingHostLimitsOnlyItsOwnRemoval(t *testing.T) {
	n := newAheadNetwork(t, 4)
	a, b, c, d := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
	n.save(t, a, b, c, d)
	n.converge(t)
	forgetInstalled(t, n, a)
	a.conflict = true
	a.claim = func(held, _ hostnetwork.Manifest) hostnetwork.Manifest {
		held.Peers = slices.DeleteFunc(slices.Clone(held.Peers), func(p hostnetwork.Peer) bool { return p.ServerID == c.id })
		return held
	}
	n.revoke(t, c)
	honest := func() bool {
		for _, h := range []*aheadHost{b, d} {
			if note := n.note(t, h); note != "" {
				t.Fatalf("another member's note changed: %q", note)
			}
			if claimCounter(t, n.f, `SELECT count(*) FROM network_members WHERE server_id=$1 AND acknowledged_generation=generation`, h.id) != 1 {
				return false
			}
		}
		return a.refused > 0 && !holdsPeer(b, c.id) && !holdsPeer(d, c.id)
	}
	n.until(t, honest)
	for _, h := range []*aheadHost{b, d} {
		if len(h.held.Peers) != 2 || !holdsPeer(h, a.id) || len(h.held.Exports) != 0 || len(h.held.Egress) != 0 {
			t.Fatalf("an honest member was sent more than before: %+v", h.held)
		}
	}
	for range 3 {
		n.round(t)
	}
	if !holdsPeer(a, c.id) {
		t.Fatal("management removed a peer the host said it did not hold")
	}
}

func forgetInstalled(t *testing.T, n *aheadNetwork, h *aheadHost) {
	t.Helper()
	if _, err := n.f.st.Pool.Exec(n.ctx, `UPDATE network_members SET installed_manifest=NULL,manifest_hash='' WHERE server_id=$1`, h.id); err != nil {
		t.Fatal(err)
	}
}

// A modified agent can describe, on every refusal, what it held before management's last removal
// landed. Each such removal leaves the host short of the change it refused, so the network moves
// to a new generation and every member is sent it again. Management keeps no description older
// than the record it has, so that happens once, not without end.
func TestReplayedDescriptionDoesNotSpendTheNetworkGeneration(t *testing.T) {
	n := newAheadNetwork(t, 4)
	a, b, c, added := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
	n.save(t, a, b, c)
	n.converge(t)
	var stale *hostnetwork.Manifest
	a.conflict = true
	a.claim = func(held, _ hostnetwork.Manifest) hostnetwork.Manifest {
		if stale == nil {
			stale = &held
		}
		return *stale
	}
	n.save(t, a, b, c, added)
	n.until(t, func() bool { return stale != nil && n.note(t, a) != "" })
	n.revoke(t, c)
	n.until(t, func() bool { return !holdsPeer(a, c.id) && !holdsPeer(b, c.id) })
	for range 3 {
		n.round(t)
	}
	settled := n.counter(t)
	for range 8 {
		n.round(t)
	}
	if got := n.counter(t); got != settled {
		t.Fatalf("a replayed description moved the network generation from %d to %d", settled, got)
	}
}

// A member whose first join was refused over a route, and which the owner then removes, holds
// nothing, so it acknowledges its removal without applying it, and every later one too, and the
// network and new placements stop waiting on it.
func TestRemovingAMemberThatNeverJoinedReleasesTheNetwork(t *testing.T) {
	n := newAheadNetwork(t, 4)
	a, b, c, d := n.hosts[0], n.hosts[1], n.hosts[2], n.hosts[3]
	n.save(t, a, b, d)
	n.converge(t)
	environment := n.primaryEnvironment(t, a)
	c.conflict = true
	n.save(t, a, b, c, d)
	n.until(t, func() bool { return n.note(t, c) != "" })
	wantPlacementRefused(t, n.place(t, &environment, a.id, b.id))
	n.save(t, a, b, d)
	n.converge(t)
	// A later generation reaches the removed member as a removal again.
	n.revoke(t, d)
	n.converge(t)
	wantNoUnsettledNetworkWork(t, n, c)
	if c.held.NetworkID != uuid.Nil {
		t.Fatalf("the removed member installed %+v", c.held)
	}
	if got := n.place(t, &environment, a.id, b.id); got.err != nil || got.status != http.StatusCreated {
		t.Fatalf("placement after the removal = %d %s %v", got.status, got.body, got.err)
	}
}

func wantNoUnsettledNetworkWork(t *testing.T, n *aheadNetwork, h *aheadHost) {
	t.Helper()
	if got := claimCounter(t, n.f, `SELECT count(*) FROM execution_operations WHERE server_id=$1 AND kind='network' AND state NOT IN ('succeeded','failed','cancelled')`, h.id); got != 0 {
		t.Fatalf("%d network commands are unsettled on the removed member", got)
	}
}

// A receipt that says a configure adding the host was a removal is refused, so the network keeps
// waiting for that host. The agent itself acknowledges only a disabled manifest unapplied.
func TestAcknowledgedRemovalCannotStandInForJoining(t *testing.T) {
	n := newAheadNetwork(t, 3)
	a, b, c := n.hosts[0], n.hosts[1], n.hosts[2]
	c.forgeRemoval = true
	n.save(t, a, b, c)
	n.until(t, func() bool { return c.rejected > 0 })
	for range 3 {
		n.round(t)
	}
	if view := n.view(t); view.State != "pending" {
		t.Fatalf("a forged removal let the network become %s", view.State)
	}
}
