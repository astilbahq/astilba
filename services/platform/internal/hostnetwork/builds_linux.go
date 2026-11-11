//go:build linux

package hostnetwork

import (
	"context"
	"net/netip"
	"os"
	"strings"
	"syscall"
)

// BuildPolicySupported reports whether this agent can install the build network policy: it
// needs the pinned nft and a netfilter netlink socket, which the agent's unit must permit.
func (h Host) BuildPolicySupported(ctx context.Context) bool {
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_NETFILTER)
	if err != nil {
		return false
	}
	syscall.Close(fd)
	nft, err := h.run(ctx, "nft", nil, "--version")
	if err != nil || !strings.HasPrefix(string(nft), "nftables v"+NFTVersion+" ") {
		return false
	}
	_, err = h.run(ctx, "nft", nil, "list", "tables")
	return err == nil
}

// BuildResolvers returns the IPv4 nameservers builds use, at most three, or none when the
// host has only a loopback stub, leaving BuildKit's public fallback.
func BuildResolvers() ([]netip.Addr, error) {
	var contents [2][]byte
	for i, path := range []string{"/run/systemd/resolve/resolv.conf", "/etc/resolv.conf"} {
		raw, err := readBounded(path)
		if err != nil && !os.IsNotExist(err) {
			return nil, err
		}
		contents[i] = raw
	}
	return buildResolvers(contents[0], contents[1])
}
