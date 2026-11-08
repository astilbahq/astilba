package hostnetwork

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// defaultLikeBits is the shortest route treated as naming a network. Shorter routes, such
// as the 0.0.0.0/1 and 128.0.0.0/1 pair a VPN installs to replace the default route, say
// nothing about which private ranges are in use, like the default route itself.
const defaultLikeBits = 8

// parseFIB reads /proc/net/fib_trie, which lists every IPv4 route of every type in every
// table, local addresses included, without a netlink socket. Each leaf line names an
// address and is followed by one line per prefix length routed at it:
//
//	|-- 10.252.9.0
//	   /24 universe UNREACHABLE
//
// The +-- lines are the trie's internal nodes, not routes.
func parseFIB(raw []byte) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	var leaf netip.Addr
	lines := bufio.NewScanner(bytes.NewReader(raw))
	for lines.Scan() {
		line := strings.TrimSpace(lines.Text())
		if address, ok := strings.CutPrefix(line, "|-- "); ok {
			a, err := netip.ParseAddr(address)
			if err != nil || !a.Is4() {
				return nil, errors.New("cannot read the host's routes")
			}
			leaf = a
			continue
		}
		rest, ok := strings.CutPrefix(line, "/")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if !leaf.IsValid() || len(fields) == 0 {
			return nil, errors.New("cannot read the host's routes")
		}
		bits, err := strconv.Atoi(fields[0])
		if err != nil {
			return nil, errors.New("cannot read the host's routes")
		}
		p, err := leaf.Prefix(bits)
		if err != nil {
			return nil, errors.New("cannot read the host's routes")
		}
		out = append(out, p)
	}
	if lines.Err() != nil {
		return nil, errors.New("cannot read the host's routes")
	}
	return out, nil
}

// parseNameservers reads the IPv4 nameservers from resolv.conf content. A line it can't
// read could hide a nameserver, so it refuses rather than skip the rest.
func parseNameservers(raw []byte) ([]netip.Prefix, error) {
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
	if lines.Err() != nil {
		return nil, errors.New("cannot read the host's nameservers")
	}
	return out, nil
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
		// Snapshot has validated the prefix.
		p, _ := Prefix(s.Manifest.Prefix)
		out = append(out, p)
	}
	return out, nil
}

// Occupied returns the IPv4 ranges a new container network on this host must not overlap.
// Managed are the prefixes of the managed networks the host holds, which install only /32
// routes for their members. Routes are every route that names a network, in any table, so a
// LAN, a VPN, a static route through a gateway or a blackhole the owner installed, and the
// host's nameservers. Docker's own subnet choice skips only directly connected routes.
// Docker refuses overlaps with its own networks, which callers add.
func (h Host) Occupied(ctx context.Context) (managed, routes []netip.Prefix, err error) {
	if managed, err = h.ManagedPrefixes(); err != nil {
		return nil, nil, err
	}
	if routes, err = hostOccupied(ctx); err != nil {
		return nil, nil, err
	}
	return managed, routes, nil
}

func occupiedFrom(routes []netip.Prefix, resolvers ...[]byte) ([]netip.Prefix, error) {
	out := []netip.Prefix{}
	for _, r := range routes {
		if r.Bits() >= defaultLikeBits {
			out = append(out, r)
		}
	}
	for _, raw := range resolvers {
		servers, err := parseNameservers(raw)
		if err != nil {
			return nil, err
		}
		out = append(out, servers...)
	}
	return out, nil
}
