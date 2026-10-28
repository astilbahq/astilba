package hostnetwork

import (
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const WireGuardVersion = "1.0.20210914"
const NFTVersion = "1.0.9"

// Bridge identities are observed from owned Docker resources, never supplied by a peer.
type ProxyPath struct {
	Name   string `json:"name"`
	Prefix string `json:"prefix"`
}
type Bridge struct {
	Proxy   []ProxyPath `json:"proxy,omitempty"`
	UnitID  uuid.UUID   `json:"unitId"`
	Name    string      `json:"name"`
	Sources []string    `json:"sources"`
}
type Snapshot struct {
	InterfaceGroup uint32   `json:"interfaceGroup"`
	Manifest       Manifest `json:"manifest"`
	Bridges        []Bridge `json:"bridges"`
	AppliedHash    string   `json:"appliedHash"`
}
type Host struct{ Dir, WG, IP, NFT string }

func (h Host) tool(name string) string {
	switch name {
	case "wg":
		if h.WG != "" {
			return h.WG
		}
	case "ip":
		if h.IP != "" {
			return h.IP
		}
	case "nft":
		if h.NFT != "" {
			return h.NFT
		}
	}
	return name
}
func (h Host) run(ctx context.Context, name string, input []byte, args ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, h.tool(name), args...)
	if input != nil {
		c.Stdin = bytes.NewReader(input)
	}
	var out boundedBuffer
	c.Stdout = &out
	c.Stderr = io.Discard
	if err := c.Run(); err != nil {
		return nil, fmt.Errorf("managed network %s operation failed", name)
	}
	if out.overflow {
		return nil, errors.New("managed network inspection exceeded its bound")
	}
	return out.Bytes(), nil
}

type boundedBuffer struct {
	bytes.Buffer
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	space := (1 << 20) - b.Len()
	if n > space {
		b.overflow = true
		p = p[:max(0, space)]
	}
	_, _ = b.Buffer.Write(p)
	return n, nil
}
func (h Host) identityPath(id uuid.UUID) string {
	return filepath.Join(h.Dir, "network", id.String()+".key")
}
func privateRead(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("network state must be a private regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 1<<20))
}
func atomicPrivate(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return errors.New("network state directory must be private")
	}
	f, err := os.CreateTemp(dir, ".network-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (h Host) Identity(id uuid.UUID) (Identity, error) {
	unlock, err := h.lock()
	if err != nil {
		return Identity{}, err
	}
	defer unlock()
	return h.identity(id)
}
func (h Host) identity(id uuid.UUID) (Identity, error) {
	out := Identity{MembershipID: id}
	if id == uuid.Nil {
		return out, errors.New("membership identity required")
	}
	path := h.identityPath(id)
	raw, err := privateRead(path)
	if os.IsNotExist(err) {
		key, e := ecdh.X25519().GenerateKey(rand.Reader)
		if e != nil {
			return out, e
		}
		raw = []byte(base64.StdEncoding.EncodeToString(key.Bytes()))
		if err = atomicPrivate(path, raw); err != nil {
			return out, err
		}
	} else if err != nil {
		return out, err
	}
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil {
		return out, errors.New("invalid local network key")
	}
	key, err := ecdh.X25519().NewPrivateKey(b)
	if err != nil {
		return out, errors.New("invalid local network key")
	}
	out.PublicKey = base64.StdEncoding.EncodeToString(key.PublicKey().Bytes())
	return out, nil
}
func (h Host) path(id uuid.UUID) string { return filepath.Join(h.Dir, "network", id.String()+".json") }
func (h Host) Snapshot(id uuid.UUID) (Snapshot, error) {
	var s Snapshot
	b, err := privateRead(h.path(id))
	if err != nil {
		return s, err
	}
	if err = Decode(b, &s); err != nil {
		return s, err
	}
	if s.Manifest.NetworkID != id || s.Manifest.Validate() != nil {
		return s, errors.New("invalid installed network identity")
	}
	return s, nil
}
func Decode(raw []byte, out any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil || d.Decode(new(any)) != io.EOF {
		return errors.New("invalid managed network encoding")
	}
	return nil
}
func (h Host) save(s Snapshot) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return atomicPrivate(h.path(s.Manifest.NetworkID), b)
}
func (h Host) Restore(ctx context.Context) error {
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return h.restore(ctx)
}
func (h Host) restore(ctx context.Context) error {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "network"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id, e := uuid.Parse(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return errors.New("invalid network state filename")
		}
		s, e := h.Snapshot(id)
		if e != nil {
			return e
		}
		if e = h.apply(ctx, s.Manifest, s.Bridges, false); e != nil {
			return e
		}
	}
	return nil
}
func (h Host) Apply(ctx context.Context, m Manifest, bridges []Bridge, restrict bool) error {
	unlock, err := h.lock()
	if err != nil {
		return err
	}
	defer unlock()
	return h.apply(ctx, m, bridges, restrict)
}
func (h Host) apply(ctx context.Context, m Manifest, bridges []Bridge, restrict bool) error {
	if runtime.GOOS != "linux" || m.Validate() != nil {
		return errors.New("managed links require a valid Linux network manifest")
	}
	identity, err := h.identity(m.MembershipID)
	if err != nil {
		return err
	}
	if identity.PublicKey != m.PublicKey {
		return errors.New("managed network public key does not match local identity")
	}
	old, err := h.Snapshot(m.NetworkID)
	if err == nil {
		if old.Manifest.ServerID != m.ServerID || old.Manifest.ControllerGeneration > m.ControllerGeneration || old.Manifest.Generation > m.Generation || old.Manifest.Generation == m.Generation && Hash(old.Manifest) != Hash(m) {
			return errors.New("managed network generation conflict")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	group := old.InterfaceGroup
	recoverCreate := err == nil && group != 0
	if group == 0 {
		var entropy [4]byte
		if _, e := rand.Read(entropy[:]); e != nil {
			return e
		}
		group = binary.BigEndian.Uint32(entropy[:]) & 0x7fffffff
		if group == 0 {
			group = 1
		}
	}
	if restrict && (err != nil || !Restricts(old.Manifest, m)) {
		return errors.New("network recovery cannot expand installed authority")
	}
	rules, err := Rules(m, bridges)
	if err != nil {
		return err
	}
	if err = h.inspectInterface(ctx, m, group, recoverCreate); err != nil {
		return err
	}
	if err = h.inspectRoutes(ctx, m); err != nil {
		return err
	}
	if err = h.save(Snapshot{InterfaceGroup: group, Manifest: m, Bridges: bridges}); err != nil {
		return err
	}
	// The owned filter is installed before any new peer or published port is enabled.
	if err = h.publishRules(ctx, m, rules); err != nil {
		return err
	}
	if _, err = h.run(ctx, "ip", nil, "link", "show", "dev", m.Interface()); err != nil {
		if _, err = h.run(ctx, "ip", nil, "link", "add", "dev", m.Interface(), "group", strconv.FormatUint(uint64(group), 10), "type", "wireguard"); err != nil {
			return err
		}
	}
	// The kernel preserves group at creation but applies ifalias only via SETLINK.
	// The private intent plus that atomic marker identifies an interrupted create.
	if _, err = h.run(ctx, "ip", nil, "link", "set", "dev", m.Interface(), "alias", m.Alias()); err != nil {
		return err
	}
	if _, err = h.run(ctx, "ip", nil, "address", "replace", m.Address+"/32", "dev", m.Interface()); err != nil {
		return err
	}
	private, err := privateRead(h.identityPath(m.MembershipID))
	if err != nil {
		return err
	}
	var config strings.Builder
	fmt.Fprintf(&config, "[Interface]\nPrivateKey = %s\nListenPort = %d\n", strings.TrimSpace(string(private)), m.ListenPort)
	for _, p := range m.Peers {
		fmt.Fprintf(&config, "\n[Peer]\nPublicKey = %s\nAllowedIPs = %s/32\nEndpoint = %s\nPersistentKeepalive = 25\n", p.PublicKey, p.Address, p.Endpoint)
	}
	f, err := os.CreateTemp(filepath.Join(h.Dir, "network"), ".wg-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.WriteString(config.String())
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if _, err = h.run(ctx, "wg", nil, "setconf", m.Interface(), f.Name()); err != nil {
		return err
	}
	if _, err = h.run(ctx, "ip", nil, "link", "set", "dev", m.Interface(), "mtu", "1280", "up"); err != nil {
		return err
	}
	for _, p := range m.Peers {
		if _, err = h.run(ctx, "ip", nil, "route", "replace", p.Address+"/32", "dev", m.Interface(), "src", m.Address); err != nil {
			return err
		}
	}
	for _, p := range old.Manifest.Peers {
		if !slices.ContainsFunc(m.Peers, func(next Peer) bool { return next.Address == p.Address }) {
			if _, err = h.run(ctx, "ip", nil, "route", "del", p.Address+"/32", "dev", m.Interface()); err != nil {
				return err
			}
		}
	}
	if m.Disabled {
		if _, err = h.run(ctx, "ip", nil, "link", "set", "dev", m.Interface(), "down"); err != nil {
			return err
		}
	}
	actual, err := h.run(ctx, "wg", nil, "show", m.Interface(), "public-key")
	if err != nil || strings.TrimSpace(string(actual)) != m.PublicKey {
		return errors.New("managed network identity verification failed")
	}
	return h.save(Snapshot{InterfaceGroup: group, Manifest: m, Bridges: bridges, AppliedHash: Hash(m)})
}
func (h Host) inspectInterface(ctx context.Context, m Manifest, group uint32, recoverCreate bool) error {
	raw, err := h.run(ctx, "ip", nil, "-N", "-j", "-d", "link", "show")
	if err != nil {
		return err
	}
	var links []struct {
		IfName string          `json:"ifname"`
		Alias  string          `json:"ifalias"`
		Group  json.RawMessage `json:"group"`
		Info   struct {
			Kind string `json:"info_kind"`
		} `json:"linkinfo"`
	}
	if json.Unmarshal(raw, &links) != nil {
		return errors.New("cannot inspect host interfaces")
	}
	for _, l := range links {
		if l.IfName != m.Interface() {
			continue
		}
		var observed uint32
		var text string
		if json.Unmarshal(l.Group, &observed) != nil {
			if json.Unmarshal(l.Group, &text) != nil {
				return errors.New("cannot inspect interface ownership marker")
			}
			value, err := strconv.ParseUint(text, 10, 32)
			if err != nil {
				return errors.New("invalid interface ownership marker")
			}
			observed = uint32(value)
		}
		if l.Info.Kind != "wireguard" || observed != group || l.Alias != m.Alias() && !(recoverCreate && l.Alias == "") {
			return errors.New("managed interface name is occupied by unrelated state")
		}
	}

	return nil
}
func (h Host) inspectRoutes(ctx context.Context, m Manifest) error {
	raw, err := h.run(ctx, "ip", nil, "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return err
	}
	var routes []struct {
		Dst string `json:"dst"`
		Dev string `json:"dev"`
	}
	if json.Unmarshal(raw, &routes) != nil {
		return errors.New("cannot inspect host routes")
	}
	prefix, _ := Prefix(m.Prefix)
	for _, r := range routes {
		if r.Dst == "default" || r.Dst == "" || r.Dev == m.Interface() {
			continue
		}
		p, e := netip.ParsePrefix(r.Dst)
		if e != nil {
			if a, e2 := netip.ParseAddr(r.Dst); e2 == nil {
				p = netip.PrefixFrom(a, 32)
			} else {
				continue
			}
		}
		if prefix.Overlaps(p) {
			return errors.New("managed network conflicts with an existing host route")
		}
	}
	return nil
}
func table(m Manifest) string { return "astilba_" + strings.ReplaceAll(m.NetworkID.String(), "-", "") }
func (h Host) publishRules(ctx context.Context, m Manifest, rules string) error {
	raw, err := h.run(ctx, "nft", nil, "-j", "list", "tables")
	if err != nil {
		return err
	}
	var state struct {
		Items []struct {
			Table *struct {
				Name    string `json:"name"`
				Family  string `json:"family"`
				Comment string `json:"comment"`
			} `json:"table"`
		} `json:"nftables"`
	}
	if json.Unmarshal(raw, &state) != nil {
		return errors.New("cannot inspect host firewall")
	}
	prefix := ""
	for _, item := range state.Items {
		if item.Table != nil && item.Table.Family == "inet" && item.Table.Name == table(m) {
			if item.Table.Comment != m.Alias() {
				return errors.New("managed firewall name is occupied by unrelated state")
			}
			prefix = "delete table inet " + table(m) + "\n"
		}
	}
	_, err = h.run(ctx, "nft", []byte(prefix+rules), "-f", "-")
	return err
}

var bridgeName = regexp.MustCompile(`^(br-[a-f0-9]{12}|as[a-f0-9]{12})$`)

func Rules(m Manifest, bridges []Bridge) (string, error) {
	if m.Validate() != nil {
		return "", errors.New("invalid managed firewall intent")
	}
	byUnit := map[uuid.UUID]Bridge{}
	for _, b := range bridges {
		if b.UnitID == uuid.Nil || !bridgeName.MatchString(b.Name) || len(b.Sources) > 100 || byUnit[b.UnitID].UnitID != uuid.Nil {
			return "", errors.New("invalid owned bridge identity")
		}
		for _, s := range b.Sources {
			a, e := netip.ParsePrefix(s)
			if e != nil || !a.Addr().Is4() || !a.Addr().IsPrivate() || a.Bits() < 8 || a != a.Masked() || a.String() != s {
				return "", errors.New("invalid owned bridge source")
			}
		}
		if len(b.Proxy) > 50 {
			return "", errors.New("too many ingress network attachments")
		}
		for _, path := range b.Proxy {
			ip, err := netip.ParsePrefix(path.Prefix)
			if err != nil || !ip.Addr().Is4() || !ip.Addr().IsPrivate() || ip.Bits() < 8 || ip != ip.Masked() || !bridgeName.MatchString(path.Name) {
				return "", errors.New("invalid owned ingress attachment")
			}
		}
		byUnit[b.UnitID] = b
	}
	peers := map[uuid.UUID]Peer{}
	for _, p := range m.Peers {
		peers[p.ServerID] = p
	}
	var in, forward strings.Builder
	// Destination filtering runs after conntrack and before Docker DNAT.
	for _, e := range m.Exports {
		for _, s := range e.Sources {
			p := peers[s.ServerID]
			fmt.Fprintf(&in, "iifname %q ip saddr %s ip daddr %s tcp dport %d accept\n", m.Interface(), p.Address, m.Address, e.Port)
			b, ok := byUnit[e.UnitID]
			if !ok {
				return "", errors.New("export unit bridge is unavailable")
			}
			for _, ip := range b.Sources {
				fmt.Fprintf(&forward, "iifname %q ip saddr %s oifname %q ip daddr %s tcp sport %d ct state established ct direction reply accept\n", b.Name, ip, m.Interface(), p.Address, e.ContainerPort)
			}
		}
	}
	for _, e := range m.Egress {
		b, ok := byUnit[e.UnitID]
		if !ok {
			return "", errors.New("egress unit bridge is unavailable")
		}
		p := peers[e.ServerID]
		for _, ip := range b.Sources {
			fmt.Fprintf(&forward, "iifname %q ip saddr %s oifname %q ip daddr %s tcp dport %d accept\n", b.Name, ip, m.Interface(), p.Address, e.Port)
		}
		if e.Ingress {
			if len(b.Proxy) == 0 {
				return "", errors.New("the owned ingress proxy must be running before cross-host placement")
			}
			for _, path := range b.Proxy {
				fmt.Fprintf(&forward, "iifname %q ip saddr %s oifname %q ip daddr %s tcp dport %d accept\n", path.Name, path.Prefix, m.Interface(), p.Address, e.Port)
			}
		}
		fmt.Fprintf(&in, "iifname %q ip saddr %s ip daddr %s tcp sport %d ct state established ct direction reply accept\n", m.Interface(), p.Address, m.Address, e.Port)
	}
	fmt.Fprintf(&in, "iifname %q drop\nip daddr %s drop\n", m.Interface(), m.Address)
	fmt.Fprintf(&forward, "oifname %q drop\n", m.Interface())
	return fmt.Sprintf("table inet %s {\ncomment %q\nchain incoming { type filter hook prerouting priority -150; policy accept;\n%s}\nchain outgoing { type filter hook forward priority -10; policy accept;\n%s}\nchain host_outgoing { type filter hook output priority -10; policy accept; oifname %q drop; }\n}\n", table(m), m.Alias(), in.String(), forward.String(), m.Interface()), nil
}

// StableInventory hashes unbounded containers by identity, not their changing usage.
func StableInventory(ids []string) string { sort.Strings(ids); return Hash(ids) }
