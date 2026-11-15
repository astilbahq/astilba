package agent

import (
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// A mirror's host and path as BuildKit's configuration names them. Docker has already parsed
// them as a URL; these keep anything that could end a TOML string out of the configuration.
var (
	builderMirrorHost   = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9.-]+)(:[0-9]{1,5})?$`)
	builderMirrorPrefix = regexp.MustCompile(`^[A-Za-z0-9._~/-]*$`)
)

// builderConfig is rootless BuildKit's buildkitd.toml for one build.
//
//   - The build's own registry, when it pushes to one, is reached only over verified HTTPS.
//   - Docker Hub images are read through the daemon's HTTPS Docker Hub mirrors, in Docker's
//     order, as the daemon and the agent's own image reads do, and from Docker Hub itself when
//     no mirror has the image or all fail. Pushes go to the registry itself.
//   - RUN steps use the host's resolvers, since BuildKit replaces Docker's loopback resolver.
//
// A plain HTTP mirror is left out. BuildKit takes a mirror's transport from the registry entry
// for the mirror's host, so it would read every Docker Hub tag through it without TLS, and
// any image named on that host too. A mirror's answer for a tag is not checked against
// anything, so builds read tags only over TLS. The build network refuses a private mirror the
// runner hasn't allowed, which BuildKit then passes over, and the build's refusal note says so.
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
		if mirror.plainHTTP || !builderMirrorHost.MatchString(mirror.host) || !builderMirrorPrefix.MatchString(mirror.prefix) {
			continue
		}
		name := mirror.host
		if mirror.prefix != "" {
			name += "/" + mirror.prefix
		}
		hub := at("docker.io")
		if !slices.Contains(hub.mirrors, name) {
			hub.mirrors = append(hub.mirrors, name)
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

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, strconv.Quote(value))
	}
	return strings.Join(quoted, ", ")
}
