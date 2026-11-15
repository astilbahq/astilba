// Package catalog owns the small curated set of visual specifications people can stage:
// single-service recipes and multi-service app templates.
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

// Recipe is one catalog entry. A service recipe has one service; an app template has several,
// connected to each other. Both are an ordinary visual specification whose identities are
// public placeholders, which staging replaces (see Stage).
type Recipe struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	// Version is the upstream release an app template pins, empty for a service recipe.
	Version       string               `json:"version,omitempty"`
	Name          string               `json:"name"`
	Description   string               `json:"description"`
	Documentation string               `json:"documentation"`
	Prerequisites []string             `json:"prerequisites"`
	Specification domain.Specification `json:"specification"`
	// Secrets are the secret versions the specification references. Staging generates each one
	// into the environment's secret store unless the person names an existing version instead.
	Secrets []Secret `json:"secrets"`
	// Route is the public HTTP route staging adds, if the entry needs one.
	Route *RoutePreset `json:"route,omitempty"`
	// Backups are the policies the entry recommends once it is deployed.
	Backups []BackupPreset `json:"backups"`
	Support Maintenance    `json:"support"`
}

const (
	ServiceRecipe = "service"
	AppTemplate   = "app"
)

// Secret is a placeholder secret version in a recipe's specification.
type Secret struct {
	ID uuid.UUID `json:"id"`
	// ServiceID names the service the secret belongs to, which names the stored secret.
	ServiceID uuid.UUID `json:"serviceId"`
	Name      string    `json:"name"`
	// Format is how a generated value is written: "token" is 32 random bytes as unpadded
	// base64url, "hex" 32 random bytes as lowercase hex.
	Format string `json:"format"`
}

// RoutePreset is the public HTTP route an entry needs. Each of OriginVariables is set on the
// service to the route's origin, such as https://tracker.example.com, which the application
// needs to know its own public address.
type RoutePreset struct {
	ServiceID       uuid.UUID `json:"serviceId"`
	Port            int       `json:"port"`
	OriginVariables []string  `json:"originVariables"`
}

// BackupPreset is a backup policy an entry recommends for one of its services. It takes the
// schedule's local time in the zone the owner chooses.
type BackupPreset struct {
	ServiceID uuid.UUID `json:"serviceId"`
	Method    string    `json:"method"`
	Hour      int       `json:"hour"`
	Minute    int       `json:"minute"`
	Retain    int       `json:"retain"`
}

const NginxImage = "nginx:1.30.6-alpine3.24@sha256:583b7f2fce1cdb8da5ad5c39ebd3c33b98b87d493190e548bdee1fa8c619c311"
const ValkeyImage = "valkey/valkey:9.1.3-alpine3.24@sha256:bf09d58e96d6b4524a15396b65b242bbe18beebbcc15a04d8ee75ddac62fae0b"

// The slim image leaves out Chromium and the embedded MariaDB, which the recipe does not
// use and which carry most of the full image's advisories. The rootless image runs as
// node (uid 1000) and ships /app/data owned by it, so a new volume is writable.
const UptimeKumaImage = "louislam/uptime-kuma:2.5.9-slim-rootless@sha256:053a48927ee714b1da78c6810041a44a266bd8c06dc30ead5b3f459d0f219fb0"
const RegistryImage = "registry:3.1.2@sha256:01cd312e67026f06f5d89c3e133a4a9adc7cb9eeb9b1194e0b2954dfd770efd2"

// KaneoImage bundles Kaneo's API and web client behind one nginx on port 5173, as uid 1001.
const KaneoImage = "ghcr.io/usekaneo/kaneo:2.49.0@sha256:cd1826b1aba3ce736015cadf6f6c975d923aac00f4b6aaa7aedb29bc722dae49"

// placeholder is a public identity in a recipe, which staging replaces.
func placeholder(recipe, part string) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("catalog/"+recipe+"/"+part))
}

// template is a one-service specification under placeholder identities.
func template(id, name, kind, image string, port int, path, mount string) domain.Specification {
	return service(id, "service", name, kind, image, port, path, mount)
}

func service(id, part, name, kind, image string, port int, path, mount string) domain.Specification {
	a := domain.Application{ID: placeholder(id, part), Name: name, Kind: kind, Source: domain.ApplicationSource{Kind: "image", Image: &image}}
	s := domain.Specification{Version: 1, Variables: []domain.Variable{}}
	if port != 0 {
		a.Outputs = []domain.ServiceOutput{{Name: "PORT", Type: "port", Port: port}, {Name: "URL", Type: "url", Scheme: "http", Port: port, Path: path}}
		if kind == "web" {
			a.Health = &domain.HealthCheck{Port: port, Path: path}
		}
	}
	if mount != "" {
		// A service recipe's volume keeps the placeholder it has always had.
		volumePart := "volume"
		if part != "service" {
			volumePart = part + "/volume"
		}
		volume := placeholder(id, volumePart)
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
	pg := Recipe{ID: "postgresql-18", Name: "PostgreSQL", Description: "PostgreSQL 18 with a retained data volume and secret-backed connection values.", Documentation: "https://hub.docker.com/_/postgres", Prerequisites: []string{private, "Staging generates the password into the secret store, or uses an existing secret version you choose. Initialization settings do not change credentials in an existing volume."}, Specification: template("postgresql-18", "PostgreSQL", "postgres", domain.PostgresImage, 0, "", domain.PostgresMount(domain.PostgresImage)), Support: base}
	password := uuid.NewSHA1(uuid.NameSpaceURL, []byte("catalog/password-placeholder"))
	pg.Specification.Applications[0].Postgres = &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: password}
	pg.Secrets = []Secret{{ID: password, ServiceID: pg.Specification.Applications[0].ID, Name: "password", Format: "token"}}
	pg.Support.Backup = Support{"tested", "PostgreSQL logical backup using the existing encrypted off-server backup policy."}
	pg.Support.Restore = Support{"tested", "Logical restore into fresh storage with a verified application check."}
	pg.Support.Upgrade = Support{"tested", "Only the existing PostgreSQL 17.10 → 18.4 workflow; later major versions require a new recipe."}
	cache := Recipe{ID: "valkey-cache", Name: "Valkey cache", Description: "A private, disposable Redis-compatible cache. Data is intentionally not persisted.", Documentation: "https://valkey.io/topics/persistence/", Prerequisites: []string{private, "Unauthenticated within this deployment unit. Use only for data that can be rebuilt; startup is not a readiness guarantee."}, Specification: template("valkey-cache", "Valkey cache", "worker", ValkeyImage, 6379, "", ""), Support: base}
	cache.Specification.Applications[0].Outputs[1].Scheme = "redis"
	cache.Specification.Applications[0].Command = []string{"valkey-server", "--save", "", "--appendonly", "no", "--maxmemory", "128mb", "--maxmemory-policy", "allkeys-lru"}
	cache.Support.Backup, cache.Support.Restore = stateless, stateless
	nginx := Recipe{ID: "nginx", Name: "Nginx", Description: "A static HTTP server with the upstream welcome page and HTTP readiness.", Documentation: "https://hub.docker.com/_/nginx", Prerequisites: []string{private, "Add your own image or reviewed configuration to serve application content."}, Specification: template("nginx", "Nginx", "web", NginxImage, 80, "/", ""), Support: base}
	nginx.Support.Backup, nginx.Support.Restore = stateless, stateless
	kuma := Recipe{ID: "uptime-kuma", Name: "Uptime Kuma", Description: "Uptime monitoring with a retained local data volume.", Documentation: "https://github.com/louislam/uptime-kuma/wiki/%F0%9F%94%A7-How-to-Install", Prerequisites: []string{private, "Create the initial administrator through a restricted route before granting public access. Docker socket monitoring is not enabled.", "Choose SQLite or an external MariaDB at setup. The image has no embedded MariaDB or local browser for Real Browser monitors."}, Specification: template("uptime-kuma", "Uptime Kuma", "web", UptimeKumaImage, 3001, "/", "/app/data"), Support: base}
	registry := Recipe{ID: "oci-registry", Name: "OCI registry", Description: "A private Distribution registry with retained local storage; separate from Astilba build registry credentials.", Documentation: "https://distribution.github.io/distribution/about/deploying/", Prerequisites: []string{private, "The template is unauthenticated. Configure registry authentication and TLS before publishing; the Docker host cannot use a private container hostname as a remote build registry.", "As a build registry, deploy it on a different server from the one that builds: builds can't reach the server they run on."}, Specification: template("oci-registry", "OCI registry", "web", RegistryImage, 5000, "/v2/", "/var/lib/registry"), Support: base}
	none := "none"
	registry.Specification.Applications[0].Variables = []domain.Variable{{Name: "OTEL_TRACES_EXPORTER", Value: &none}}
	recipes := []Recipe{pg, cache, nginx, kuma, registry, kaneo(base)}
	for i := range recipes {
		r := &recipes[i]
		if r.Kind == "" {
			r.Kind = ServiceRecipe
		}
		if r.Secrets == nil {
			r.Secrets = []Secret{}
		}
		if r.Backups == nil {
			r.Backups = []BackupPreset{}
		}
	}
	return recipes
}

// kaneo is Kaneo's project tracker on a managed PostgreSQL database, after upstream's Compose
// file (github.com/usekaneo/kaneo, MIT): its image derives the API's URL from KANEO_CLIENT_URL
// and serves both behind its own nginx, and keeps no local data unless uploads go to S3.
func kaneo(base Maintenance) Recipe {
	const id = "kaneo"
	app := service(id, "app", "Kaneo", "web", KaneoImage, 5173, "/", "")
	app.Applications[0].Health.Path = "/api/health"
	db := service(id, "database", "Kaneo database", "postgres", domain.PostgresImage, 0, "", domain.PostgresMount(domain.PostgresImage))
	password, auth := placeholder(id, "database/password"), placeholder(id, "app/auth-secret")
	database := &db.Applications[0]
	database.Postgres = &domain.Postgres{Database: "kaneo", Username: "kaneo", PasswordSecretVersionID: password}
	kaneo := &app.Applications[0]
	// The first account becomes Kaneo's administrator even with registration closed, so the
	// route admits only the owner's network until they have signed up (see Stage). Later
	// accounts come by invitation.
	closed := "true"
	kaneo.Variables = []domain.Variable{{Name: "AUTH_SECRET", SecretVersionID: &auth}, {Name: "DISABLE_REGISTRATION", Value: &closed}}
	kaneo.DependsOn = []uuid.UUID{database.ID}
	spec := domain.Specification{Version: 1, Variables: []domain.Variable{}, Applications: []domain.Application{*kaneo, *database}, Volumes: db.Volumes}
	spec.Bindings = []domain.Binding{{ID: placeholder(id, "database-url"), SourceID: database.ID, TargetID: kaneo.ID, Output: "URL", Variable: "DATABASE_URL"}}
	r := Recipe{
		ID: id, Kind: AppTemplate, Version: "2.49.0", Name: "Kaneo",
		Description:   "Kaneo project management with its own PostgreSQL database, a generated session secret and a public HTTPS route.",
		Documentation: "https://kaneo.app/docs/core/installation/environment-variables",
		Prerequisites: []string{
			"The route admits only the client networks you list. Kaneo makes the first account its administrator, so create yours before allowing anyone else: then remove the allowlist from the route, or widen it.",
			"Registration is closed after the first account; invite people from Kaneo. Set DISABLE_REGISTRATION to false to let anyone sign up.",
			"Email, sign-in providers, uploads and GitHub need Kaneo's own variables (SMTP_*, GITHUB_*, S3_*). Without S3, Kaneo stores no files.",
			"KANEO_CLIENT_URL follows the domain you stage with. Change it with the route's domain.",
		},
		Specification: spec,
		Secrets: []Secret{
			{ID: password, ServiceID: database.ID, Name: "password", Format: "token"},
			{ID: auth, ServiceID: kaneo.ID, Name: "AUTH_SECRET", Format: "hex"},
		},
		Route:   &RoutePreset{ServiceID: kaneo.ID, Port: 5173, OriginVariables: []string{"KANEO_CLIENT_URL"}},
		Backups: []BackupPreset{{ServiceID: database.ID, Method: "postgres-logical", Hour: 3, Minute: 0, Retain: 14}},
		Support: base,
	}
	r.Support.Binding = Support{"tested", "Kaneo receives the database URL and waits for PostgreSQL readiness."}
	r.Support.Backup = Support{"tested", "PostgreSQL logical backup of the database, which holds all of Kaneo's data unless uploads go to S3."}
	r.Support.Restore = Support{"tested", "Logical restore of the database into fresh storage."}
	r.Support.Upgrade = Support{"manual", "Kaneo migrates its database when a new image starts and cannot go back. Back up and check the release notes before changing the image."}
	return r
}
