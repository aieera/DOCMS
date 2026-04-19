package database

import (
	"errors"
	"fmt"
	"net/url"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres" // driver
	_ "github.com/golang-migrate/migrate/v4/source/file"      // driver
)

// withMigrationsTable appends the `x-migrations-table=<table>` query
// parameter so each service's migrator uses its own bookkeeping table
// (auth_schema_migrations, policy_schema_migrations, …). Without this,
// every service shares the default `schema_migrations` table and their
// version numbers collide — running auth's migrations would roll the
// "current version" backwards from policy's perspective.
func withMigrationsTable(databaseURL, table string) (string, error) {
	u, err := url.Parse(databaseURL)
	if err != nil {
		return "", fmt.Errorf("parse database url: %w", err)
	}
	q := u.Query()
	q.Set("x-migrations-table", table)
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// ServiceMigrationsTable returns the convention-based bookkeeping table
// name for a service (e.g. "audit" → "audit_schema_migrations"). Exposed
// so the Makefile and ops tooling can stay in lockstep with the Go
// runtime.
func ServiceMigrationsTable(service string) string {
	return service + "_schema_migrations"
}

// RunMigrations applies all pending up-migrations from migrationsDir against
// databaseURL. migrationsDir must be a local path (e.g. "services/document/migrations").
// A no-op run (already up to date) is not an error.
func RunMigrations(databaseURL, migrationsDir string) error {
	m, err := migrate.New("file://"+migrationsDir, databaseURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() {
		_, _ = m.Close()
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate up: %w", err)
	}
	return nil
}

// RollbackMigration rolls back the most recent migration step.
func RollbackMigration(databaseURL, migrationsDir string) error {
	m, err := migrate.New("file://"+migrationsDir, databaseURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Steps(-1); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("migrate down 1: %w", err)
	}
	return nil
}

// ForceVersion overrides the migrate bookkeeping to version. Use only when
// recovering from a failed migration.
func ForceVersion(databaseURL, migrationsDir string, version int) error {
	m, err := migrate.New("file://"+migrationsDir, databaseURL)
	if err != nil {
		return fmt.Errorf("init migrator: %w", err)
	}
	defer func() { _, _ = m.Close() }()
	return m.Force(version)
}

// RunServiceMigrations is the §4.1 / A5 canonical entrypoint: applies
// all pending up-migrations from migrationsDir using `<service>_schema_migrations`
// as the bookkeeping table. Every service that deploys schema should call
// this (or invoke `make migrate-up SERVICE=x`) at startup.
func RunServiceMigrations(databaseURL, migrationsDir, serviceName string) error {
	u, err := withMigrationsTable(databaseURL, ServiceMigrationsTable(serviceName))
	if err != nil {
		return err
	}
	return RunMigrations(u, migrationsDir)
}

// RollbackServiceMigration mirrors RunServiceMigrations but steps one
// migration back. Intended for operator use; never called from service
// startup.
func RollbackServiceMigration(databaseURL, migrationsDir, serviceName string) error {
	u, err := withMigrationsTable(databaseURL, ServiceMigrationsTable(serviceName))
	if err != nil {
		return err
	}
	return RollbackMigration(u, migrationsDir)
}
