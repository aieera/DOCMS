// dms-admin kms — per-tenant KEK operations.
//
// Subcommands:
//
//	dms-admin kms list [--tenant <uuid>]        print tenant_keks rows
//	dms-admin kms rotate --tenant <uuid>        add v+1, retire prior version
//	dms-admin kms create --tenant <uuid>        allocate v1 for a new tenant
//
// Rotation is NOT destructive: the prior version's row stays in the
// table with retired_at set so historical ciphertext continues to
// decrypt. New encrypts use the new version's alias. See ADR 0022
// and `docs/runbooks/06-key-management.md`.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

func kmsMain(args []string) {
	if len(args) == 0 {
		fmt.Println("Usage: dms-admin kms <subcommand>")
		fmt.Println("Subcommands: list, create, rotate, rewrap, rewrap-regional")
		os.Exit(2)
	}
	switch args[0] {
	case "list":
		kmsList(args[1:])
	case "create":
		kmsCreate(args[1:])
	case "rotate":
		kmsRotate(args[1:])
	case "rewrap":
		// Rotation re-wrap: re-wrap each non-regional blob's DEK in
		// place under the tenant's live KEK version (post `kms rotate`).
		kmsRewrap(args[1:])
	case "rewrap-regional":
		// Wave 12.3: enumerate blobs whose kek_id lacks a region
		// suffix and re-encrypt each under the region-local KEK.
		kmsRewrapRegional(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown kms subcommand: %s\n", args[0])
		os.Exit(2)
	}
}

func kmsList(args []string) {
	fs := flag.NewFlagSet("kms list", flag.ExitOnError)
	tenant := fs.String("tenant", "", "filter by tenant uuid (default: all)")
	_ = fs.Parse(args)
	var tenantID uuid.UUID
	if *tenant != "" {
		tenantID = mustTenant(*tenant)
	}

	pool := mustPool()
	defer pool.Close()
	keks, err := listKEKs(context.Background(), pool, tenantID)
	if err != nil {
		fatal("query tenant_keks: %v", err)
	}

	fmt.Printf("%-38s %4s  %-50s %-20s %s\n", "TENANT_ID", "VER", "ALIAS", "CREATED", "RETIRED")
	for _, k := range keks {
		ret := "-"
		if k.Retired != nil {
			ret = k.Retired.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-38s %4d  %-50s %-20s %s\n",
			k.TenantID, k.Version, k.Alias, k.Created.UTC().Format(time.RFC3339), ret)
	}
}

func kmsCreate(args []string) {
	fs := flag.NewFlagSet("kms create", flag.ExitOnError)
	tenant := fs.String("tenant", "", "tenant uuid (required)")
	_ = fs.Parse(args)
	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "--tenant is required")
		os.Exit(2)
	}
	tenantID := mustTenant(*tenant)

	pool := mustPool()
	defer pool.Close()
	alias, err := createKEK(context.Background(), pool, tenantID)
	if err != nil {
		fatal("insert tenant_keks: %v", err)
	}
	fmt.Printf("alias=%s version=1\n", alias)
}

func kmsRotate(args []string) {
	fs := flag.NewFlagSet("kms rotate", flag.ExitOnError)
	tenant := fs.String("tenant", "", "tenant uuid (required)")
	_ = fs.Parse(args)
	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "--tenant is required")
		os.Exit(2)
	}
	tenantID := mustTenant(*tenant)

	pool := mustPool()
	defer pool.Close()
	liveVer, newVer, newAlias, err := rotateKEK(context.Background(), pool, tenantID)
	if err != nil {
		fatal("rotate kek for tenant %s: %v", *tenant, err)
	}
	fmt.Printf("rotated: tenant=%s retired=v%d live=v%d alias=%s\n",
		*tenant, liveVer, newVer, newAlias)
	fmt.Println("note: existing ciphertext still decrypts under v" +
		fmt.Sprint(liveVer) + "; new encrypts use the new alias.")
	fmt.Println("to re-wrap existing blobs under the new version online, run " +
		"`dms-admin kms rewrap --tenant " + *tenant + " --execute` " +
		"(dry-run first without --execute).")
	fmt.Println("to migrate historical blobs to a region-local KEK, run " +
		"`dms-admin kms rewrap-regional --tenant " + *tenant + " --execute`.")
}

// kekRow is one tenant_keks row.
type kekRow struct {
	TenantID uuid.UUID
	Version  int
	Alias    string
	Created  time.Time
	Retired  *time.Time
}

// tenant_keks is FORCE ROW LEVEL SECURITY (document migration 000108):
// even the table owner sees only the rows of the tenant in
// app.current_tenant, so every KEK operation runs in the target tenant's
// WithTenantTx, whatever role the operator connects as.

// listKEKs returns one tenant's KEK history, or every tenant's when
// tenantID is uuid.Nil. "Every tenant" includes soft-deleted
// organizations (tenant_keks.tenant_id references organizations), so a
// tenant scheduled for crypto-shredding still shows its keys.
func listKEKs(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) ([]kekRow, error) {
	tenants := []uuid.UUID{tenantID}
	if tenantID == uuid.Nil {
		rows, err := pool.Query(ctx, `SELECT id FROM organizations ORDER BY id`)
		if err != nil {
			return nil, fmt.Errorf("enumerate tenants: %w", err)
		}
		if tenants, err = pgx.CollectRows(rows, pgx.RowTo[uuid.UUID]); err != nil {
			return nil, fmt.Errorf("enumerate tenants: %w", err)
		}
	}
	var out []kekRow
	for _, id := range tenants {
		err := database.WithTenantTx(ctx, pool, id, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT tenant_id, version, alias, created_at, retired_at
				  FROM tenant_keks WHERE tenant_id = $1 ORDER BY version`, id)
			if err != nil {
				return err
			}
			keks, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (kekRow, error) {
				var k kekRow
				scanErr := r.Scan(&k.TenantID, &k.Version, &k.Alias, &k.Created, &k.Retired)
				return k, scanErr
			})
			out = append(out, keks...)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("tenant %s: %w", id, err)
		}
	}
	return out, nil
}

// createKEK allocates v1 for a tenant (idempotent) and returns its alias.
func createKEK(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (string, error) {
	alias := fmt.Sprintf("vaultdms/tenant/%s", tenantID)
	err := database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO tenant_keks (tenant_id, version, alias, created_at)
			VALUES ($1, 1, $2, now())
			ON CONFLICT (tenant_id, version) DO NOTHING
		`, tenantID, alias)
		return err
	})
	return alias, err
}

// rotateKEK retires the tenant's live KEK version and inserts v+1.
func rotateKEK(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID) (liveVer, newVer int, newAlias string, err error) {
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		// Read the currently-live version.
		if qErr := tx.QueryRow(ctx, `
			SELECT version FROM tenant_keks
			 WHERE tenant_id = $1 AND retired_at IS NULL
			 ORDER BY version DESC LIMIT 1
		`, tenantID).Scan(&liveVer); qErr != nil {
			return fmt.Errorf("no live KEK (run `dms-admin kms create` first): %w", qErr)
		}
		// Retire the current live row.
		if _, execErr := tx.Exec(ctx, `
			UPDATE tenant_keks SET retired_at = now()
			 WHERE tenant_id = $1 AND version = $2
		`, tenantID, liveVer); execErr != nil {
			return fmt.Errorf("retire live kek: %w", execErr)
		}
		// Insert a new live row.
		newVer = liveVer + 1
		newAlias = fmt.Sprintf("vaultdms/tenant/%s@v%d", tenantID, newVer)
		if _, execErr := tx.Exec(ctx, `
			INSERT INTO tenant_keks (tenant_id, version, alias, created_at)
			VALUES ($1, $2, $3, now())
		`, tenantID, newVer, newAlias); execErr != nil {
			return fmt.Errorf("insert new kek: %w", execErr)
		}
		return nil
	})
	if err != nil {
		return 0, 0, "", err
	}
	return liveVer, newVer, newAlias, nil
}

// mustTenant parses a --tenant flag value or exits with a usage error.
func mustTenant(s string) uuid.UUID {
	id, err := uuid.Parse(s)
	if err != nil || id == uuid.Nil {
		fmt.Fprintf(os.Stderr, "invalid --tenant %q: must be a non-zero uuid\n", s)
		os.Exit(2)
	}
	return id
}

func mustPool() *pgxpool.Pool {
	url := envOrDefault("SEDOC_DATABASE_URL",
		envOrDefault("DATABASE_URL",
			"postgres://sedoc:devpassword@localhost:15432/sedoc?sslmode=disable"))
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		fatal("db connect: %v", err)
	}
	return pool
}
