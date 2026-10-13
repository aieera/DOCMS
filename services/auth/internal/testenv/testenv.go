//go:build integration

// Package testenv builds the real-schema test databases the auth
// integration and prod-posture suites share. It runs the document
// schema-of-record chain (services/document/migrations, which owns every
// auth table) and then auth's own chain (the SECURITY DEFINER lookups),
// so tests exercise the production DDL, RLS policies and grants instead
// of hand-copied fixtures that drift.
//
// Compiled only under the `integration` build tag.
package testenv

import (
	"context"
	"fmt"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/testutil"
)

// Dirs returns the absolute document and auth migration directories.
func Dirs() (documentDir, authDir string) {
	_, file, _, _ := runtime.Caller(0)
	authRoot := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(authRoot, "..", "document", "migrations"),
		filepath.Join(authRoot, "migrations")
}

// Env is a superuser (dev-posture) Postgres with the full schema plus a
// Redis. Logic tests use it; RLS behaviour is covered by ProdPosture.
type Env struct {
	DSN   string
	Pool  *pgxpool.Pool
	Redis *redis.Client
}

// Start boots Postgres + Redis and applies the document and auth chains.
// The returned cleanup terminates both containers.
func Start(ctx context.Context) (*Env, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	dsn, pgClean, err := testutil.NewPostgresContainer(ctx)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, pgClean)

	docDir, authDir := Dirs()
	if err := database.RunMigrations(dsn, docDir); err != nil {
		return nil, cleanup, fmt.Errorf("document migrations: %w", err)
	}
	if err := database.RunServiceMigrations(dsn, authDir, "auth"); err != nil {
		return nil, cleanup, fmt.Errorf("auth migrations: %w", err)
	}

	cfg := database.DefaultPoolConfig()
	cfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, cfg)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, pool.Close)

	redisURL, redisClean, err := testutil.NewRedisContainer(ctx)
	if err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, redisClean)
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, cleanup, err
	}
	rdb := redis.NewClient(opts)
	cleanups = append(cleanups, func() { _ = rdb.Close() })

	return &Env{DSN: dsn, Pool: pool, Redis: rdb}, cleanup, nil
}

// ProdPosture returns a prod-posture database (dms_app NOBYPASSRLS app
// pool + superuser seeding pool) carrying the real document and auth
// chains. NewProdPostureDB re-grants DML on every table to dms_app after
// the document chain runs, which would mask document 000001's
// `REVOKE INSERT, UPDATE, DELETE ON organizations FROM dms_app`; the
// revoke is re-applied here so organizations writes are as constrained
// as in production.
func ProdPosture(t testing.TB) (context.Context, *testutil.ProdPostureDB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Second)
	t.Cleanup(cancel)

	docDir, authDir := Dirs()
	db := testutil.NewProdPostureDB(ctx, t, docDir)
	if err := database.RunServiceMigrations(db.SuperDSN, authDir, "auth"); err != nil {
		t.Fatalf("auth migrations: %v", err)
	}
	if _, err := db.Super.Exec(ctx, `
		REVOKE INSERT, UPDATE, DELETE ON organizations FROM dms_app;
		GRANT  SELECT ON organizations TO dms_app;`); err != nil {
		t.Fatalf("restore organizations grants: %v", err)
	}
	return ctx, db
}

// SeedTenant inserts an organization (organizations has no RLS; seeding is
// out-of-band, like provisioning tooling) and returns its id.
func SeedTenant(ctx context.Context, t testing.TB, super *pgxpool.Pool, slug string) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	if _, err := super.Exec(ctx,
		`INSERT INTO organizations (id, name, slug) VALUES ($1, $2, $3)`,
		id, "Org "+slug, slug); err != nil {
		t.Fatalf("seed tenant %s: %v", slug, err)
	}
	return id
}

// UserSpec describes a seeded user.
type UserSpec struct {
	Email        string
	Role         string // default member
	Status       string // default active
	PasswordHash string
	MFAEnabled   bool
	MFASecretEnc string
}

// SeedUser inserts a user row as the superuser and returns its id.
func SeedUser(ctx context.Context, t testing.TB, super *pgxpool.Pool, tenantID uuid.UUID, u UserSpec) uuid.UUID {
	t.Helper()
	if u.Role == "" {
		u.Role = "member"
	}
	if u.Status == "" {
		u.Status = "active"
	}
	id := uuid.Must(uuid.NewV7())
	var secret any
	if u.MFASecretEnc != "" {
		secret = u.MFASecretEnc
	}
	if _, err := super.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, password_hash, role, status,
		                   mfa_enabled, mfa_secret_encrypted)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8, $9)`,
		tenantID, id, u.Email, u.Email, u.PasswordHash, u.Role, u.Status,
		u.MFAEnabled, secret); err != nil {
		t.Fatalf("seed user %s: %v", u.Email, err)
	}
	return id
}

// UniqueSlug returns a slug unique within one shared database.
func UniqueSlug(prefix string) string {
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString()[:8])
}
