//go:build linux

package hostnetwork

import (
	"context"
	"errors"
	"io"
	"net/netip"
	"os"
)

// Routes come from procfs, not from ip(8): the agent's unit has no AF_NETLINK until the
// owner prepares managed networking, and every deployment host must place networks.
const fibPath = "/proc/net/fib_trie"

// resolvers are read the way Docker reads them: systemd-resolved's upstream list as well
// as the stub file, since 127.0.0.53 hides the real servers.
var resolvers = []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"}

func hostOccupied(context.Context) ([]netip.Prefix, error) {
	raw, err := readBounded(fibPath)
	if err != nil {
		return nil, err
	}
	routes, err := parseFIB(raw)
	if err != nil {
		return nil, err
	}
	contents := [][]byte{}
	for _, path := range resolvers {
		raw, err := readBounded(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		contents = append(contents, raw)
	}
	return occupiedFrom(routes, contents...)
}

// readBounded refuses a file past its bound: a truncated route list would hide routes.
func readBounded(path string) ([]byte, error) {
	const bound = 16 << 20
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, bound+1))
	if err == nil && len(raw) > bound {
		err = errors.New("host network state exceeds its bound")
	}
	return raw, err
}
