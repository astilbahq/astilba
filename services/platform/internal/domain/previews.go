package domain

import (
	"github.com/astilbahq/astilba/services/platform/internal/stablejson"
	"github.com/distribution/reference"
	"github.com/google/uuid"
)

// PreviewLimits is frozen into every preview plan and enforced by the agent.
type PreviewLimits struct {
	CPUs      int `json:"cpus"`
	MemoryMiB int `json:"memoryMiB"`
	Pids      int `json:"pids"`
}

func (l PreviewLimits) Validate() error {
	if l.CPUs < 1 || l.CPUs > 4 || l.MemoryMiB < 128 || l.MemoryMiB > 8192 || l.Pids < 32 || l.Pids > 512 {
		return Invalid("preview.limits")
	}
	return nil
}

type PreviewIdentity struct {
	RequestID  uuid.UUID     `json:"requestId"`
	Generation int64         `json:"generation"`
	Limits     PreviewLimits `json:"limits"`
}

func (p PreviewIdentity) Validate(s Specification) error {
	if s.HasReplicaConfiguration() || s.Placement != nil {
		return Invalid("preview.replicas")
	}
	if p.RequestID == uuid.Nil || p.Generation < 1 {
		return Invalid("preview.identity")
	}
	if err := p.Limits.Validate(); err != nil {
		return err
	}
	if s.Compose != nil || len(s.Applications) > 5 || len(s.Volumes) > 5 || len(s.Routes) > 5 {
		return Invalid("preview.specification")
	}
	for _, r := range s.Routes {
		if r.Protocol != "http" {
			return Invalid("preview.routes")
		}
	}
	return nil
}

func ValidatePreviewTemplate(s Specification, target uuid.UUID) error {
	if s.HasReplicaConfiguration() || s.Placement != nil {
		return Invalid("preview.replicas")
	}
	if s.Compose != nil || len(s.Applications) > 5 || len(s.Volumes) > 5 || len(s.Routes) > 5 {
		return Invalid("preview.specification")
	}
	for _, r := range s.Routes {
		if r.Protocol != "http" {
			return Invalid("preview.routes")
		}
	}
	found := false
	for _, a := range s.Applications {
		found = found || a.ID == target
		if a.ID == target && (a.ServiceKind() == "postgres" || len(a.Mounts) > 0) {
			return Invalid("preview.statelessBuildTarget")
		}
		if a.Source.Kind != "image" || a.Source.Image == nil || a.Source.ArtifactID != nil {
			return Invalid("preview.templateImages")
		}
		named, err := reference.ParseNormalizedNamed(*a.Source.Image)
		if err != nil {
			return Invalid("preview.templateImages")
		}
		if d, ok := named.(reference.Digested); !ok || d.Digest().Algorithm() != "sha256" {
			return Invalid("preview.templateImages")
		}
	}
	if !found {
		return Invalid("preview.applicationId")
	}
	return nil
}

// RemapPreview copies intent, not live resources, artifacts or production routes.
// Secret mapping is supplied from the separately scoped template store.
func RemapPreview(source Specification, ids, secrets map[uuid.UUID]uuid.UUID, hostname func(uuid.UUID) string) (Specification, error) {
	s, err := RemapSpecification(source, ids, secrets, "preview")
	if err != nil {
		return s, err
	}
	routes := []Route{}
	for _, r := range s.Routes {
		r.Domain = hostname(r.TargetID)
		if r.Domain == "" {
			continue
		}
		r.TLS = "managed"
		r.ListenPort = 0
		routes = append(routes, r)
	}
	s.Routes = routes
	raw, err := stablejson.Marshal(s)
	if err != nil {
		return s, err
	}
	s, _, err = ParseSpec(raw)
	return s, err
}
