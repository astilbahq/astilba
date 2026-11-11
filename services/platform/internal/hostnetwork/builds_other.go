//go:build !linux

package hostnetwork

import (
	"context"
	"errors"
	"net/netip"
)

func (Host) BuildPolicySupported(context.Context) bool { return false }

func BuildResolvers() ([]netip.Addr, error) {
	return nil, errors.New("builds require a Linux host")
}
