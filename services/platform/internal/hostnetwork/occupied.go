package hostnetwork

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// defaultLikeBits is the shortest route treated as naming a network. Shorter routes, such
// as the 0.0.0.0/1 and 128.0.0.0/1 pair a VPN installs to replace the default route, say
// nothing about which private ranges are in use, like the default route itself.
const defaultLikeBits = 8

type hostRoute struct {
	prefix netip.Prefix
	dev    string
}

// routes reads every IPv4 route in every table, of every type, including local addresses.
func (h Host) routes(ctx context.Context) ([]hostRoute, error) {
	raw, err := h.run(ctx, "ip", nil, "-j", "-4", "route", "show", "table", "all")
	if err != nil {
		return nil, err
	}
	return parseRoutes(raw)
}

func parseRoutes(raw []byte) ([]hostRoute, error) {
	var routes []struct {
		Dst string `json:"dst"`
		Dev string `json:"dev"`
	}
	if readToolJSON(raw, &routes) != nil {
		return nil, errors.New("cannot inspect host routes")
	}
	out := []hostRoute{}
	for _, r := range routes {
		if r.Dst == "default" || r.Dst == "" {
			continue
		}
		p, err := netip.ParsePrefix(r.Dst)
		if err != nil {
			a, e := netip.ParseAddr(r.Dst)
			if e != nil {
				continue
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		if p.Addr().Is4() {
			out = append(out, hostRoute{prefix: p.Masked(), dev: r.Dev})
		}
	}
	return out, nil
}

// parseNameservers reads the IPv4 nameservers from resolv.conf content.
func parseNameservers(raw []byte) []netip.Prefix {
	out := []netip.Prefix{}
	lines := bufio.NewScanner(bytes.NewReader(raw))
	for lines.Scan() {
		fields := strings.Fields(lines.Text())
		if len(fields) < 2 || fields[0] != "nameserver" {
			continue
		}
		if a, err := netip.ParseAddr(fields[1]); err == nil && a.Is4() && !a.IsLoopback() {
			out = append(out, netip.PrefixFrom(a, 32))
		}
	}
	return out
}

// ManagedPrefixes returns the prefix of every managed network this host holds, enabled
// or not, since a disabled network can be enabled again with the same prefix.
func (h Host) ManagedPrefixes() ([]netip.Prefix, error) {
	entries, err := os.ReadDir(filepath.Join(h.Dir, "network"))
	if os.IsNotExist(err) {
		return []netip.Prefix{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := []netip.Prefix{}
	for _, entry := range entries {
		name, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok {
			continue
		}
		id, err := uuid.Parse(name)
		if err != nil {
			return nil, errors.New("invalid network state filename")
		}
		s, err := h.Snapshot(id)
		if err != nil {
			return nil, err
		}
		p, err := Prefix(s.Manifest.Prefix)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// Occupied returns the IPv4 ranges a new container network on this host must not overlap:
// every route that names a network, in any table, so a LAN, a VPN, a static route through
// a gateway or a blackhole the owner installed; the host's nameservers; and the prefix of
// each managed network the host holds, which installs only /32 routes for its members.
// Docker's own subnet choice skips only directly connected routes. Docker refuses overlaps
// with its own networks, which callers add.
func (h Host) Occupied(ctx context.Context) ([]netip.Prefix, error) {
	out, err := h.ManagedPrefixes()
	if err != nil {
		return nil, err
	}
	host, err := h.hostOccupied(ctx)
	if err != nil {
		return nil, err
	}
	return append(out, host...), nil
}

func occupiedFrom(routes []hostRoute, resolvers ...[]byte) []netip.Prefix {
	out := []netip.Prefix{}
	for _, r := range routes {
		if r.prefix.Bits() >= defaultLikeBits {
			out = append(out, r.prefix)
		}
	}
	for _, raw := range resolvers {
		out = append(out, parseNameservers(raw)...)
	}
	return out
}
