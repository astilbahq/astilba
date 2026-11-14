package dbmigrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"
	"io/fs"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Version is the newest migration this binary carries. It runs on a database at this version,
// or ahead of it when every newer migration recorded itself as additive.
const Version int64 = 54

// CustodyStage is the custody stage this release completes. Its migrations assume the one before
// it is complete, so `migrate` refuses an installation further behind: stages are taken one
// release at a time. See docs/hosting.md#key-custody.
const CustodyStage = 1

func provider(dsn string) (*goose.Provider, *sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, nil, errors.New("cannot configure migration database")
	}
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	source, err := fs.Sub(migrations, "migrations")
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, source, goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true))
	if err != nil {
		db.Close()
		return nil, nil, err
	}
	return p, db, nil
}
func Up(ctx context.Context, dsn string) error {
	p, db, err := provider(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	if err = checkStage(ctx, db); err != nil {
		return err
	}
	if _, err := p.Up(ctx); err != nil {
		return errors.New("database migration failed")
	}
	return riverSchema(ctx, dsn, true)
}

// checkStage refuses to migrate an installation more than one custody stage behind this release.
// A database from before stages were recorded has completed none.
func checkStage(ctx context.Context, db *sql.DB) error {
	var recorded bool
	completed := 0
	// inline-sql: the table may not exist yet, and a query naming it would then fail to parse.
	err := db.QueryRowContext(ctx, `SELECT to_regclass('custody_stage') IS NOT NULL`).Scan(&recorded)
	if err == nil && recorded {
		// inline-sql: as above, sqlc's queries assume the current schema.
		err = db.QueryRowContext(ctx, `SELECT stage FROM custody_stage`).Scan(&completed)
	}
	if err != nil {
		return errors.New("cannot read the custody stage")
	}
	return stageRefusal(CustodyStage, completed)
}

// stageRefusal refuses a release that completes stage required on an installation that has
// completed stage completed, unless it is the next stage or an earlier one.
func stageRefusal(required, completed int) error {
	if required > completed+1 {
		return fmt.Errorf("this release completes custody stage %d, and the installation has completed stage %d: upgrade through the releases between them first", required, completed)
	}
	return nil
}

func Check(ctx context.Context, dsn string) error {
	p, db, err := provider(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	version, err := p.GetDBVersion(ctx)
	if err != nil || version < Version {
		return errors.New("database schema is incompatible; run the matching migration command")
	}
	if version > Version {
		var unmarked int64
		// Goose's provider deletes a migration's row when it is taken back, so every row is applied.
		// inline-sql: goose owns its version table, which sqlc does not know.
		err = db.QueryRowContext(ctx, `SELECT count(*) FROM goose_db_version v WHERE v.version_id>$1 AND NOT EXISTS (SELECT 1 FROM schema_additive_migrations a WHERE a.version=v.version_id)`, Version).Scan(&unmarked)
		if err != nil || unmarked != 0 {
			return errors.New("database schema is newer than this release and not only added to; restore the backup taken before the upgrade to run this release")
		}
	}
	return riverSchema(ctx, dsn, false)
}

func riverSchema(ctx context.Context, dsn string, apply bool) error {
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer pool.Close()
	// Hold a session advisory lock across the explicit River migration/check.
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SELECT pg_advisory_lock(681931253)"); err != nil {
		return err
	}
	defer conn.Exec(context.Background(), "SELECT pg_advisory_unlock(681931253)")
	m, err := rivermigrate.New(riverpgxv5.New(pool), nil)
	if err != nil {
		return err
	}
	if apply {
		_, err = m.Migrate(ctx, rivermigrate.DirectionUp, nil)
		return err
	}
	// River's migrations carry no marker, so a release that moves River's schema on is never
	// additive: ExistingVersions refuses a version this binary does not know.
	existing, err := m.ExistingVersions(ctx)
	if err != nil {
		return errors.New("run the matching migration command")
	}
	if len(existing) != len(m.AllVersions()) {
		return errors.New("River schema is incompatible; run the matching migration command")
	}
	return nil
}
