package hostnetwork

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/google/uuid"
)

const Version = 1
const MaxMembers = 16
const FirstEndpointPort = 20000
const LastEndpointPort = 39999

// Network configuration is execution intent. Private keys never enter it.
type Manifest struct {
	Version              int        `json:"version"`
	NetworkID            uuid.UUID  `json:"networkId"`
	MembershipID         uuid.UUID  `json:"membershipId"`
	ServerID             uuid.UUID  `json:"serverId"`
	ControllerGeneration int64      `json:"controllerGeneration"`
	Generation           int64      `json:"generation"`
	Prefix               string     `json:"prefix"`
	Address              string     `json:"address"`
	PublicKey            string     `json:"publicKey"`
	ListenPort           int        `json:"listenPort"`
	Peers                []Peer     `json:"peers"`
	Exports              []Endpoint `json:"exports"`
	Egress               []Egress   `json:"egress"`
	Disabled             bool       `json:"disabled"`
}
type Peer struct {
	ServerID     uuid.UUID `json:"serverId"`
	MembershipID uuid.UUID `json:"membershipId"`
	Address      string    `json:"address"`
	PublicKey    string    `json:"publicKey"`
	Endpoint     string    `json:"endpoint"`
}
type Source struct {
	ServerID uuid.UUID `json:"serverId"`
	UnitID   uuid.UUID `json:"unitId"`
}
type Endpoint struct {
	ID            uuid.UUID `json:"id"`
	UnitID        uuid.UUID `json:"unitId"`
	ResourceID    uuid.UUID `json:"resourceId"`
	InstanceID    uuid.UUID `json:"instanceId"`
	Port          int       `json:"port"`
	ContainerPort int       `json:"containerPort"`
	Sources       []Source  `json:"sources"`
}
type Egress struct {
	Ingress    bool      `json:"ingress,omitempty"`
	UnitID     uuid.UUID `json:"unitId"`
	ServerID   uuid.UUID `json:"serverId"`
	EndpointID uuid.UUID `json:"endpointId"`
	Port       int       `json:"port"`
}
type Identity struct {
	MembershipID uuid.UUID `json:"membershipId"`
	PublicKey    string    `json:"publicKey"`
}
type Command struct {
	Action       string    `json:"action"`
	NetworkID    uuid.UUID `json:"networkId"`
	MembershipID uuid.UUID `json:"membershipId"`
	Manifest     *Manifest `json:"manifest,omitempty"`
	Baseline     *Manifest `json:"baseline,omitempty"`
}
type Receipt struct {
	Identity   Identity  `json:"identity"`
	NetworkID  uuid.UUID `json:"networkId"`
	Generation int64     `json:"generation"`
	Hash       string    `json:"hash"`
	Disabled   bool      `json:"disabled"`
	Before     *Manifest `json:"before,omitempty"`
	Effective  *Manifest `json:"effective,omitempty"`
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (m Manifest) Interface() string {
	return "as" + strings.ReplaceAll(m.NetworkID.String(), "-", "")[:12]
}
func (m Manifest) Alias() string {
	return "astilba:" + m.NetworkID.String() + ":" + m.ServerID.String()
}
func PublicKey(s string) bool {
	b, err := base64.StdEncoding.DecodeString(s)
	return err == nil && len(b) == 32 && s == base64.StdEncoding.EncodeToString(b) && slices.ContainsFunc(b, func(v byte) bool { return v != 0 })
}
func Prefix(s string) (netip.Prefix, error) {
	p, err := netip.ParsePrefix(s)
	if err != nil || !p.Addr().Is4() || !p.Addr().IsPrivate() || p != p.Masked() || p.Bits() < 20 || p.Bits() > 24 || !p.Contains(p.Addr().Next()) {
		return netip.Prefix{}, errors.New("private network requires an aligned IPv4 /20 through /24")
	}
	return p, nil
}
func TransportEndpoint(s string) bool {
	p, err := netip.ParseAddrPort(s)
	return err == nil && p.Addr().Zone() == "" && p.Addr().IsValid() && !p.Addr().IsUnspecified() && !p.Addr().IsLoopback() && !p.Addr().IsMulticast() && !p.Addr().IsLinkLocalUnicast() && p.Port() >= 1024
}
func port(n int) bool { return n >= FirstEndpointPort && n <= LastEndpointPort }
func (m Manifest) Validate() error {
	bad := func() error { return errors.New("invalid managed network manifest") }
	raw, _ := json.Marshal(m)
	if len(raw) > 98304 {
		return bad()
	}
	prefix, err := Prefix(m.Prefix)
	own, e := netip.ParseAddr(m.Address)
	if err != nil || e != nil || m.Version != Version || m.NetworkID == uuid.Nil || m.MembershipID == uuid.Nil || m.ServerID == uuid.Nil || m.Generation < 1 || m.ControllerGeneration < 0 || !prefix.Contains(own) || own == prefix.Addr() || !PublicKey(m.PublicKey) || m.ListenPort < 1024 || m.ListenPort > 65535 || len(m.Peers) >= MaxMembers || len(m.Exports) > 200 || len(m.Egress) > 400 {
		return bad()
	}
	peers := map[uuid.UUID]Peer{}
	addresses := map[string]bool{m.Address: true}
	keys := map[string]bool{m.PublicKey: true}
	for _, p := range m.Peers {
		addr, err := netip.ParseAddr(p.Address)
		if err != nil || !prefix.Contains(addr) || addr == prefix.Addr() || p.Address != addr.String() || p.ServerID == uuid.Nil || p.ServerID == m.ServerID || p.MembershipID == uuid.Nil || peers[p.ServerID].ServerID != uuid.Nil || addresses[p.Address] || keys[p.PublicKey] || !PublicKey(p.PublicKey) || !TransportEndpoint(p.Endpoint) {
			return bad()
		}
		peers[p.ServerID] = p
		addresses[p.Address] = true
		keys[p.PublicKey] = true
	}
	ids := map[uuid.UUID]bool{}
	ports := map[int]bool{}
	for _, e := range m.Exports {
		if e.ID == uuid.Nil || e.UnitID == uuid.Nil || e.ResourceID == uuid.Nil || e.InstanceID == uuid.Nil || !port(e.Port) || e.ContainerPort < 1 || e.ContainerPort > 65535 || ids[e.ID] || ports[e.Port] || len(e.Sources) == 0 || len(e.Sources) > 64 {
			return bad()
		}
		ids[e.ID] = true
		ports[e.Port] = true
		sources := map[Source]bool{}
		for _, s := range e.Sources {
			if s.UnitID == uuid.Nil || peers[s.ServerID].ServerID == uuid.Nil || sources[s] {
				return bad()
			}
			sources[s] = true
		}
	}
	seen := map[string]bool{}
	for _, e := range m.Egress {
		key := fmt.Sprintf("%s/%s/%s/%d/%t", e.UnitID, e.ServerID, e.EndpointID, e.Port, e.Ingress)
		if e.UnitID == uuid.Nil || e.EndpointID == uuid.Nil || peers[e.ServerID].ServerID == uuid.Nil || !port(e.Port) || seen[key] {
			return bad()
		}
		seen[key] = true
	}
	if m.Disabled && (len(m.Exports) > 0 || len(m.Egress) > 0 || len(m.Peers) > 0) {
		return bad()
	}
	return nil
}
func (c Command) Validate() error {
	if c.NetworkID == uuid.Nil || c.MembershipID == uuid.Nil {
		return errors.New("invalid network command identity")
	}
	if c.Action == "identify" && c.Manifest == nil && c.Baseline == nil {
		return nil
	}
	if c.Action != "configure" && c.Action != "restrict" || c.Manifest == nil || c.Manifest.NetworkID != c.NetworkID || c.Manifest.MembershipID != c.MembershipID {
		return errors.New("invalid network command")
	}
	if c.Baseline != nil && (c.Action != "restrict" || c.Baseline.Validate() != nil || !Restricts(*c.Baseline, *c.Manifest)) {
		return errors.New("invalid network restriction baseline")
	}
	return c.Manifest.Validate()
}

// Restricts permits recovery admission only when the installed authority shrinks.
func Restricts(old, next Manifest) bool {
	// An identical persisted intent may be replayed after effects or a receipt
	// were interrupted; it introduces no authority beyond that same intent.
	if old.Generation == next.Generation && Hash(old) == Hash(next) {
		return next.Validate() == nil
	}

	if !sameLocalIdentity(old, next) || old.ControllerGeneration > next.ControllerGeneration || next.Generation <= old.Generation || next.Validate() != nil {
		return false
	}
	for _, p := range next.Peers {
		if !slices.Contains(old.Peers, p) {
			return false
		}
	}
	for _, e := range next.Egress {
		if !slices.Contains(old.Egress, e) {
			return false
		}
	}
	for _, e := range next.Exports {
		found := false
		for _, prior := range old.Exports {
			if e.ID != prior.ID {
				continue
			}
			found = true
			if e.UnitID != prior.UnitID || e.ResourceID != prior.ResourceID || e.InstanceID != prior.InstanceID || e.Port != prior.Port || e.ContainerPort != prior.ContainerPort {
				return false
			}
			for _, s := range e.Sources {
				if !slices.Contains(prior.Sources, s) {
					return false
				}
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// Canonical ordering makes reviewed manifests identical to reconciled database state.
func (m *Manifest) Sort() {
	sort.Slice(m.Peers, func(i, j int) bool { return m.Peers[i].ServerID.String() < m.Peers[j].ServerID.String() })
	sort.Slice(m.Exports, func(i, j int) bool { return m.Exports[i].ID.String() < m.Exports[j].ID.String() })
	for i := range m.Exports {
		s := m.Exports[i].Sources
		sort.Slice(s, func(i, j int) bool {
			if s[i].ServerID == s[j].ServerID {
				return s[i].UnitID.String() < s[j].UnitID.String()
			}
			return s[i].ServerID.String() < s[j].ServerID.String()
		})
	}
	sort.Slice(m.Egress, func(i, j int) bool {
		a, b := m.Egress[i], m.Egress[j]
		if a.UnitID == b.UnitID {
			return a.EndpointID.String() < b.EndpointID.String()
		}
		return a.UnitID.String() < b.UnitID.String()
	})
}

// Restriction applies revocations while a placement owns the host, without
// granting any pending additions from that placement's desired manifest.
func Restriction(installed, desired Manifest) Manifest {
	next := installed
	next.Peers = nil
	next.Exports = nil
	next.Egress = nil
	for _, peer := range installed.Peers {
		if slices.Contains(desired.Peers, peer) {
			next.Peers = append(next.Peers, peer)
		}
	}
	retainedPeer := func(id uuid.UUID) bool {
		return slices.ContainsFunc(next.Peers, func(p Peer) bool { return p.ServerID == id })
	}
	for _, e := range installed.Exports {
		for _, wanted := range desired.Exports {
			if e.ID != wanted.ID {
				continue
			}
			e.Sources = slices.DeleteFunc(slices.Clone(e.Sources), func(s Source) bool { return !retainedPeer(s.ServerID) || !slices.Contains(wanted.Sources, s) })
			if len(e.Sources) > 0 {
				next.Exports = append(next.Exports, e)
			}
		}
	}
	for _, egress := range installed.Egress {
		if retainedPeer(egress.ServerID) && slices.Contains(desired.Egress, egress) {
			next.Egress = append(next.Egress, egress)
		}
	}
	next.Disabled = desired.Disabled
	next.Sort()
	removed := next.Disabled != installed.Disabled || len(next.Peers) != len(installed.Peers) || len(next.Exports) != len(installed.Exports) || len(next.Egress) != len(installed.Egress)
	for _, endpoint := range next.Exports {
		for _, prior := range installed.Exports {
			if endpoint.ID == prior.ID && len(endpoint.Sources) != len(prior.Sources) {
				removed = true
			}
		}
	}
	// Pending additions belong to the reviewed placement's links phase. A
	// no-op restriction must not consume that phase's frozen generation.
	if removed {
		next.Generation = desired.Generation
	}
	return next
}
