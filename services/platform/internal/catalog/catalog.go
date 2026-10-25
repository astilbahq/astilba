// Package catalog owns the small curated set of visual service templates.
package catalog

import (
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

type Support struct {
	Level  string `json:"level"`
	Detail string `json:"detail"`
}
type Maintenance struct {
	Deployment Support `json:"deployment"`
	Binding    Support `json:"binding"`
	Backup     Support `json:"backup"`
	Restore    Support `json:"restore"`
	Upgrade    Support `json:"upgrade"`
}
type Recipe struct {
	ID            string               `json:"id"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Documentation string               `json:"documentation"`
	Prerequisites []string             `json:"prerequisites"`
	NeedsPassword bool                 `json:"needsPassword"`
	Specification domain.Specification `json:"specification"`
	Support       Maintenance          `json:"support"`
}

const NginxImage = "nginx:1.30.6-alpine3.24@sha256:47928bf67eb62a1863cb08d9ecb52d3e704304ba614eaee17884204677702951"
const ValkeyImage = "valkey/valkey:9.1.2-alpine3.24@sha256:48332870af354a799964c0012ae1194a0bf2bf894eb508f945810596dc2d8d11"
const UptimeKumaImage = "louislam/uptime-kuma:2.5.7@sha256:4b95dbe7ac5343b768f8e04e3035ccd59997872b1a35ab9b0f082d2cc279070e"
const RegistryImage = "registry:3.1.2@sha256:01cd312e67026f06f5d89c3e133a4a9adc7cb9eeb9b1194e0b2954dfd770efd2"

// Template identities are public placeholders. Staging replaces them with fresh
// IDs and replaces the PostgreSQL credential with an existing environment secret.
func template(id, name, kind, image string, port int, path, mount string) domain.Specification {
	a := domain.Application{ID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("catalog/"+id+"/service")), Name: name, Kind: kind, Source: domain.ApplicationSource{Kind: "image", Image: &image}}
	s := domain.Specification{Version: 1, Variables: []domain.Variable{}}
	if port != 0 {
		a.Outputs = []domain.ServiceOutput{{Name: "PORT", Type: "port", Port: port}, {Name: "URL", Type: "url", Scheme: "http", Port: port, Path: path}}
		if kind == "web" {
			a.Health = &domain.HealthCheck{Port: port, Path: path}
		}
	}
	if mount != "" {
		volume := uuid.NewSHA1(uuid.NameSpaceURL, []byte("catalog/"+id+"/volume"))
		s.Volumes = []domain.Volume{{ID: volume, Name: name + " data"}}
		a.Mounts = []domain.VolumeMount{{VolumeID: volume, Target: mount}}
	}
	s.Applications = []domain.Application{a}
	return s
}

func Recipes() []Recipe {
	private := "No public route is created. Review authentication and access before exposing this service."
	deployment := Support{"tested", "Pinned image, private deployment and restart on the supported native Docker baseline."}
	binding := Support{"tested", "Named private host, port and connection outputs."}
	manualBackup := Support{"manual", "Stopped-volume snapshots are available; application-specific recovery is not validated for this recipe."}
	manualRestore := Support{"manual", "Restore into fresh storage and verify the application before accepting writes."}
	manualUpgrade := Support{"manual", "Review upstream compatibility and a verified backup. No supported major-upgrade recipe."}
	stateless := Support{"not_applicable", "No persistent application data in this recipe."}
	base := Maintenance{deployment, binding, manualBackup, manualRestore, manualUpgrade}
	pg := Recipe{ID: "postgresql-18", Name: "PostgreSQL", Description: "PostgreSQL 18 with a retained data volume and secret-backed connection values.", Documentation: "https://hub.docker.com/_/postgres", Prerequisites: []string{private, "Select an existing password secret. Initialization settings do not change credentials in an existing volume."}, NeedsPassword: true, Specification: template("postgresql-18", "PostgreSQL", "postgres", domain.PostgresImage, 0, "", "/var/lib/postgresql"), Support: base}
	pg.Specification.Applications[0].Postgres = &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.NewSHA1(uuid.NameSpaceURL, []byte("catalog/password-placeholder"))}
	pg.Support.Backup = Support{"tested", "PostgreSQL logical backup using the existing encrypted off-server backup policy."}
	pg.Support.Restore = Support{"tested", "Logical restore into fresh storage with a verified application check."}
	pg.Support.Upgrade = Support{"tested", "Only the existing PostgreSQL 17.10 → 18.4 workflow; later major versions require a new recipe."}
	cache := Recipe{ID: "valkey-cache", Name: "Valkey cache", Description: "A private, disposable Redis-compatible cache. Data is intentionally not persisted.", Documentation: "https://valkey.io/topics/persistence/", Prerequisites: []string{private, "Unauthenticated within this deployment unit. Use only for data that can be rebuilt; startup is not a readiness guarantee."}, Specification: template("valkey-cache", "Valkey cache", "worker", ValkeyImage, 6379, "", ""), Support: base}
	cache.Specification.Applications[0].Outputs[1].Scheme = "redis"
	cache.Specification.Applications[0].Command = []string{"valkey-server", "--save", "", "--appendonly", "no", "--maxmemory", "128mb", "--maxmemory-policy", "allkeys-lru"}
	cache.Support.Backup, cache.Support.Restore = stateless, stateless
	nginx := Recipe{ID: "nginx", Name: "Nginx", Description: "A static HTTP server with the upstream welcome page and HTTP readiness.", Documentation: "https://hub.docker.com/_/nginx", Prerequisites: []string{private, "Add your own image or reviewed configuration to serve application content."}, Specification: template("nginx", "Nginx", "web", NginxImage, 80, "/", ""), Support: base}
	nginx.Support.Backup, nginx.Support.Restore = stateless, stateless
	kuma := Recipe{ID: "uptime-kuma", Name: "Uptime Kuma", Description: "Uptime monitoring with a retained local data volume.", Documentation: "https://github.com/louislam/uptime-kuma/wiki/%F0%9F%94%A7-How-to-Install", Prerequisites: []string{private, "Create the initial administrator through a restricted route before granting public access. Docker socket monitoring is not enabled."}, Specification: template("uptime-kuma", "Uptime Kuma", "web", UptimeKumaImage, 3001, "/", "/app/data"), Support: base}
	registry := Recipe{ID: "oci-registry", Name: "OCI registry", Description: "A private Distribution registry with retained local storage; separate from Astilba build registry credentials.", Documentation: "https://distribution.github.io/distribution/about/deploying/", Prerequisites: []string{private, "The template is unauthenticated. Configure registry authentication and TLS before publishing; the Docker host cannot use a private container hostname as a remote build registry."}, Specification: template("oci-registry", "OCI registry", "web", RegistryImage, 5000, "/v2/", "/var/lib/registry"), Support: base}
	none := "none"
	registry.Specification.Applications[0].Variables = []domain.Variable{{Name: "OTEL_TRACES_EXPORTER", Value: &none}}
	return []Recipe{pg, cache, nginx, kuma, registry}
}
