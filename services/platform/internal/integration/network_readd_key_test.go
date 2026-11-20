package integration

import "testing"

// Remove from private networks, then adding the server back, gives it a new membership and so a
// new private-link key. Its peers drop the old key, so a copy of the state directory that holds
// it, such as one taken before the agent sealed it, joins no network.
func TestReAddedServerJoinsWithANewKey(t *testing.T) {
	n := newAheadNetwork(t, 2)
	a, c := n.hosts[0], n.hosts[1]
	n.save(t, a, c)
	n.converge(t)
	oldMembership, oldKey := c.held.MembershipID, c.held.PublicKey
	peerKey := func() string {
		for _, p := range a.held.Peers {
			if p.ServerID == c.id {
				return p.PublicKey
			}
		}
		return ""
	}
	if oldKey == "" || peerKey() != oldKey {
		t.Fatal("the test proves nothing: the peer doesn't hold the server's key")
	}
	n.b.call("POST", n.base+"/servers/"+c.id.String()+"/network-removal", nil, 204, nil)
	n.until(t, func() bool { return !holdsPeer(a, c.id) })
	n.save(t, a, c)
	n.converge(t)
	if c.held.MembershipID == oldMembership || c.held.PublicKey == oldKey || c.held.PublicKey == "" {
		t.Fatalf("the re-added server kept its membership or key: %s %s", c.held.MembershipID, c.held.PublicKey)
	}
	if got := peerKey(); got != c.held.PublicKey {
		t.Fatalf("the peer holds %q, not the server's new key", got)
	}
}
