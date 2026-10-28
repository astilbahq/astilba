//go:build !linux

package hostnetwork

import "context"

func (Host) Supported(context.Context) bool { return false }
