package integration

import (
	"context"
	"testing"

	dbmigrate "github.com/astilbahq/astilba/services/platform/db"
)

// A release runs on the schema of the release after it when that release only added to it, so
// rolling back is a redeploy. Anything else ahead of it, or behind it, is refused.
func TestSchemaCheckAcceptsOnlyAdditiveNewerMigrations(t *testing.T) {
	f := setupStore(t, false, false)
	ctx := context.Background()
	dsn := f.st.Pool.Config().ConnString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := f.st.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	check := func(want bool) {
		t.Helper()
		if err := dbmigrate.Check(ctx, dsn); (err == nil) != want {
			t.Fatalf("Check: %v, want accepted %v", err, want)
		}
	}
	check(true)

	// The next release added a table and recorded it.
	exec("INSERT INTO goose_db_version (version_id,is_applied) VALUES ($1,true)", dbmigrate.Version+1)
	exec("INSERT INTO schema_additive_migrations (version) VALUES ($1)", dbmigrate.Version+1)
	check(true)

	// A second migration in the same release changed an existing table.
	exec("INSERT INTO goose_db_version (version_id,is_applied) VALUES ($1,true)", dbmigrate.Version+2)
	check(false)
	// Goose's older form records a migration it took back as a later row that is not applied.
	exec("INSERT INTO goose_db_version (version_id,is_applied) VALUES ($1,false)", dbmigrate.Version+2)
	check(true)

	// A marker for a migration that was never applied vouches for nothing else.
	exec("INSERT INTO schema_additive_migrations (version) VALUES ($1)", dbmigrate.Version+4)
	exec("INSERT INTO goose_db_version (version_id,is_applied) VALUES ($1,true)", dbmigrate.Version+3)
	check(false)
	exec("DELETE FROM goose_db_version WHERE version_id=$1", dbmigrate.Version+3)
	check(true)

	// River's migrations carry no marker, so a newer River schema is always refused.
	exec("INSERT INTO river_migration (line,version) VALUES ('main',10000)")
	check(false)
	exec("DELETE FROM river_migration WHERE version=10000")
	check(true)

	// A database behind this release still needs migrating.
	exec("DELETE FROM goose_db_version WHERE version_id>=$1", dbmigrate.Version)
	check(false)
}
