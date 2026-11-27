package httpapi

import (
	"net/netip"
	"testing"
)

// Browsers built on another one name it in their User-Agent, and mobile systems name the
// desktop ones they derive from, so the summary must pick the most specific family.
func TestClientFamilies(t *testing.T) {
	for _, c := range []struct{ agent, browser, os string }{
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15", "safari", "macos"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0", "edge", "windows"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 OPR/115.0.0.0", "opera", "windows"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:133.0) Gecko/20100101 Firefox/133.0", "firefox", "linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) CriOS/131.0.6778.73 Mobile/15E148 Safari/604.1", "chrome", "ios"},
		{"Mozilla/5.0 (iPad; CPU OS 18_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) FxiOS/133.0 Mobile/15E148 Safari/605.1.15", "firefox", "ios"},
		{"Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Mobile Safari/537.36", "chrome", "android"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36", "chrome", "chromeos"},
		{"curl/8.7.1", "other", "other"},
		{"", "other", "other"},
	} {
		if browser, os := clientFamilies(c.agent); browser != c.browser || os != c.os {
			t.Errorf("%q: got %s on %s, want %s on %s", c.agent, browser, os, c.browser, c.os)
		}
	}
}

// A session keeps only the network its client is in, never the address.
func TestSessionNetwork(t *testing.T) {
	for _, c := range []struct{ address, network string }{
		{"203.0.113.77", "203.0.113.0/24"},
		{"2001:db8:1234:5678::9", "2001:db8:1234::/48"},
		{"", ""},
	} {
		address, _ := netip.ParseAddr(c.address)
		if got := sessionNetwork(address); got != c.network {
			t.Errorf("%q: got %q, want %q", c.address, got, c.network)
		}
	}
}
