package agent

import (
	"net"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// builderMirrorHost is a mirror's host as BuildKit's configuration names it. Docker has already
// parsed it as a URL; this keeps anything that could end a TOML string, or that strconv.Quote
// would escape in a way TOML doesn't accept, out of the configuration.
var builderMirrorHost = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)

// builderConfig is rootless BuildKit's buildkitd.toml for one build.
//
//   - The build's own registry, when it pushes to one, is reached only over verified HTTPS.
//   - Docker Hub images are read through the daemon's Docker Hub mirrors, in Docker's order, and
//     from Docker Hub itself when no mirror has the image or all fail. Pushes go to the registry
//     itself.
//   - RUN steps use the host's resolvers, since BuildKit replaces Docker's loopback resolver.
//
// BuildKit can't read some mirrors as Docker does, so builds leave them out:
//   - A plain HTTP mirror. BuildKit takes a mirror's transport from the registry entry for the
//     mirror's host, so it would read every Docker Hub tag through it without TLS, and any
//     image named on that host too. A mirror's answer for a tag is not checked against
//     anything, so builds read tags only over TLS.
//   - A mirror on loopback. It names the host's own loopback, which the build's network
//     namespace doesn't share, and BuildKit reads loopback without TLS.
//   - A mirror with a path. Docker reads it under that path, at <path>/v2/, and BuildKit under
//     its registry API, at /v2/<path>/: another repository, perhaps another registry's.
//
// The build network refuses a private mirror the runner hasn't allowed, which BuildKit then
// passes over, and the build's refusal note says so.
func builderConfig(registryHost string, mirrors []dockerHubMirror, nameservers []netip.Addr) string {
	type entry struct {
		mirrors  []string
		verified bool
	}
	registries := map[string]*entry{}
	at := func(host string) *entry {
		if registries[host] == nil {
			registries[host] = &entry{}
		}
		return registries[host]
	}
	if registryHost != "" {
		at(registryHost).verified = true
	}
	for _, mirror := range mirrors {
		if mirror.plainHTTP || mirror.prefix != "" || loopbackHost(mirror.host) || !builderMirrorHost.MatchString(mirror.host) {
			continue
		}
		if hub := at("docker.io"); !slices.Contains(hub.mirrors, mirror.host) {
			hub.mirrors = append(hub.mirrors, mirror.host)
		}
	}
	var b strings.Builder
	hosts := make([]string, 0, len(registries))
	for host := range registries {
		hosts = append(hosts, host)
	}
	slices.Sort(hosts)
	for _, host := range hosts {
		r := registries[host]
		b.WriteString("[registry." + strconv.Quote(host) + "]\n")
		if r.verified {
			b.WriteString("  http = false\n  insecure = false\n")
		}
		if len(r.mirrors) > 0 {
			b.WriteString("  mirrors = [" + quotedList(r.mirrors) + "]\n")
		}
	}
	if len(nameservers) > 0 {
		names := make([]string, 0, len(nameservers))
		for _, nameserver := range nameservers {
			names = append(names, nameserver.String())
		}
		b.WriteString("[dns]\n  nameservers = [" + quotedList(names) + "]\n")
	}
	return b.String()
}

// loopbackHost reports whether a registry host, with or without a port, is one BuildKit reads
// without TLS by default: localhost or a loopback address.
func loopbackHost(host string) bool {
	name, _, err := net.SplitHostPort(host)
	if err != nil {
		name = strings.Trim(host, "[]")
	}
	address, err := netip.ParseAddr(name)
	return strings.EqualFold(name, "localhost") || err == nil && address.IsLoopback()
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, ", ")
}
