package deployment

import (
	"strconv"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

// Effects lists what applying spec over previous does, for the reviewer to accept.
func Effects(spec, previous domain.Specification, hostChanges bool) []string {
	out := []string{"Pull the reviewed image digests and reconcile the deployment unit.", "Services may restart. No automatic image or database rollback is performed."}
	if hostChanges {
		out = append(out, "Install or restart the owned Traefik proxy and publish the reviewed host ports.")
	}
	current := map[uuid.UUID]bool{}
	for _, a := range spec.Applications {
		current[a.ID] = true
	}
	for _, a := range previous.Applications {
		if !current[a.ID] {
			out = append(out, "Remove service "+a.Name+"; retain its named volumes.")
		}
	}
	routes := map[uuid.UUID]bool{}
	for _, r := range spec.Routes {
		routes[r.ID] = true
	}
	for _, r := range previous.Routes {
		if !routes[r.ID] {
			out = append(out, "Remove route "+routeLabel(r)+"; this server stops accepting its traffic.")
		}
	}
	previousVolumes, currentVolumes := map[uuid.UUID]string{}, map[uuid.UUID]string{}
	for _, v := range previous.Volumes {
		previousVolumes[v.ID] = v.Name
	}
	for _, v := range spec.Volumes {
		currentVolumes[v.ID] = v.Name
	}
	for _, v := range previous.Volumes {
		if _, ok := currentVolumes[v.ID]; !ok {
			out = append(out, "Remove volume "+v.Name+" from the configuration; retain its data and existing backup identity.")
		}
	}
	for _, v := range spec.Volumes {
		if _, ok := previousVolumes[v.ID]; !ok {
			out = append(out, "Attach volume "+v.Name+" with a new identity. New storage starts empty; retained storage at that identity is reused.")
		}
	}
	for _, old := range previous.Applications {
		for _, next := range spec.Applications {
			if next.ID != old.ID {
				continue
			}
			for _, mount := range old.Mounts {
				destination := "no volume"
				same := false
				for _, replacement := range next.Mounts {
					if replacement.Target == mount.Target {
						destination = currentVolumes[replacement.VolumeID]
						same = replacement.VolumeID == mount.VolumeID
					}
				}
				if !same {
					out = append(out, "Change "+next.Name+" storage at "+mount.Target+" from "+previousVolumes[mount.VolumeID]+" to "+destination+"; data is not copied.")
				}
			}
		}
	}
	if len(spec.Volumes) > 0 {
		out = append(out, "Named data volumes survive service replacement and deletion. Export does not copy their data.")
	}
	for _, r := range spec.Routes {
		if r.TLS == "off" {
			out = append(out, "Traffic for "+r.Domain+" uses an unencrypted endpoint.")
		}
	}
	return out
}

func routeLabel(r domain.Route) string {
	if r.Protocol == "tcp" {
		return "TCP " + r.Domain + ":" + strconv.Itoa(r.ListenPort)
	}
	return r.Domain
}
