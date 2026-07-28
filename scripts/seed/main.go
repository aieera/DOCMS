// Command seed populates a dev/test database with a default organization
// and admin user so a fresh `make setup` gives a working login.
//
// Environment variables:
//
//	DATABASE_URL            Postgres connection string (required)
//	SEED_ADMIN_EMAIL        Default: admin@acme.local
//	SEED_ADMIN_PASSWORD     Default: ChangeMe!Now2026
//	SEED_TENANT_SLUG        Default: acme
//	SEED_TENANT_NAME        Default: Acme Corporation
//	SEED_REGION             Default: me-central-1
//
// Flags:
//
//	--force  Truncate existing seed rows for the tenant slug and recreate.
//
// The script is idempotent by default: re-running is a no-op if the
// tenant already exists. Use --force in integration tests that need a
// clean start.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"
)

const (
	defaultEmail    = "admin@acme.local"
	defaultPassword = "ChangeMe!Now2026"
	defaultSlug     = "acme"
	defaultName     = "Acme Corporation"
	defaultRegion   = "me-central-1"
	defaultPlan     = "enterprise"
	bcryptCost      = 12
)

type seedInputs struct {
	TenantSlug    string
	TenantName    string
	TenantRegion  string
	TenantPlan    string
	AdminEmail    string
	AdminPassword string
	Force         bool
}

func main() {
	force := flag.Bool("force", false, "truncate existing seed rows and recreate")
	flag.Parse()

	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		fatalf("DATABASE_URL not set")
	}

	in := seedInputs{
		TenantSlug:    envOr("SEED_TENANT_SLUG", defaultSlug),
		TenantName:    envOr("SEED_TENANT_NAME", defaultName),
		TenantRegion:  envOr("SEED_REGION", defaultRegion),
		TenantPlan:    envOr("SEED_PLAN", defaultPlan),
		AdminEmail:    envOr("SEED_ADMIN_EMAIL", defaultEmail),
		AdminPassword: envOr("SEED_ADMIN_PASSWORD", defaultPassword),
		Force:         *force,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		fatalf("connect postgres: %v", err)
	}
	defer pool.Close()

	if err := run(ctx, pool, in); err != nil {
		fatalf("%v", err)
	}
	fmt.Println("seed complete.")
}

func run(ctx context.Context, pool *pgxpool.Pool, in seedInputs) error {
	if in.Force {
		if err := truncate(ctx, pool, in.TenantSlug); err != nil {
			return fmt.Errorf("truncate: %w", err)
		}
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.AdminPassword), bcryptCost)
	if err != nil {
		return fmt.Errorf("bcrypt: %w", err)
	}

	tenantID, err := upsertOrganization(ctx, pool, in)
	if err != nil {
		return fmt.Errorf("organization: %w", err)
	}
	fmt.Printf("  organization: %s (id=%s)\n", in.TenantSlug, tenantID)

	// Everything below must be tenant-scoped for RLS. Open one tx, set
	// app.current_tenant, do all tenant-scoped inserts, commit.
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return fmt.Errorf("set tenant: %w", err)
	}

	userID, err := upsertAdminUser(ctx, tx, tenantID, in.AdminEmail, string(hash))
	if err != nil {
		return fmt.Errorf("admin user: %w", err)
	}
	fmt.Printf("  admin user:   %s (id=%s)\n", in.AdminEmail, userID)

	if err := upsertEveryoneGroup(ctx, tx, tenantID, userID); err != nil {
		return fmt.Errorf("everyone group: %w", err)
	}
	fmt.Printf("  group:        everyone\n")

	wsID, err := upsertWorkspace(ctx, tx, tenantID, userID, in.TenantRegion)
	if err != nil {
		return fmt.Errorf("workspace: %w", err)
	}
	fmt.Printf("  workspace:    Default Workspace (id=%s)\n", wsID)

	if err := upsertWorkspaceAdmin(ctx, tx, tenantID, wsID, userID); err != nil {
		return fmt.Errorf("workspace member: %w", err)
	}

	if err := upsertRootFolder(ctx, tx, tenantID, wsID, userID); err != nil {
		return fmt.Errorf("root folder: %w", err)
	}
	fmt.Printf("  folder:       Shared Documents\n")

	return tx.Commit(ctx)
}

func upsertOrganization(ctx context.Context, pool *pgxpool.Pool, in seedInputs) (string, error) {
	var id string
	err := pool.QueryRow(ctx, `
		INSERT INTO organizations (name, slug, plan, primary_region, settings)
		VALUES ($1, $2, $3, $4, '{}'::jsonb)
		ON CONFLICT (slug) DO UPDATE SET name = EXCLUDED.name
		RETURNING id::text
	`, in.TenantName, in.TenantSlug, in.TenantPlan, in.TenantRegion).Scan(&id)
	return id, err
}

func upsertAdminUser(ctx context.Context, tx pgx.Tx, tenantID, email, hash string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO users (tenant_id, email, display_name, password_hash, role, status, mfa_enabled)
		VALUES ($1::uuid, $2, 'Admin User', $3, 'owner', 'active', false)
		ON CONFLICT (tenant_id, email) DO UPDATE SET
			password_hash = EXCLUDED.password_hash,
			role = 'owner',
			status = 'active'
		RETURNING id::text
	`, tenantID, email, hash).Scan(&id)
	return id, err
}

func upsertEveryoneGroup(ctx context.Context, tx pgx.Tx, tenantID, userID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO groups (tenant_id, name, description, created_by)
		VALUES ($1::uuid, 'everyone', 'All users in the organization', $2::uuid)
		ON CONFLICT (tenant_id, name) DO NOTHING
	`, tenantID, userID)
	return err
}

func upsertWorkspace(ctx context.Context, tx pgx.Tx, tenantID, userID, region string) (string, error) {
	// Workspaces table has no natural unique key, so check by name first.
	var id string
	err := tx.QueryRow(ctx, `
		SELECT id::text FROM workspaces
		WHERE tenant_id = $1::uuid AND name = 'Default Workspace' AND deleted_at IS NULL
		LIMIT 1
	`, tenantID).Scan(&id)
	if err == nil {
		return id, nil
	}
	if err != pgx.ErrNoRows {
		return "", err
	}
	// is_default: the onboarding workspace is the one every tenant user
	// can see (migration 000100 + the document service's list/get rules).
	err = tx.QueryRow(ctx, `
		INSERT INTO workspaces (tenant_id, name, description, region_pin, created_by, is_default)
		VALUES ($1::uuid, 'Default Workspace', 'Auto-created for onboarding', $2, $3::uuid, true)
		RETURNING id::text
	`, tenantID, region, userID).Scan(&id)
	return id, err
}

func upsertWorkspaceAdmin(ctx context.Context, tx pgx.Tx, tenantID, wsID, userID string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role, added_by)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'admin', $3::uuid)
		ON CONFLICT (tenant_id, workspace_id, user_id) DO NOTHING
	`, tenantID, wsID, userID)
	return err
}

func upsertRootFolder(ctx context.Context, tx pgx.Tx, tenantID, wsID, userID string) error {
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM folders
			WHERE tenant_id = $1::uuid AND workspace_id = $2::uuid
			  AND name = 'Shared Documents' AND parent_folder_id IS NULL
			  AND deleted_at IS NULL
		)
	`, tenantID, wsID).Scan(&exists)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO folders (tenant_id, workspace_id, parent_folder_id, name, path, depth, created_by)
		VALUES ($1::uuid, $2::uuid, NULL, 'Shared Documents', 'shared_documents'::ltree, 0, $3::uuid)
	`, tenantID, wsID, userID)
	return err
}

// truncate removes all rows for the named tenant. --force path only.
func truncate(ctx context.Context, pool *pgxpool.Pool, slug string) error {
	var id string
	err := pool.QueryRow(ctx,
		`SELECT id::text FROM organizations WHERE slug = $1 LIMIT 1`, slug).Scan(&id)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	// Order matters: FK children first. All tenant-scoped rows cascade
	// deletion from the organization manually since we do not use
	// ON DELETE CASCADE anywhere.
	stmts := []string{
		`DELETE FROM folders           WHERE tenant_id = $1::uuid`,
		`DELETE FROM workspace_members WHERE tenant_id = $1::uuid`,
		`DELETE FROM workspaces        WHERE tenant_id = $1::uuid`,
		`DELETE FROM groups            WHERE tenant_id = $1::uuid`,
		`DELETE FROM users             WHERE tenant_id = $1::uuid`,
		`DELETE FROM organizations     WHERE id        = $1::uuid`,
	}
	for _, s := range stmts {
		if _, err := pool.Exec(ctx, s, id); err != nil {
			return fmt.Errorf("truncate %q: %w", strings.Fields(s)[2], err)
		}
	}
	fmt.Printf("  force: removed existing rows for slug=%s\n", slug)
	return nil
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed: "+format+"\n", args...)
	os.Exit(1)
}
