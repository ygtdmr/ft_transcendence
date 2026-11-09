package postgres

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the pgx5 driver
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	sharedpostgres "github.com/melmut-42/ft_transcendence/backend/shared/postgres"
	"github.com/melmut-42/ft_transcendence/backend/stats-service/migrations"
)

// ErrMigrationSetup reports that migrations could not be prepared or the database
// could not be reached for them. It carries no connection details.
var ErrMigrationSetup = errors.New("migration setup failed")

// Migrate creates the Stats Service schema and applies every pending migration.
//
// Parameters:
//
//   - ctx: Controls schema creation and cancels a running migration between steps.
//   - dsn: PostgreSQL URL without a search_path; one is added.
//
// Flow:
//
//  1. Create the schema if it is missing, so the version table has a home.
//  2. Apply embedded migrations in order, tracked in schema_migrations inside the schema.
//
// Concurrent callers are safe: golang-migrate holds a PostgreSQL advisory lock
// while it runs. Running it again with nothing pending is not an error.
//
// Errors:
//
//   - ErrInvalidDSN: dsn is not a PostgreSQL URL.
//   - ErrMigrationSetup: the schema could not be created or the migration source or
//     database could not be opened.
//   - Migration errors: a migration failed; the database is left at the failing version.
//   - Context errors: ctx was canceled or its deadline expired.
func Migrate(ctx context.Context, dsn string) error {
	scoped, err := DSNWithSchema(dsn)
	if err != nil {
		return err
	}
	if err := ensureSchema(ctx, dsn); err != nil {
		return err
	}

	source, err := iofs.New(migrations.Files, ".")
	if err != nil {
		return ErrMigrationSetup
	}
	runner, err := migrate.NewWithSourceInstance("iofs", source, migrateURL(scoped))
	if err != nil {
		return ErrMigrationSetup
	}
	defer func() { _, _ = runner.Close() }()

	done := make(chan error, 1)
	go func() { done <- runner.Up() }()

	select {
	case err = <-done:
	case <-ctx.Done():
		runner.GracefulStop <- true
		<-done
		return ctx.Err()
	}
	if err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("apply migrations: %w", err)
	}

	return nil
}

// schemaLockKey is the advisory lock key serializing schema creation across instances.
const schemaLockKey int64 = 0x7374617473 // "stats"

// ensureSchema creates Schema when it does not exist yet.
//
// CREATE SCHEMA IF NOT EXISTS is not safe under concurrency: two sessions can both
// see the schema missing and one fails with a duplicate key error. A transaction-level
// advisory lock makes concurrent starts take turns; it is released on commit.
func ensureSchema(ctx context.Context, dsn string) error {
	pool, err := sharedpostgres.Open(ctx, sharedpostgres.Config{DSN: dsn})
	if err != nil {
		return err
	}
	defer pool.Close()

	tx, err := pool.Begin(ctx)
	if err != nil {
		return ErrMigrationSetup
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", schemaLockKey); err != nil {
		return ErrMigrationSetup
	}
	if _, err := tx.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+pgx.Identifier{Schema}.Sanitize()); err != nil {
		return ErrMigrationSetup
	}
	if err := tx.Commit(ctx); err != nil {
		return ErrMigrationSetup
	}

	return nil
}

// migrateURL rewrites a postgres URL to the scheme that selects the pgx v5 driver.
func migrateURL(dsn string) string {
	parsed, _ := url.Parse(dsn)
	parsed.Scheme = "pgx5"

	return parsed.String()
}
