package backup

import (
	"strings"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
)

func recoveryFixture(t *testing.T) (Recovery, Manifest) {
	t.Helper()
	image := domain.PostgresImage
	app, volume := uuid.New(), uuid.New()
	plan := deployment.Plan{ID: uuid.New(), ServerID: uuid.New(), EnvironmentID: uuid.New(), UnitID: uuid.New(), RevisionID: uuid.New(), Recipe: deployment.Recipe, ExpiresAt: time.Now().Add(time.Hour), Images: map[string]string{app.String(): image}, Specification: domain.Specification{Version: 1, Applications: []domain.Application{{ID: app, Name: "Database", Kind: "postgres", Source: domain.ApplicationSource{Kind: "image", Image: &image}, Postgres: &domain.Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}, Mounts: []domain.VolumeMount{{VolumeID: volume, Target: "/var/lib/postgresql"}}}}, Variables: []domain.Variable{}, Volumes: []domain.Volume{{ID: volume, Name: "Data"}}}}.WithHash()
	policy := Policy{ID: uuid.New(), Name: "Database", WorkspaceID: uuid.New(), EnvironmentID: plan.EnvironmentID, ServerID: plan.ServerID, DestinationID: uuid.New(), ResourceID: app, Generation: 3, Method: "postgres-logical", Schedule: Schedule{Timezone: "UTC", Hour: 2}, Retain: 7, Plan: plan}
	if err := policy.Validate(); err != nil {
		t.Fatal("fixture policy: ", err)
	}
	r := Recovery{Installation: Installation{Policy: policy, Destination: Destination{ID: policy.DestinationID, Name: "S3", Kind: "s3", Endpoint: "https://s3.example.test", Bucket: "backups", Region: "auto"}, Access: Access{AccessKey: "key", SecretKey: "secret", Password: "repository-password"}}, RunID: uuid.New(), Snapshot: strings.Repeat("a", 64), TargetID: uuid.New()}
	m := Manifest{Recipe: Recipe, PolicyID: policy.ID, RunID: uuid.New(), EnvironmentID: policy.EnvironmentID, ResourceID: app, Method: policy.Method, Image: image, Database: "app", Username: "app", CreatedAt: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC), Roles: []Role{}, Extensions: []string{}, SHA256: strings.Repeat("b", 64), Bytes: 4096}
	return r, m
}

// The digest that freezes a restore covers what it restores and where, and none of the access
// released with it.
func TestRecoveryDigestCoversWhatIsRestoredAndNotAccess(t *testing.T) {
	r, _ := recoveryFixture(t)
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	base := r.Digest()
	released := r
	released.Installation.Access = Access{AccessKey: "other", SecretKey: "other", Password: "other-password", DatabasePassword: "other"}
	released.Installation.RepositoryID = strings.Repeat("c", 64)
	if released.Digest() != base {
		t.Fatal("released access changed the restore's digest")
	}
	for name, change := range map[string]func(*Recovery){
		"snapshot":    func(r *Recovery) { r.Snapshot = strings.Repeat("d", 64) },
		"target":      func(r *Recovery) { r.TargetID = uuid.New() },
		"run":         func(r *Recovery) { r.RunID = uuid.New() },
		"destination": func(r *Recovery) { r.Installation.Destination.Bucket = "other" },
		"policy":      func(r *Recovery) { r.Installation.Policy.ResourceID = uuid.New() },
	} {
		changed := r
		change(&changed)
		if changed.Digest() == base {
			t.Errorf("a different %s kept the restore's digest", name)
		}
	}
}

func TestRecoveryRefusesAnIncompleteRestore(t *testing.T) {
	for name, change := range map[string]func(*Recovery){
		"no run":      func(r *Recovery) { r.RunID = uuid.Nil },
		"no target":   func(r *Recovery) { r.TargetID = uuid.Nil },
		"no snapshot": func(r *Recovery) { r.Snapshot = "" },
		"a container job": func(r *Recovery) {
			r.Installation.Policy.Method = "container-job"
			r.Installation.Policy.Command = []string{"true"}
		},
		"a plan for another place": func(r *Recovery) { r.Installation.Policy.ServerID = uuid.New() },
	} {
		r, _ := recoveryFixture(t)
		change(&r)
		if r.Validate() == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// A manifest read out of the snapshot must have been written for the policy and describe a usable
// archive.
func TestRecoveryAdmitsOnlyThePolicysManifest(t *testing.T) {
	r, m := recoveryFixture(t)
	if err := r.Admits(m); err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*Manifest){
		"another policy":      func(m *Manifest) { m.PolicyID = uuid.New() },
		"another environment": func(m *Manifest) { m.EnvironmentID = uuid.New() },
		"another resource":    func(m *Manifest) { m.ResourceID = uuid.New() },
		"another method":      func(m *Manifest) { m.Method = QuiescedFiles },
		"another recipe":      func(m *Manifest) { m.Recipe = Recipe + 1 },
		"no run":              func(m *Manifest) { m.RunID = uuid.Nil },
		"no bytes":            func(m *Manifest) { m.Bytes = 0 },
		"too many bytes":      func(m *Manifest) { m.Bytes = 1<<40 + 1 },
		"no digest":           func(m *Manifest) { m.SHA256 = "" },
		"no roles":            func(m *Manifest) { m.Roles = nil },
		"no extensions":       func(m *Manifest) { m.Extensions = nil },
		"too many roles": func(m *Manifest) {
			m.Roles = make([]Role, 101)
			for i := range m.Roles {
				m.Roles[i].Name = "r"
			}
		},
		"a long role":     func(m *Manifest) { m.Roles = []Role{{Name: strings.Repeat("r", 64)}} },
		"an unnamed role": func(m *Manifest) { m.Roles = []Role{{}} },
		"a long image":    func(m *Manifest) { m.Image = strings.Repeat("i", 513) },
		"a long database": func(m *Manifest) { m.Database = strings.Repeat("d", 101) },
	} {
		changed := m
		change(&changed)
		if r.Admits(changed) == nil {
			t.Errorf("a manifest with %s was admitted", name)
		}
	}
}
