package hostnetwork

import (
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"github.com/google/uuid"
	"testing"
)

func public(t *testing.T) string {
	t.Helper()
	k, e := ecdh.X25519().GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return base64.StdEncoding.EncodeToString(k.PublicKey().Bytes())
}
func fixture(t *testing.T) Manifest {
	t.Helper()
	peer := uuid.New()
	source := uuid.New()
	return Manifest{Version: Version, NetworkID: uuid.New(), MembershipID: uuid.New(), ServerID: uuid.New(), Generation: 1, Prefix: "10.251.0.0/24", Address: "10.251.0.1", PublicKey: public(t), ListenPort: 51820, Peers: []Peer{{ServerID: peer, MembershipID: uuid.New(), Address: "10.251.0.2", PublicKey: public(t), Endpoint: "192.0.2.2:51820"}}, Exports: []Endpoint{{ID: uuid.New(), UnitID: uuid.New(), ResourceID: uuid.New(), InstanceID: uuid.New(), Port: 20001, ContainerPort: 8080, Sources: []Source{{ServerID: peer, UnitID: source}}}}, Egress: []Egress{}}
}
func TestRecoveryOnlyRemovesInstalledNetworkAuthority(t *testing.T) {
	old := fixture(t)
	if err := old.Validate(); err != nil {
		t.Fatal(err)
	}
	if !Restricts(old, old) {
		t.Fatal("identical persisted restriction cannot resume after an interrupted effect or receipt")
	}
	additions := old
	additions.Generation++
	additions.Egress = []Egress{{UnitID: uuid.New(), EndpointID: uuid.New()}}
	if restricted := Restriction(old, additions); restricted.Generation != old.Generation {
		t.Fatal("addition-only sweep consumed the reviewed links generation")
	}
	revoked := additions
	revoked.Exports = nil
	if restricted := Restriction(old, revoked); restricted.Generation != revoked.Generation || !Restricts(old, restricted) || len(restricted.Exports) != 0 || len(restricted.Egress) != 0 {
		t.Fatal("real revocation was blocked or granted pending additions")
	}
	changed := old
	changed.Exports = nil
	if Restricts(old, changed) {
		t.Fatal("different same-generation authority accepted")
	}
	next := old
	next.Generation++
	next.Exports = nil
	if !Restricts(old, next) {
		t.Fatal("removal cannot be admitted for recovery")
	}
	next = old
	next.Generation++
	next.Exports = append([]Endpoint(nil), old.Exports...)
	next.Exports[0].Sources = append(append([]Source(nil), old.Exports[0].Sources...), Source{ServerID: old.Peers[0].ServerID, UnitID: uuid.New()})
	if Restricts(old, next) {
		t.Fatal("recovery expanded source-unit authority")
	}
	next = old
	next.Generation++
	next.Peers = append([]Peer(nil), old.Peers...)
	next.Peers[0].PublicKey = public(t)
	if Restricts(old, next) {
		t.Fatal("recovery replaced peer authority")
	}
	old.Egress = []Egress{{UnitID: uuid.New(), ServerID: old.Peers[0].ServerID, EndpointID: uuid.New(), Port: 20002}}
	next.Egress = old.Egress
	removedPeer := Restriction(old, next)
	if removedPeer.Validate() != nil || !Restricts(old, removedPeer) || len(removedPeer.Peers) != 0 || len(removedPeer.Exports) != 0 || len(removedPeer.Egress) != 0 {
		t.Fatal("re-enrolled peer retained its predecessor's grants or prevented revocation")
	}
	next = old
	next.Generation++
	next.Exports = nil
	next.Egress = nil
	next.Peers = nil
	next.Disabled = true
	if !Restricts(old, next) {
		t.Fatal("complete deauthorization rejected")
	}
}
func TestNetworkKeyRemainsHostLocalAcrossReconciliation(t *testing.T) {
	h := Host{Dir: t.TempDir()}
	member := uuid.New()
	first, err := h.Identity(member)
	if err != nil {
		t.Fatal(err)
	}
	again, err := h.Identity(member)
	if err != nil || again != first {
		t.Fatal("network identity changed across restart", err)
	}
	other, err := h.Identity(uuid.New())
	if err != nil || other.PublicKey == first.PublicKey {
		t.Fatal("renewed membership reused its predecessor key", err)
	}
}
