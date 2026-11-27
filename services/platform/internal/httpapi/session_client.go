package httpapi

import (
	"net/http"
	"net/netip"
	"strings"
)

// sessionClient summarizes the request's client for the account page's list of sessions:
// coarse enough to tell which of the holder's devices and networks a session is on, and keeping
// neither the User-Agent nor the address.
func (s *Server) sessionClient(r *http.Request) sessionClient {
	browser, os := clientFamilies(r.UserAgent())
	return sessionClient{browser: browser, os: os, network: sessionNetwork(s.clientAddress(r))}
}

// clientFamilies reads the browser and operating system families from a User-Agent. A browser
// built on another names that one too, so each is matched before the one it names, and iOS and
// Android before the systems their User-Agents also name. Anything not recognized is "other",
// which says no more than an absent header would.
func clientFamilies(agent string) (browser, os string) {
	has := func(tokens ...string) bool {
		for _, token := range tokens {
			if strings.Contains(agent, token) {
				return true
			}
		}
		return false
	}
	switch {
	case has("Edg/", "EdgA/", "EdgiOS/"):
		browser = "edge"
	case has("OPR/", "OPT/", "Opera"):
		browser = "opera"
	case has("Firefox/", "FxiOS/"):
		browser = "firefox"
	case has("Chrome/", "CriOS/", "Chromium/"):
		browser = "chrome"
	case has("Safari/"):
		browser = "safari"
	default:
		browser = "other"
	}
	switch {
	case has("iPhone", "iPad", "iPod"):
		os = "ios"
	case has("Android"):
		os = "android"
	case has("CrOS"):
		os = "chromeos"
	case has("Windows"):
		os = "windows"
	case has("Macintosh", "Mac OS X"):
		os = "macos"
	case has("Linux"):
		os = "linux"
	default:
		os = "other"
	}
	return browser, os
}

// sessionNetwork is the network an address is in: its IPv4 /24 or IPv6 /48, about what a
// provider assigns one customer. That tells the holder's own networks from someone else's
// without keeping which address in it made the request. It is empty for an unknown address.
func sessionNetwork(address netip.Addr) string {
	switch {
	case !address.IsValid():
		return ""
	case address.Is4():
		return netip.PrefixFrom(address, 24).Masked().String()
	default:
		return netip.PrefixFrom(address, 48).Masked().String()
	}
}
