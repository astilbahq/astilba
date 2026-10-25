package domain

import (
	"crypto/sha256"
	"encoding/base32"
	"net/netip"
	"strings"

	"github.com/google/uuid"
)

// Application names bind all three identities, even when an app ID is reused in
// another environment. The server label makes placement visible in the name.
func ApplicationHostname(base string, ws, env, app, server uuid.UUID) string {
	sum := sha256.Sum256([]byte(ws.String() + ":" + env.String() + ":" + app.String()))
	label := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(sum[:]))
	return "a" + label + "." + ServerDomain(base, server)
}
func ServerDomain(base string, server uuid.UUID) string {
	return "s" + strings.ReplaceAll(server.String(), "-", "") + "." + base
}
func ValidHostname(value string) bool { return len(value) <= 253 && domainName.MatchString(value) }
func ValidApplicationDomain(base string) bool {
	return len(base) <= 165 && domainName.MatchString(base)
}
func PublicAddress(value string, v4 bool) (string, error) {
	if value == "" {
		return "", nil
	}
	a, e := netip.ParseAddr(value)
	if e != nil || a.Is4() != v4 || a.Is4In6() || !a.IsGlobalUnicast() || a.IsPrivate() || a.Zone() != "" {
		return "", Invalid("publicAddress")
	}
	return a.String(), nil
}

// GeneratedRouteServers validates namespace ownership before any host is chosen.
// The caller additionally checks these server IDs belong to the workspace.
func GeneratedRouteServers(base string, ws, env uuid.UUID, spec Specification, selected *uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	if base == "" {
		return out, nil
	}
	seen := map[uuid.UUID]bool{}
	for _, r := range spec.Routes {
		if r.Domain != base && !strings.HasSuffix(r.Domain, "."+base) {
			continue
		}
		labels := strings.Split(strings.TrimSuffix(r.Domain, "."+base), ".")
		if len(labels) != 2 || len(labels[1]) != 33 || labels[1][0] != 's' {
			return nil, Invalid("routes.generatedDomain")
		}
		server, e := uuid.Parse(labels[1][1:])
		if e != nil || server == uuid.Nil || selected != nil && server != *selected || r.Domain != ApplicationHostname(base, ws, env, r.TargetID, server) {
			return nil, Invalid("routes.generatedDomain")
		}
		if !seen[server] {
			out = append(out, server)
			seen[server] = true
		}
	}
	return out, nil
}
