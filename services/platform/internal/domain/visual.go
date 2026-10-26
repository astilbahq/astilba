package domain

import (
	"net/netip"
	"path"
	"regexp"
	"strings"

	"github.com/google/uuid"
)

const Postgres17Image = "postgres:17.10-alpine3.23@sha256:8189a1f6e40904781fc9e2612687877791d21679866db58b1de996b31fc312e4"

const PostgresImage = "postgres:18.4-alpine3.23@sha256:996d0920e4ff9df1fc19dacb904492f3c1ec0ec1cc338f0ad7123be7731c5f5e"

type Volume struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`
}
type VolumeMount struct {
	VolumeID uuid.UUID `json:"volumeId"`
	Target   string    `json:"target"`
	ReadOnly bool      `json:"readOnly"`
}
type HealthCheck struct {
	Port int    `json:"port"`
	Path string `json:"path"`
}

const MaxReplicas = 10
const MaxRuntimeInstances = 40

func (a Application) ReplicaCount() int {
	if a.Replicas == 0 {
		return 1
	}
	return a.Replicas
}

func (s Specification) HasReplicaConfiguration() bool {
	for _, a := range s.Applications {
		if a.Replicas != 0 {
			return true
		}
	}
	return false
}

func (h HealthCheck) Valid() bool {
	return h.Port >= 1 && h.Port <= 65535 && len(h.Path) <= 256 && strings.HasPrefix(h.Path, "/") && !strings.ContainsAny(h.Path, "\r\n\x00")
}

type Postgres struct {
	Database                string    `json:"database"`
	Username                string    `json:"username"`
	PasswordSecretVersionID uuid.UUID `json:"passwordSecretVersionId"`
}
type Binding struct {
	Output   string    `json:"output,omitempty"`
	ID       uuid.UUID `json:"id"`
	SourceID uuid.UUID `json:"sourceId"`
	TargetID uuid.UUID `json:"targetId"`
	Variable string    `json:"variable"`
}
type Route struct {
	ID         uuid.UUID     `json:"id"`
	TargetID   uuid.UUID     `json:"targetId"`
	Protocol   string        `json:"protocol"`
	Domain     string        `json:"domain"`
	Port       int           `json:"port"`
	ListenPort int           `json:"listenPort"`
	TLS        string        `json:"tls"`
	AllowCIDRs []string      `json:"allowCidrs"`
	Advanced   *RouteOptions `json:"advanced,omitempty"`
}

// Advanced options are typed intent, never raw provider configuration or references.
type RouteOptions struct {
	ResponseHeaders   map[string]string `json:"responseHeaders,omitempty"`
	StripPrefix       string            `json:"stripPrefix,omitempty"`
	RequestsPerSecond int               `json:"requestsPerSecond,omitempty"`
}

func (a Application) ServiceKind() string {
	if a.Kind == "" {
		return "web"
	}
	return a.Kind
}

var dbName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var domainName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?)+$`)
var headerName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)

func ValidMount(target string) bool {
	if len(target) > 240 || !strings.HasPrefix(target, "/") || target == "/" || path.Clean(target) != target || strings.ContainsAny(target, "\x00\r\n\\") {
		return false
	}
	for _, root := range []string{"/proc", "/sys", "/dev", "/run", "/var/run"} {
		if target == root || strings.HasPrefix(target, root+"/") {
			return false
		}
	}
	return true
}
func ValidVariable(v Variable) bool {
	return variableName.MatchString(v.Name) && (v.Value == nil) != (v.SecretVersionID == nil) && (v.Value == nil || len(*v.Value) <= 4096 && !strings.ContainsRune(*v.Value, 0)) && (v.SecretVersionID == nil || *v.SecretVersionID != uuid.Nil)
}
func ValidateVisual(s Specification) error {
	if len(s.Volumes) > 50 || len(s.Bindings) > 100 || len(s.Routes) > 50 {
		return Invalid("specification")
	}
	ids, names := map[uuid.UUID]bool{}, map[string]bool{}
	apps, volumes := map[uuid.UUID]Application{}, map[uuid.UUID]bool{}
	runtimeCount := 0
	claim := func(id uuid.UUID) bool {
		if id == uuid.Nil || ids[id] {
			return false
		}
		ids[id] = true
		return true
	}
	for _, a := range s.Applications {
		if a.Replicas < 0 || a.Replicas > MaxReplicas {
			return Invalid("applications.replicas")
		}
		runtimeCount += a.ReplicaCount()
		if !claim(a.ID) {
			return Invalid("applications.id")
		}
		apps[a.ID] = a
	}
	if s.HasReplicaConfiguration() && (s.Compose != nil || runtimeCount > MaxRuntimeInstances) {
		return Invalid("applications.replicas")
	}
	for _, v := range s.Volumes {
		name, err := Name(v.Name)
		if !claim(v.ID) || err != nil || name != v.Name || names[strings.ToLower(name)] {
			return Invalid("volumes")
		}
		names[strings.ToLower(name)] = true
		volumes[v.ID] = true
	}
	variables := map[uuid.UUID]map[string]bool{}
	databaseVolumes := map[uuid.UUID]bool{}
	for _, a := range s.Applications {
		if s.Compose == nil && len(a.Networks) > 0 {
			return Invalid("applications.networks")
		}
		if a.ServiceKind() != "web" && a.ServiceKind() != "worker" && a.ServiceKind() != "postgres" {
			return Invalid("applications.kind")
		}
		if len(a.Variables) > 100 || len(a.Mounts) > 20 || len(a.DependsOn) > 50 || len(a.Command) > 32 {
			return Invalid("applications")
		}
		for _, arg := range a.Command {
			if len(arg) > 4096 || strings.ContainsRune(arg, 0) {
				return Invalid("applications.command")
			}
		}
		variables[a.ID] = map[string]bool{}
		for _, v := range s.Variables {
			variables[a.ID][v.Name] = true
		}
		for _, v := range a.Variables {
			if !ValidVariable(v) || variables[a.ID][v.Name] {
				return Invalid("applications.variables")
			}
			variables[a.ID][v.Name] = true
		}
		if err := validateOutputs(s, a); err != nil {
			return err
		}
		for i, m := range a.Mounts {
			if !volumes[m.VolumeID] || !ValidMount(m.Target) {
				return Invalid("applications.mounts")
			}
			for _, other := range a.Mounts[:i] {
				if m.VolumeID == other.VolumeID || m.Target == other.Target || strings.HasPrefix(m.Target, other.Target+"/") || strings.HasPrefix(other.Target, m.Target+"/") {
					return Invalid("applications.mounts")
				}
			}
		}
		if a.Health != nil && (a.ServiceKind() != "web" || !a.Health.Valid()) {
			return Invalid("applications.health")
		}
		if a.ReplicaCount() > 1 {
			if a.ServiceKind() != "web" || a.Health == nil || len(a.Mounts) != 0 || a.Postgres != nil {
				return Invalid("applications.replicas")
			}
			routed := false
			for _, route := range s.Routes {
				if route.TargetID == a.ID {
					if route.Protocol != "http" {
						return Invalid("applications.replicas")
					}
					routed = true
				}
			}
			if !routed {
				return Invalid("applications.replicas")
			}
			for _, b := range s.Bindings {
				if b.SourceID == a.ID {
					return Invalid("applications.replicas")
				}
			}
			for _, consumer := range s.Applications {
				for _, dependency := range Dependencies(s, consumer) {
					if dependency == a.ID {
						return Invalid("applications.replicas")
					}
				}
			}
		}
		if a.ServiceKind() == "postgres" {
			if a.Postgres == nil || !dbName.MatchString(a.Postgres.Database) || !dbName.MatchString(a.Postgres.Username) || a.Postgres.PasswordSecretVersionID == uuid.Nil || a.Source.Kind != "image" || a.Source.Image == nil || (*a.Source.Image != PostgresImage && *a.Source.Image != Postgres17Image) || len(a.Command) != 0 {
				return Invalid("applications.postgres")
			}
			if len(a.Mounts) != 1 || a.Mounts[0].Target != PostgresMount(*a.Source.Image) || a.Mounts[0].ReadOnly {
				return Invalid("applications.postgres.mounts")
			}
			if databaseVolumes[a.Mounts[0].VolumeID] {
				return Invalid("applications.postgres.mounts")
			}
			databaseVolumes[a.Mounts[0].VolumeID] = true
			for _, key := range []string{"POSTGRES_PASSWORD", "POSTGRES_USER", "POSTGRES_DB", "PGDATA", "POSTGRES_HOST_AUTH_METHOD", "POSTGRES_INITDB_ARGS", "POSTGRES_INITDB_WALDIR", "POSTGRES_PASSWORD_FILE"} {
				if variables[a.ID][key] {
					return Invalid("applications.postgres.variables")
				}
			}
		} else if a.Postgres != nil {
			return Invalid("applications.postgres")
		}
		seen := map[uuid.UUID]bool{}
		for _, id := range a.DependsOn {
			dep, ok := apps[id]
			if !ok || id == a.ID || seen[id] || (s.Compose == nil && dep.ServiceKind() != "postgres" && dep.Health == nil) {
				return Invalid("applications.dependsOn")
			}
			seen[id] = true
		}
	}
	for _, a := range s.Applications {
		if a.ServiceKind() != "postgres" {
			for _, m := range a.Mounts {
				if databaseVolumes[m.VolumeID] {
					return Invalid("applications.mounts")
				}
			}
		}
	}
	visiting, done := map[uuid.UUID]bool{}, map[uuid.UUID]bool{}
	var visit func(uuid.UUID) bool
	visit = func(id uuid.UUID) bool {
		if visiting[id] {
			return false
		}
		if done[id] {
			return true
		}
		visiting[id] = true
		for _, dep := range Dependencies(s, apps[id]) {
			if !visit(dep) {
				return false
			}
		}
		visiting[id] = false
		done[id] = true
		return true
	}
	for id := range apps {
		if !visit(id) {
			return Invalid("applications.dependsOn")
		}
	}
	for _, b := range s.Bindings {
		src, sok := apps[b.SourceID]
		dst, dok := apps[b.TargetID]
		_, outputOK := BindingOutput(src, b.Output)
		if !claim(b.ID) || !sok || !dok || b.SourceID == b.TargetID || !outputOK || dst.ServiceKind() == "postgres" || !variableName.MatchString(b.Variable) || variables[b.TargetID][b.Variable] {
			return Invalid("bindings")
		}
		variables[b.TargetID][b.Variable] = true
	}
	domains, ports := map[string]bool{}, map[int]bool{}
	for _, r := range s.Routes {
		a, ok := apps[r.TargetID]
		if !claim(r.ID) || !ok || a.ServiceKind() == "worker" || r.Port < 1 || r.Port > 65535 || len(r.Domain) > 253 || !domainName.MatchString(r.Domain) {
			return Invalid("routes")
		}
		if err := ValidateRoute(r); err != nil {
			return err
		}
		if r.Protocol == "http" {
			if a.ServiceKind() != "web" || domains[r.Domain] {
				return Invalid("routes")
			}
			domains[r.Domain] = true
		} else {
			if ports[r.ListenPort] {
				return Invalid("routes")
			}
			ports[r.ListenPort] = true
		}
	}
	return nil
}

func SecretReferences(s Specification) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := []uuid.UUID{}
	add := func(id uuid.UUID) {
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, v := range s.Variables {
		if v.SecretVersionID != nil {
			add(*v.SecretVersionID)
		}
	}
	for _, a := range s.Applications {
		for _, v := range a.Variables {
			if v.SecretVersionID != nil {
				add(*v.SecretVersionID)
			}
		}
		if a.Postgres != nil {
			add(a.Postgres.PasswordSecretVersionID)
		}
	}
	return out
}

// ValidateRoute is shared by specification admission and the durable host registry.
func ValidateRoute(r Route) error {
	if r.ID == uuid.Nil || r.TargetID == uuid.Nil || r.Port < 1 || r.Port > 65535 || len(r.Domain) > 253 || !domainName.MatchString(r.Domain) {
		return Invalid("routes")
	}
	if r.Protocol == "http" {
		if (r.TLS != "off" && r.TLS != "managed") || r.ListenPort != 0 {
			return Invalid("routes")
		}
	} else if r.Protocol == "tcp" {
		if r.ListenPort < 1024 || r.ListenPort > 65535 || r.ListenPort == 8080 || (r.TLS != "off" && r.TLS != "passthrough") || len(r.AllowCIDRs) == 0 || r.Advanced != nil {
			return Invalid("routes")
		}
	} else {
		return Invalid("routes.protocol")
	}
	if len(r.AllowCIDRs) > 32 {
		return Invalid("routes.allowCidrs")
	}
	for _, raw := range r.AllowCIDRs {
		if _, err := netip.ParsePrefix(raw); err != nil {
			return Invalid("routes.allowCidrs")
		}
	}
	if o := r.Advanced; o != nil {
		if len(o.ResponseHeaders) > 16 || o.RequestsPerSecond < 0 || o.RequestsPerSecond > 10000 || len(o.StripPrefix) > 128 || (o.StripPrefix != "" && (!strings.HasPrefix(o.StripPrefix, "/") || path.Clean(o.StripPrefix) != o.StripPrefix || strings.ContainsAny(o.StripPrefix, "?#{}`\r\n\x00"))) {
			return Invalid("routes.advanced")
		}
		for key, v := range o.ResponseHeaders {
			if !headerName.MatchString(key) || (!strings.HasPrefix(strings.ToLower(key), "x-") && key != "Content-Security-Policy" && key != "Referrer-Policy") || len(v) > 1024 || strings.ContainsAny(v, "\r\n\x00") || strings.Contains(v, "{{") {
				return Invalid("routes.advanced.responseHeaders")
			}
		}
	}
	return nil
}

// Legacy database bindings imply readiness. Named output references do not
// impose execution order; callers declare readiness with DependsOn.
func Dependencies(s Specification, a Application) []uuid.UUID {
	out := append([]uuid.UUID(nil), a.DependsOn...)
	for _, b := range s.Bindings {
		if b.TargetID == a.ID && b.Output == "" {
			found := false
			for _, id := range out {
				found = found || id == b.SourceID
			}
			if !found {
				out = append(out, b.SourceID)
			}
		}
	}
	return out
}

func PostgresMount(image string) string {
	if image == Postgres17Image {
		return "/var/lib/postgresql/data"
	}
	return "/var/lib/postgresql"
}
