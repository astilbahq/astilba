package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/astilbahq/astilba/services/platform/internal/deployment"
	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// nativeUpgradeLocaleRefusals checks, from the preflight's helper, that the upgrade refuses what
// the new cluster would give another encoding or locale (PLAT-602). It leaves source's template1
// and postgres as initdb made them, but for their comments and oids. image is source's release.
func nativeUpgradeLocaleRefusals(t *testing.T, e *Engine, helper pgClient, source string, a domain.Application, p deployment.Plan, image string) {
	t.Helper()
	ctx := context.Background()
	var err error
	// A superuser can drop template1 or postgres and create it again from template0, with
	// initdb's flags, owner, grants and connection limit, in another encoding or locale,
	// which the new cluster wouldn't keep (PLAT-602). Created again as initdb made it, it's
	// accepted, and then the attributes left out are what differ.
	recreate := func(database, options string) {
		t.Helper()
		statements := []string{"DROP DATABASE " + database + " WITH (FORCE)", "CREATE DATABASE " + database + " TEMPLATE template0 " + options}
		if database == "template1" {
			statements = []string{"ALTER DATABASE template1 IS_TEMPLATE false", statements[0], statements[1] + " IS_TEMPLATE true", "REVOKE TEMPORARY ON DATABASE template1 FROM PUBLIC"}
		}
		for _, statement := range statements {
			if _, err = e.pgQuery(ctx, pgClient{container: source}, a, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
	}
	for _, database := range []struct{ name, called string }{{"template1", "template1"}, {"postgres", "the postgres maintenance database"}} {
		recreate(database.name, "")
		if err = e.upgradeDatabaseChecks(ctx, helper, a); err != nil {
			t.Fatalf("%s created again from template0 as initdb made it was refused: %v", database.name, err)
		}
		for _, variant := range []struct{ options, differs string }{
			{"ENCODING 'LATIN1' LOCALE 'C'", "(encoding, LC_COLLATE, LC_CTYPE)"},
			{"LC_COLLATE 'C'", "(LC_COLLATE)"},
			{"LOCALE_PROVIDER icu ICU_LOCALE 'en-US'", "(locale provider, ICU or builtin locale)"},
			{"LOCALE_PROVIDER builtin BUILTIN_LOCALE 'C.UTF-8'", "(locale provider, ICU or builtin locale)"},
			{"LOCALE_PROVIDER icu ICU_LOCALE 'en-US' ICU_RULES '&a < b'", "(locale provider, ICU or builtin locale, ICU rules)"},
		} {
			recreate(database.name, variant.options)
			if err = e.upgradeDatabaseChecks(ctx, helper, a); err == nil || !strings.Contains(err.Error(), "an encoding or locale other than template0's") || !strings.Contains(err.Error(), database.called+" "+variant.differs) {
				t.Fatalf("%s created again with %s wasn't refused by name: %v", database.name, variant.options, err)
			}
		}
		recreate(database.name, "")
	}
	// A cluster of its own, before the owner model, for what the source must keep: the
	// application's database made again in another encoding, and template0, which can be
	// dropped once it isn't a template but never made again under oid 4.
	t.Run("legacy_cluster_locale", func(t *testing.T) {
		created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: image, Env: []string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=locale-fixture-password"}, Labels: visualLabels(p, uuid.New())}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			_, _ = e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
		}()
		if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
			t.Fatal(err)
		}
		if err = e.waitPostgresStartup(ctx, created.ID, "app", "app"); err != nil {
			t.Fatal(err)
		}
		legacy := a
		legacyPG := *a.Postgres
		legacyPG.Username, legacyPG.Database, legacyPG.Roles = "app", "app", ""
		legacy.Postgres = &legacyPG
		maintenance := legacy
		maintenancePG := legacyPG
		maintenancePG.Database = "postgres"
		maintenance.Postgres = &maintenancePG
		cluster := pgClient{container: created.ID}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy); err != nil {
			t.Fatal("a cluster as initdb made it was refused:", err)
		}
		for _, statement := range []string{"DROP DATABASE app WITH (FORCE)", "CREATE DATABASE app TEMPLATE template0 ENCODING 'LATIN1' LOCALE 'C'"} {
			if _, err = e.pgQuery(ctx, cluster, maintenance, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy); err == nil || !strings.Contains(err.Error(), "the application database (encoding, LC_COLLATE, LC_CTYPE)") {
			t.Fatalf("the application's database made again in LATIN1 wasn't refused by name: %v", err)
		}
		for _, statement := range []string{"ALTER DATABASE template0 IS_TEMPLATE false", "DROP DATABASE template0"} {
			if _, err = e.pgQuery(ctx, cluster, maintenance, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy); err == nil || !strings.Contains(err.Error(), "template0") || strings.Contains(err.Error(), "encoding or locale") {
			t.Fatalf("a cluster without template0 wasn't refused for it: %v", err)
		}
	})
}
