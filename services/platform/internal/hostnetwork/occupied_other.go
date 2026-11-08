//go:build !linux

package hostnetwork

import (
	"context"
	"net/netip"
)

// Agents are supported only on Linux. Elsewhere, as in development against a Docker VM,
// this machine's routes are not the Docker host's, so none are reported.
func (Host) hostOccupied(context.Context) ([]netip.Prefix, error) { return []netip.Prefix{}, nil }
