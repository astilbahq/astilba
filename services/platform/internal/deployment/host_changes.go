package deployment

import (
	"strconv"
	"strings"
)

// Kinds of host change an agent reports. Management words them for review, so a host cannot
// put its own text in front of the owner who approves it.
const (
	HostChangePorts            = "ports"
	HostChangeCertificateEmail = "certificateEmail"
	HostChangeManagedEgress    = "managedEgress"
)

// UnreportedHostChangesEffect explains a plan reviewed because its server's agent did not say what
// applying it would change on the host.
const UnreportedHostChangesEffect = "Review host changes: this server's agent does not report what this plan would change on the host. Update the agent so approved permissions carry forward again."

// effectLength is the longest plan effect the API describes.
const effectLength = 300

// ElevatedServicesEffect explains a plan reviewed because services with host-level access, or
// services that can reach them, changed since the applied deployment. It names as many of them
// as fit within an effect's length, however many changed.
func ElevatedServicesEffect(names []string) string {
	const before, after = "Review host-level access: ", " differs from what the applied deployment ran, and runs privileged, adds Linux capabilities or shares a volume or network with a service that does."
	list := ""
	for i, name := range names {
		next := name
		if i > 0 {
			next = list + ", " + name
		}
		rest := ""
		if more := len(names) - i - 1; more > 0 {
			rest = " and " + strconv.Itoa(more) + " more"
		}
		if len(before)+len(next)+len(rest)+len(after) > effectLength {
			return before + list + " and " + strconv.Itoa(len(names)-i) + " more" + after
		}
		list = next
	}
	return before + list + after
}

type HostChange struct {
	Kind string `json:"kind"`
	// Ports are the host ports a "ports" change starts publishing.
	Ports []int `json:"ports,omitempty"`
}

// HostChangeEffect is the reviewed wording of an agent-reported host change.
func HostChangeEffect(c HostChange) string {
	switch c.Kind {
	case HostChangePorts:
		// Within the reviewed effect's length, however many ports a change adds.
		shown := c.Ports[:min(len(c.Ports), 12)]
		ports := make([]string, len(shown))
		for i, p := range shown {
			ports[i] = strconv.Itoa(p)
		}
		list := strings.Join(ports, ", ")
		if more := len(c.Ports) - len(shown); more > 0 {
			list += " and " + strconv.Itoa(more) + " more"
		}
		return "Publish host ports " + list + " through the owned Traefik proxy, restarting it."
	case HostChangeCertificateEmail:
		return "Set the host's certificate contact email for managed TLS."
	default:
		return "Turn on managed egress filtering for this host's services, restarting the owned Traefik proxy."
	}
}
