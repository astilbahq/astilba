//go:build linux

package hostnetwork

import (
	"context"
	"os"
	"strings"
	"syscall"
)

func (h Host) Supported(ctx context.Context) bool {
	if _, err := os.Stat("/sys/module/wireguard"); err != nil {
		return false
	}
	fd, err := syscall.Socket(syscall.AF_NETLINK, syscall.SOCK_RAW, syscall.NETLINK_ROUTE)
	if err != nil {
		return false
	}
	syscall.Close(fd)
	wg, err := h.run(ctx, "wg", nil, "--version")
	if err != nil || !strings.Contains(string(wg), "v"+WireGuardVersion) {
		return false
	}
	nft, err := h.run(ctx, "nft", nil, "--version")
	if err != nil || !strings.HasPrefix(string(nft), "nftables v"+NFTVersion+" ") {
		return false
	}
	_, err = h.run(ctx, "ip", nil, "-Version")
	return err == nil
}
