package agent

import (
	"context"
	"testing"
	"time"

	"github.com/astilbahq/astilba/services/platform/internal/domain"
	"github.com/google/uuid"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

// nativeUpgradeLocaleRefusals checks, from the preflight's helper, that the upgrade refuses what
// the new cluster, which target's initdb makes, would give another encoding, locale or setting
// (PLAT-602, PLAT-614, PLAT-615). It leaves source's template1 and postgres as initdb made them,
// but for their comments and oids. image is source's release, and s the upgrade of source.
func nativeUpgradeLocaleRefusals(t *testing.T, e *Engine, helper pgClient, source string, a domain.Application, s upgradeState, target domain.PostgresRelease, image string) {
	t.Helper()
	ctx := context.Background()
	refusal := "an encoding, locale or setting other than what PostgreSQL " + target.Name() + "'s initdb gives the new cluster requires a separate recipe: "
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
		if err = e.upgradeDatabaseChecks(ctx, helper, a, target); err != nil {
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
			if err = e.upgradeDatabaseChecks(ctx, helper, a, target); err == nil || err.Error() != refusal+database.called+" "+variant.differs {
				t.Fatalf("%s created again with %s wasn't refused by name: %v", database.name, variant.options, err)
			}
		}
		recreate(database.name, "")
	}
	// Clusters of their own, before the owner model, for what the source must keep: the
	// application's database made again in another encoding, template0, which can be dropped
	// once it isn't a template but never made again under oid 4, and the variables a database
	// from before the owner model keeps, which reach its initdb (PLAT-614, PLAT-615).
	t.Run("legacy_cluster_locale", func(t *testing.T) {
		start := func(variables ...string) pgClient {
			t.Helper()
			created, err := e.Docker.ContainerCreate(ctx, client.ContainerCreateOptions{Config: &container.Config{Image: image, Env: append([]string{"POSTGRES_USER=app", "POSTGRES_DB=app", "POSTGRES_PASSWORD=locale-fixture-password"}, variables...), Labels: visualLabels(s.Command.Plan.Source, uuid.New())}, HostConfig: &container.HostConfig{NetworkMode: "none"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = e.Docker.ContainerRemove(ctx, created.ID, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
			})
			if _, err = e.Docker.ContainerStart(ctx, created.ID, client.ContainerStartOptions{}); err != nil {
				t.Fatal(err)
			}
			return pgClient{container: created.ID}
		}
		// They start together, so their initdbs overlap.
		cluster, lang, monetary := start(), start("LANG=C.UTF-8"), start("LC_MONETARY=C", "TZ=Europe/London")
		for _, c := range []pgClient{cluster, lang, monetary} {
			if err := e.waitPostgresStartup(ctx, c.container, "app", "app"); err != nil {
				t.Fatal(err)
			}
		}
		legacy := a
		legacyPG := *a.Postgres
		legacyPG.Username, legacyPG.Database, legacyPG.Roles = "app", "app", ""
		legacy.Postgres = &legacyPG
		maintenance := legacy
		maintenancePG := legacyPG
		maintenancePG.Database = "postgres"
		maintenance.Postgres = &maintenancePG
		// The preflight itself refuses the variables' clusters, from a helper in each one's
		// namespace, before any writer stops.
		preflight := func(c pgClient) error {
			t.Helper()
			bounded, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			state := s
			state.Command.Plan.ID = uuid.New()
			_, err := e.upgradePreflight(bounded, state, c.container, legacy)
			return err
		}
		mismatch := " (LC_COLLATE, LC_CTYPE)"
		if err := preflight(lang); err == nil || err.Error() != refusal+"the application database"+mismatch+"; the postgres maintenance database"+mismatch+"; the server's settings (lc_messages, lc_monetary, lc_numeric, lc_time); template0"+mismatch+"; template1"+mismatch {
			t.Fatalf("a cluster made with LANG=C.UTF-8 wasn't refused at preflight by name: %v", err)
		}
		if err := preflight(monetary); err == nil || err.Error() != refusal+"the server's settings (lc_monetary, TimeZone)" {
			t.Fatalf("a cluster made with LC_MONETARY=C and TZ=Europe/London wasn't refused at preflight by name: %v", err)
		}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy, target); err != nil {
			t.Fatal("a cluster as initdb made it was refused:", err)
		}
		for _, statement := range []string{"DROP DATABASE app WITH (FORCE)", "CREATE DATABASE app TEMPLATE template0 ENCODING 'LATIN1' LOCALE 'C'"} {
			if _, err = e.pgQuery(ctx, cluster, maintenance, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy, target); err == nil || err.Error() != refusal+"the application database (encoding, LC_COLLATE, LC_CTYPE)" {
			t.Fatalf("the application's database made again in LATIN1 wasn't refused by name: %v", err)
		}
		for _, statement := range []string{"ALTER DATABASE template0 IS_TEMPLATE false", "DROP DATABASE template0"} {
			if _, err = e.pgQuery(ctx, cluster, maintenance, statement); err != nil {
				t.Fatalf("%s: %v", statement, err)
			}
		}
		if err = e.upgradeDatabaseChecks(ctx, cluster, legacy, target); err == nil || err.Error() != "a cluster without template0 requires a separate recipe" {
			t.Fatalf("a cluster without template0 wasn't refused for it: %v", err)
		}
	})
}
