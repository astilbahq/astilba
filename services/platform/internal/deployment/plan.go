package deployment

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/astilbahq/astilba/services/platform/internal/pipeline"
	"github.com/distribution/reference"
	"github.com/google/uuid"
)

const Recipe = 1
const ObservationLimit = 24000
const ProxyImage = "traefik:v3.7.16@sha256:22a759fb803abb5c3a7ba5d5a55a201c5fb0fc8993d5c72c47a89075e64f1953"

type Plan struct {
	Artifacts         map[string]pipeline.Artifact `json:"artifacts,omitempty"`
	ID                uuid.UUID                    `json:"id"`
	ServerID          uuid.UUID                    `json:"serverId"`
	EnvironmentID     uuid.UUID                    `json:"environmentId"`
	UnitID            uuid.UUID                    `json:"unitId"`
	RevisionID        uuid.UUID                    `json:"revisionId"`
	ExpectedAppliedID *uuid.UUID                   `json:"expectedAppliedId"`
	Recipe            int                          `json:"recipe"`
	Specification     domain.Specification         `json:"specification"`
	Images            map[string]string            `json:"images"`
	HostChanges       bool                         `json:"hostChanges"`
	ExpectedState     string                       `json:"expectedState"`
	AcceptDrift       bool                         `json:"acceptDrift"`
	Effects           []string                     `json:"effects"`
	Hash              string                       `json:"hash"`
	ExpiresAt         time.Time                    `json:"expiresAt"`
}

type Payload struct {
	RegistryAccess map[string]pipeline.Credential `json:"registryAccess,omitempty"`
	Action         string                         `json:"action"`
	Plan           Plan                           `json:"plan"`
	Secrets        map[string]string              `json:"secrets,omitempty"`
	VolumeID       *uuid.UUID                     `json:"volumeId,omitempty"`
}

func (p Payload) Digest() string { p.Secrets = nil; p.RegistryAccess = nil; return Hash(p) }

type Resource struct {
	ID          uuid.UUID `json:"id"`
	Kind        string    `json:"kind"`
	State       string    `json:"state"`
	Image       string    `json:"image"`
	Drift       bool      `json:"drift"`
	Note        string    `json:"note"`
	Logs        string    `json:"logs"`
	MemoryBytes int64     `json:"memoryBytes"`
}
type Observation struct {
	ObservedAt          time.Time         `json:"observedAt"`
	Resources           []Resource        `json:"resources"`
	Images              map[string]string `json:"images,omitempty"`
	Applied             bool              `json:"applied"`
	StateHash           string            `json:"stateHash"`
	OmittedResources    int               `json:"omittedResources,omitempty"`
	ResourceDrift       bool              `json:"resourceDrift"`
	InfrastructureDrift bool              `json:"infrastructureDrift"`
}
type Status struct {
	ID          uuid.UUID `json:"id"`
	OperationID uuid.UUID `json:"operationId"`
	State       string    `json:"state"`
	Plan        *Plan     `json:"plan"`
}

func Hash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (p Plan) WithHash() Plan { p.Hash = ""; p.Hash = Hash(p); return p }
func (p Plan) Validate(resolved bool) error {
	h := p.Hash
	p.Hash = ""
	if h != Hash(p) || p.Recipe != Recipe || p.ID == uuid.Nil || p.ServerID == uuid.Nil || p.EnvironmentID == uuid.Nil || p.UnitID == uuid.Nil || p.RevisionID == uuid.Nil {
		return errors.New("invalid deployment plan identity")
	}
	raw, err := json.Marshal(p.Specification)
	if err != nil {
		return err
	}
	normalized, _, err := domain.ParseSpec(raw)
	if err != nil {
		return err
	}
	if p.Specification.Compose != nil && (p.Specification.Compose.Namespace != p.UnitID || Hash(normalized) != Hash(p.Specification)) {
		return errors.New("Compose projection or namespace mismatch")
	}
	if p.Specification.Compose != nil {
		source, err := SourceProject(p, nil)
		if err != nil {
			return err
		}
		if len(source.Permissions) > 0 && !p.HostChanges {
			return errors.New("Compose permissions require owner-approved host changes")
		}
	}
	if len(domain.SecretReferences(p.Specification)) > 100 {
		return errors.New("too many execution secret references")
	}
	if len(p.Specification.Applications) > 20 {
		return errors.New("a visual unit supports at most twenty services")
	}
	if len(p.Images) != 0 && len(p.Images) != len(p.Specification.Applications) {
		return errors.New("incomplete image resolution")
	}
	for _, a := range p.Specification.Applications {
		if a.Source.Kind != "image" || a.Source.Image == nil {
			return errors.New("Git sources require the Git build workflow; select an image")
		}
		if _, err := reference.ParseNormalizedNamed(*a.Source.Image); err != nil {
			return errors.New("invalid image reference")
		}
		if resolved {
			image, ok := p.Images[a.ID.String()]
			artifact, built := p.Artifacts[a.ID.String()]
			local := built && artifact.ServerID != nil
			if built && (artifact.Image != *a.Source.Image || artifact.ServerID != nil && *artifact.ServerID != p.ServerID || artifact.Architecture != "amd64" && artifact.Architecture != "arm64") {
				return errors.New("artifact identity mismatch")
			}
			if !ok || local && image != artifact.Digest || !local && !ResolvedImage(*a.Source.Image, image) {
				return errors.New("image identity does not match its source")
			}
		}
	}
	return nil
}
func ResolvedImage(source, image string) bool {
	a, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return false
	}
	b, err := reference.ParseNormalizedNamed(image)
	if err != nil || reference.FamiliarName(a) != reference.FamiliarName(b) {
		return false
	}
	d, ok := b.(reference.Digested)
	if !ok || d.Digest().Algorithm() != "sha256" || d.Digest().Validate() != nil {
		return false
	}
	if expected, ok := a.(reference.Digested); ok && expected.Digest() != d.Digest() {
		return false
	}
	return true
}
func DigestReference(source, digest string) (string, error) {
	a, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return "", err
	}
	v := reference.FamiliarName(a) + "@" + digest
	if !ResolvedImage(source, v) {
		return "", errors.New("invalid resolved image")
	}
	return v, nil
}
func UnitName(id uuid.UUID) string    { return "astilba-" + id.String() }
func ServiceName(id uuid.UUID) string { return "service-" + id.String() }
func VolumeName(id uuid.UUID) string  { return "astilba-volume-" + id.String() }
func NetworkName(id uuid.UUID) string { return "astilba-network-" + id.String() }

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

// Compose receives execution values through stdin. Exports
// use placeholders; neither the reviewed plan nor the stored specification has values.
func Compose(p Plan, secrets map[string]string, export bool) ([]byte, error) {
	return ComposeAt(p, secrets, export, ".")
}
func ComposeAt(p Plan, secrets map[string]string, export bool, root string) ([]byte, error) {
	if err := p.Validate(true); err != nil {
		return nil, err
	}
	if p.Specification.Compose != nil {
		return sourceCompose(p, secrets, root, export)
	}
	value := func(id uuid.UUID) (string, error) {
		if export {
			return "${ASTILBA_SECRET_" + strings.ReplaceAll(id.String(), "-", "") + ":?Set the exported secret}", nil
		}
		v, ok := secrets[id.String()]
		if !ok || len(v) > 65536 || strings.ContainsRune(v, 0) {
			return "", errors.New("missing execution secret")
		}
		return v, nil
	}
	escape := func(v string) string {
		if export {
			return v
		}
		return strings.ReplaceAll(v, "$", "$$")
	}
	vars := func(in []domain.Variable, env map[string]string) error {
		for _, v := range in {
			if v.Value != nil {
				env[v.Name] = strings.ReplaceAll(*v.Value, "$", "$$")
			} else {
				x, e := value(*v.SecretVersionID)
				if e != nil {
					return e
				}
				env[v.Name] = escape(x)
			}
		}
		return nil
	}
	apps := map[uuid.UUID]domain.Application{}
	for _, a := range p.Specification.Applications {
		apps[a.ID] = a
	}
	services, volumes := map[string]any{}, map[string]any{}
	for _, v := range p.Specification.Volumes {
		volumes[VolumeName(v.ID)] = map[string]any{"name": VolumeName(v.ID), "external": true}
	}
	for _, a := range p.Specification.Applications {
		env := map[string]string{}
		if err := vars(p.Specification.Variables, env); err != nil {
			return nil, err
		}
		if err := vars(a.Variables, env); err != nil {
			return nil, err
		}
		if a.Postgres != nil {
			pass, e := value(a.Postgres.PasswordSecretVersionID)
			if e != nil {
				return nil, e
			}
			env["POSTGRES_PASSWORD"] = escape(pass)
			env["POSTGRES_DB"] = a.Postgres.Database
			env["POSTGRES_USER"] = a.Postgres.Username
		}
		for _, b := range p.Specification.Bindings {
			if b.TargetID != a.ID {
				continue
			}
			db := apps[b.SourceID]
			pass, e := value(db.Postgres.PasswordSecretVersionID)
			if e != nil {
				return nil, e
			}
			u := url.URL{Scheme: "postgresql", Host: ServiceName(db.ID) + ":5432", User: url.UserPassword(db.Postgres.Username, pass), Path: "/" + db.Postgres.Database, RawQuery: "sslmode=disable"}
			if export {
				u.User = url.User(db.Postgres.Username)
				env[b.Variable] = strings.Replace(u.String(), db.Postgres.Username+"@", db.Postgres.Username+":${ASTILBA_SECRET_"+strings.ReplaceAll(db.Postgres.PasswordSecretVersionID.String(), "-", "")+"_URLENCODED:?Set URL-encoded database password}@", 1)
			} else {
				env[b.Variable] = escape(u.String())
			}
		}
		labels := map[string]string{"com.astilba.server": p.ServerID.String(), "com.astilba.environment": p.EnvironmentID.String(), "com.astilba.unit": p.UnitID.String(), "com.astilba.resource": a.ID.String(), "com.astilba.kind": "visual-v1", "com.astilba.plan": p.Hash}
		service := map[string]any{"image": p.Images[a.ID.String()], "container_name": UnitName(a.ID), "restart": "unless-stopped", "environment": env, "labels": labels, "networks": map[string]any{"unit": map[string]any{"aliases": []string{ServiceName(a.ID)}}}, "logging": map[string]any{"driver": "json-file", "options": map[string]string{"max-size": "10m", "max-file": "3"}}}
		if len(a.Command) > 0 {
			command := make([]string, len(a.Command))
			for i, arg := range a.Command {
				command[i] = strings.ReplaceAll(arg, "$", "$$")
			}
			service["command"] = command
		}
		mounts := []any{}
		for _, m := range a.Mounts {
			mounts = append(mounts, map[string]any{"type": "volume", "source": VolumeName(m.VolumeID), "target": m.Target, "read_only": m.ReadOnly, "volume": map[string]bool{"nocopy": false}})
		}
		if len(mounts) > 0 {
			service["volumes"] = mounts
		}
		if a.Postgres != nil {
			service["healthcheck"] = map[string]any{"test": []string{"CMD", "pg_isready", "-U", a.Postgres.Username, "-d", a.Postgres.Database}, "interval": "2s", "timeout": "3s", "retries": 30}
		}
		// HTTP readiness is checked by the agent from the private unit network;
		// arbitrary web images need not contain curl or a shell.
		if len(domain.Dependencies(p.Specification, a)) > 0 {
			deps := map[string]any{}
			for _, id := range domain.Dependencies(p.Specification, a) {
				condition := "service_started"
				if apps[id].Postgres != nil {
					condition = "service_healthy"
				}
				deps[ServiceName(id)] = map[string]string{"condition": condition}
			}
			service["depends_on"] = deps
		}
		services[ServiceName(a.ID)] = service
	}
	return json.MarshalIndent(map[string]any{"name": UnitName(p.UnitID), "services": services, "volumes": volumes, "networks": map[string]any{"unit": map[string]any{"name": NetworkName(p.UnitID), "external": true}}}, "", "  ")
}

type Backend struct {
	Address string `json:"address"`
}
type RouteSet struct {
	EnvironmentID uuid.UUID          `json:"environmentId"`
	UnitID        uuid.UUID          `json:"unitId"`
	Routes        []domain.Route     `json:"routes"`
	Backends      map[string]Backend `json:"backends"`
}

func RouteConfig(sets map[string]RouteSet) ([]byte, error) {
	hr, hs, hm, tr, ts, tm := map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}, map[string]any{}
	domains, ports := map[string]bool{}, map[int]bool{}
	ids := make([]string, 0, len(sets))
	for k := range sets {
		ids = append(ids, k)
	}
	sort.Strings(ids)
	for _, id := range ids {
		set := sets[id]
		if id != set.UnitID.String() || set.UnitID == uuid.Nil || set.EnvironmentID == uuid.Nil {
			return nil, errors.New("route registry unit mismatch")
		}
		for _, r := range set.Routes {
			if err := domain.ValidateRoute(r); err != nil {
				return nil, err
			}
			name := UnitName(set.UnitID) + "-" + r.ID.String()
			backend, ok := set.Backends[r.TargetID.String()]
			if !ok || backend.Address != ServiceName(r.TargetID) {
				return nil, errors.New("foreign backend reference")
			}
			address := fmt.Sprintf("%s:%d", backend.Address, r.Port)
			if r.Protocol == "http" {
				if domains[r.Domain] {
					return nil, errors.New("domain is already claimed")
				}
				domains[r.Domain] = true
				hs[name] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]string{"url": "http://" + address}}}}
				router := map[string]any{"rule": "Host(`" + r.Domain + "`)", "service": name, "entryPoints": []string{"web"}}
				middlewares := []string{}
				if len(r.AllowCIDRs) > 0 {
					key := name + "-access"
					hm[key] = map[string]any{"ipAllowList": map[string]any{"sourceRange": r.AllowCIDRs}}
					middlewares = append(middlewares, key)
				}
				if a := r.Advanced; a != nil {
					if len(a.ResponseHeaders) > 0 {
						key := name + "-headers"
						hm[key] = map[string]any{"headers": map[string]any{"customResponseHeaders": a.ResponseHeaders}}
						middlewares = append(middlewares, key)
					}
					if a.StripPrefix != "" {
						key := name + "-prefix"
						hm[key] = map[string]any{"stripPrefix": map[string]any{"prefixes": []string{a.StripPrefix}}}
						middlewares = append(middlewares, key)
					}
					if a.RequestsPerSecond > 0 {
						key := name + "-rate"
						hm[key] = map[string]any{"rateLimit": map[string]int{"average": a.RequestsPerSecond, "burst": a.RequestsPerSecond * 2}}
						middlewares = append(middlewares, key)
					}
				}
				if len(middlewares) > 0 {
					router["middlewares"] = middlewares
				}
				if r.TLS == "managed" {
					router["entryPoints"] = []string{"websecure"}
					router["tls"] = map[string]string{"certResolver": "letsencrypt"}
				}
				hr[name] = router
			} else {
				if ports[r.ListenPort] {
					return nil, errors.New("TCP listener is already claimed")
				}
				ports[r.ListenPort] = true
				ts[name] = map[string]any{"loadBalancer": map[string]any{"servers": []any{map[string]string{"address": address}}}}
				router := map[string]any{"rule": "HostSNI(`*`)", "service": name, "entryPoints": []string{fmt.Sprintf("tcp-%d", r.ListenPort)}}
				key := name + "-access"
				tm[key] = map[string]any{"ipAllowList": map[string]any{"sourceRange": r.AllowCIDRs}}
				router["middlewares"] = []string{key}
				if r.TLS == "passthrough" {
					router["rule"] = "HostSNI(`" + r.Domain + "`)"
					router["tls"] = map[string]bool{"passthrough": true}
				}
				tr[name] = router
			}
		}
	}
	out := map[string]any{}
	for kind, values := range map[string]map[string]map[string]any{"http": {"routers": hr, "services": hs, "middlewares": hm}, "tcp": {"routers": tr, "services": ts, "middlewares": tm}} {
		group := map[string]any{}
		for key, value := range values {
			if len(value) > 0 {
				group[key] = value
			}
		}
		if len(group) > 0 {
			out[kind] = group
		}
	}
	return json.Marshal(out)
}

// An image change cannot reinterpret an existing PostgreSQL data directory.
func PostgresVersionChange(old, next domain.Specification) error {
	for _, a := range old.Applications {
		if a.Postgres == nil || a.Source.Image == nil || len(a.Mounts) != 1 {
			continue
		}
		for _, b := range next.Applications {
			if b.Postgres != nil && b.Source.Image != nil && len(b.Mounts) == 1 && a.Mounts[0].VolumeID == b.Mounts[0].VolumeID && *a.Source.Image != *b.Source.Image {
				return errors.New("changing a PostgreSQL version requires a supported upgrade or new storage")
			}
		}
	}
	return nil
}
