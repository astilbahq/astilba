//go:build linux

package hostnetwork

import (
	"context"
	"io"
	"net/netip"
	"os"
)

// resolvers are read the way Docker reads them: systemd-resolved's upstream list as well
// as the stub file, since 127.0.0.53 hides the real servers.
var resolvers = []string{"/etc/resolv.conf", "/run/systemd/resolve/resolv.conf"}

func (h Host) hostOccupied(ctx context.Context) ([]netip.Prefix, error) {
	routes, err := h.routes(ctx)
	if err != nil {
		return nil, err
	}
	contents := [][]byte{}
	for _, path := range resolvers {
		f, err := os.Open(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		raw, err := io.ReadAll(io.LimitReader(f, 1<<20))
		f.Close()
		if err != nil {
			return nil, err
		}
		contents = append(contents, raw)
	}
	return occupiedFrom(routes, contents...), nil
}
