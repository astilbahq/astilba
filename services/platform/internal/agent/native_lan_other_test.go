//go:build !linux

package agent

import (
	"net"
	"testing"
)

// Native tests stop before reaching these elsewhere than Linux.
type nativeLAN struct{ Address string }

func startNativeLAN(t *testing.T, _ ...string) nativeLAN {
	t.Error("native fixtures need a Linux network namespace")
	return nativeLAN{}
}

func (nativeLAN) listen(t *testing.T, _ string) net.Listener {
	t.Error("native fixtures need a Linux network namespace")
	return nil
}
