package dbmigrate

import (
	"context"
	"database/sql"
	"embed"
	"errors"
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

const Version int64 = 15

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
	if _, err := p.Up(ctx); err != nil {
		return errors.New("database migration failed")
	}
	return riverSchema(ctx, dsn, true)
}
func Check(ctx context.Context, dsn string) error {
	p, db, err := provider(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	version, err := p.GetDBVersion(ctx)
	if err != nil || version != Version {
		return errors.New("database schema is incompatible; run the matching migration command")
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
	existing, err := m.ExistingVersions(ctx)
	if err != nil {
		return errors.New("run the matching migration command")
	}
	if len(existing) != len(m.AllVersions()) {
		return errors.New("River schema is incompatible; run the matching migration command")
	}
	return nil
}
