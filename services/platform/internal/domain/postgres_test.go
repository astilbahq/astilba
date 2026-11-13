package domain

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// withReleases runs a test against a release table with a retired release in each major, as a
// pin bump leaves it, without depending on which pins are current.
func withReleases(t *testing.T, releases []PostgresRelease) {
	t.Helper()
	saved := postgresReleases
	postgresReleases = releases
	t.Cleanup(func() { postgresReleases = saved })
}

var (
	pg1710 = PostgresRelease{Image: "postgres:17.10-alpine3.23@sha256:" + strings.Repeat("1", 64), Version: 170010, Line: "alpine3.23", Mount: "/var/lib/postgresql/data", DataDirectory: "/var/lib/postgresql/data"}
	pg1712 = PostgresRelease{Image: "postgres:17.12-alpine3.23@sha256:" + strings.Repeat("2", 64), Version: 170012, Line: "alpine3.23", Mount: "/var/lib/postgresql/data", DataDirectory: "/var/lib/postgresql/data"}
	pg184  = PostgresRelease{Image: "postgres:18.4-alpine3.23@sha256:" + strings.Repeat("3", 64), Version: 180004, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"}
	pg186  = PostgresRelease{Image: "postgres:18.6-alpine3.23@sha256:" + strings.Repeat("4", 64), Version: 180006, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"}
	pg187  = PostgresRelease{Image: "postgres:18.7-alpine3.23@sha256:" + strings.Repeat("5", 64), Version: 180007, Line: "alpine3.23", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"}
	pg188  = PostgresRelease{Image: "postgres:18.8-alpine3.24@sha256:" + strings.Repeat("6", 64), Version: 180008, Line: "alpine3.24", Mount: "/var/lib/postgresql", DataDirectory: "/var/lib/postgresql/18/docker"}
)

// The table is what every check reads, so a wrong row strands data. Each row must name the
// version, line and layout its pinned tag says, and the exported pins must be current.
func TestPostgresReleaseTableDescribesItsPins(t *testing.T) {
	pinned := regexp.MustCompile(`^postgres:([0-9]+)\.([0-9]+)-(alpine[0-9.]+)@sha256:[0-9a-f]{64}$`)
	seen := map[string]bool{}
	versions := map[int]bool{}
	layout := map[int][2]string{17: {"/var/lib/postgresql/data", "/var/lib/postgresql/data"}, 18: {"/var/lib/postgresql", "/var/lib/postgresql/18/docker"}}
	for _, r := range PostgresReleases() {
		m := pinned.FindStringSubmatch(r.Image)
		if m == nil {
			t.Fatalf("%s is not a digest-pinned Alpine PostgreSQL image", r.Image)
		}
		major, _ := strconv.Atoi(m[1])
		minor, _ := strconv.Atoi(m[2])
		if r.Version != major*10000+minor || r.Line != m[3] || r.Name() != m[1]+"."+m[2] {
			t.Errorf("%s: version %d, line %s and name %s differ from its tag", r.Image, r.Version, r.Line, r.Name())
		}
		if want, ok := layout[r.Major()]; !ok || r.Mount != want[0] || r.DataDirectory != want[1] {
			t.Errorf("%s: layout %s, %s differs from its major's", r.Image, r.Mount, r.DataDirectory)
		}
		if seen[r.Image] || versions[r.Version] {
			t.Errorf("%s is listed twice", r.Image)
		}
		seen[r.Image], versions[r.Version] = true, true
		if got, ok := PostgresReleaseOf(r.Image); !ok || got.Image != r.Image || PostgresMount(r.Image) != r.Mount {
			t.Errorf("%s does not resolve to itself", r.Image)
		}
	}
	for major, image := range map[int]string{17: Postgres17Image, 18: PostgresImage} {
		current, ok := CurrentPostgres(major)
		if !ok || current.Image != image || current.Retired() {
			t.Errorf("the PostgreSQL %d pin %s is not its major's current release", major, image)
		}
	}
	if _, ok := PostgresReleaseOf(strings.Split(PostgresImage, "@")[0]); ok {
		t.Error("a tag without its digest named a release")
	}
}

func TestPostgresReleaseRules(t *testing.T) {
	withReleases(t, []PostgresRelease{pg1710, pg1712, pg184, pg186, pg187, pg188})
	if current, _ := CurrentPostgres(18); current.Image != pg188.Image || !pg184.Retired() || !pg187.Retired() || pg188.Retired() || pg1712.Retired() {
		t.Fatal("the newest release of a major is not the only current one")
	}
	for _, c := range []struct {
		name            string
		serving, source PostgresRelease
		restores, opens string
	}{
		{name: "same release", serving: pg184, source: pg184},
		{name: "later minor of the line", serving: pg187, source: pg184},
		{name: "later minor of another line", serving: pg188, source: pg184, opens: "sorting libraries differ"},
		{name: "earlier minor", serving: pg184, source: pg187, restores: "newer than the PostgreSQL 18.4", opens: "never moves to an earlier release"},
		{name: "older major", serving: pg187, source: pg1710, restores: "taken on PostgreSQL 17.10, and the database now runs PostgreSQL 18.7", opens: "cannot open data PostgreSQL 17.10 wrote"},
		{name: "newer major", serving: pg1712, source: pg184, restores: "restore it into a PostgreSQL 18 database", opens: "needs the supported upgrade"},
	} {
		check := func(rule string, err error, want string) {
			if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
				t.Errorf("%s, %s: got %v, want %q", c.name, rule, err, want)
			}
		}
		check("restore", c.serving.RestoresArchiveFrom(c.source), c.restores)
		check("open", c.serving.OpensDataFrom(c.source), c.opens)
	}
	if pg1710.UpgradesTo(pg184) != nil || pg1712.UpgradesTo(pg188) != nil || pg184.UpgradesTo(pg187) == nil || pg184.UpgradesTo(pg1712) == nil {
		t.Error("the upgrade is not exactly PostgreSQL 17 to 18")
	}
	// The advice follows PostgreSQL's releases, so moving 18.4 to 18.7 without pinning 18.6
	// still rebuilds what 18.6 named.
	reindex := func(from, to PostgresRelease) []int {
		var versions []int
		for _, query := range to.ReindexSince(from) {
			for _, advice := range postgresReindex {
				if advice.Query == query && advice.Version/10000 == to.Major() {
					versions = append(versions, advice.Version)
				}
			}
		}
		return versions
	}
	for _, c := range []struct {
		from, to PostgresRelease
		want     []int
	}{
		{pg184, pg187, []int{180006, 180007}},
		{pg186, pg187, []int{180007}},
		{pg187, pg188, nil},
		{pg187, pg187, nil},
		{pg1710, pg1712, []int{170011, 170012}},
		{pg1712, pg187, nil},
	} {
		if got := reindex(c.from, c.to); !slices.Equal(got, c.want) {
			t.Errorf("%s to %s rebuilds after %v, want %v", c.from.Name(), c.to.Name(), got, c.want)
		}
	}
}

// Stored drafts and frozen plans keep naming a retired release, so it stays valid. A draft may
// keep it for a database that runs it, but cannot start anything new on it.
func TestRetiredPostgresReleaseIsKeptButNeverChosen(t *testing.T) {
	withReleases(t, []PostgresRelease{pg1710, pg1712, pg184, pg187})
	database, volume, secret := uuid.New(), uuid.New(), uuid.New()
	spec := func(image string) Specification {
		return Specification{Version: 1, Variables: []Variable{}, Volumes: []Volume{{ID: volume, Name: "Data"}}, Applications: []Application{{ID: database, Name: "Database", Kind: "postgres", Source: ApplicationSource{Kind: "image", Image: &image}, Postgres: &Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []VolumeMount{{VolumeID: volume, Target: PostgresMount(image)}}}}}
	}
	for _, image := range []string{pg184.Image, pg187.Image, pg1710.Image} {
		if err := ValidateVisual(spec(image)); err != nil {
			t.Errorf("%s: %v", image, err)
		}
	}
	if ValidateVisual(spec("postgres:18.4-alpine3.23")) == nil {
		t.Error("an unpinned PostgreSQL image was accepted")
	}
	for _, c := range []struct {
		next, applied string
		refused       bool
	}{
		{next: pg187.Image},
		{next: pg184.Image, refused: true},
		{next: pg184.Image, applied: pg184.Image},
		{next: pg187.Image, applied: pg184.Image},
		{next: pg184.Image, applied: pg187.Image, refused: true},
		{next: pg1710.Image, applied: pg184.Image, refused: true},
	} {
		applied := Specification{}
		if c.applied != "" {
			applied = spec(c.applied)
		}
		if err := ValidateNewDraft(spec(c.next), applied); (err != nil) != c.refused {
			t.Errorf("%s over %q: %v", c.next, c.applied, err)
		}
	}
}

// A preview's database is new storage, so it takes its major's current release.
func TestPreviewDatabaseStartsOnTheCurrentRelease(t *testing.T) {
	withReleases(t, []PostgresRelease{pg1710, pg1712, pg184, pg187})
	database, volume, secret := uuid.New(), uuid.New(), uuid.New()
	for _, c := range []struct{ template, want string }{{pg184.Image, pg187.Image}, {pg187.Image, pg187.Image}, {pg1710.Image, pg1712.Image}} {
		image := c.template
		template := Specification{Version: 1, Variables: []Variable{}, Volumes: []Volume{{ID: volume, Name: "Data"}}, Applications: []Application{{ID: database, Name: "Database", Kind: "postgres", Source: ApplicationSource{Kind: "image", Image: &image}, Postgres: &Postgres{Database: "app", Username: "app", PasswordSecretVersionID: secret}, Mounts: []VolumeMount{{VolumeID: volume, Target: PostgresMount(image)}}}}}
		ids := map[uuid.UUID]uuid.UUID{database: uuid.New(), volume: uuid.New()}
		preview, err := RemapPreview(template, ids, map[uuid.UUID]uuid.UUID{secret: uuid.New()}, func(uuid.UUID) string { return "" })
		if err != nil {
			t.Fatal(err)
		}
		if got := *preview.Applications[0].Source.Image; got != c.want || *template.Applications[0].Source.Image != c.template {
			t.Errorf("a preview of a %s database runs %s, want %s", c.template, got, c.want)
		}
	}
}

// A pin names one digest. The same tag rebuilt upstream is another image, which has not cleared the
// release-age cooldown, so nothing accepts it as a release until it is pinned on purpose.
func TestRebuiltTagIsNotARelease(t *testing.T) {
	tag, _, _ := strings.Cut(PostgresImage, "@")
	rebuilt := tag + "@sha256:" + strings.Repeat("0", 64)
	if _, ok := PostgresReleaseOf(rebuilt); ok {
		t.Fatal("a rebuilt tag was accepted as the pinned release")
	}
	database, volume := uuid.New(), uuid.New()
	spec := Specification{Version: 1, Variables: []Variable{}, Volumes: []Volume{{ID: volume, Name: "Data"}}, Applications: []Application{{ID: database, Name: "Database", Kind: "postgres", Source: ApplicationSource{Kind: "image", Image: &rebuilt}, Postgres: &Postgres{Database: "app", Username: "app", PasswordSecretVersionID: uuid.New()}, Mounts: []VolumeMount{{VolumeID: volume, Target: PostgresMount(PostgresImage)}}}}}
	if ValidateVisual(spec) == nil {
		t.Fatal("a managed database on a rebuilt tag was accepted")
	}
	if FreshPostgresImage(rebuilt) != rebuilt {
		t.Fatal("an unpinned image was moved to a release instead of refused")
	}
}
