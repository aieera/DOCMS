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

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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

	pool := mustPool()
	defer pool.Close()
	ctx := context.Background()

	q := `SELECT tenant_id::text, version, alias, created_at, retired_at
	        FROM tenant_keks`
	var rows pgx.Rows
	var err error
	if *tenant != "" {
		q += ` WHERE tenant_id = $1::uuid ORDER BY version`
		rows, err = pool.Query(ctx, q, *tenant)
	} else {
		q += ` ORDER BY tenant_id, version`
		rows, err = pool.Query(ctx, q)
	}
	if err != nil {
		fatal("query tenant_keks: %v", err)
	}
	defer rows.Close()

	fmt.Printf("%-38s %4s  %-50s %-20s %s\n", "TENANT_ID", "VER", "ALIAS", "CREATED", "RETIRED")
	for rows.Next() {
		var tid, alias string
		var ver int
		var created time.Time
		var retired *time.Time
		if err := rows.Scan(&tid, &ver, &alias, &created, &retired); err != nil {
			fatal("scan: %v", err)
		}
		ret := "-"
		if retired != nil {
			ret = retired.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-38s %4d  %-50s %-20s %s\n",
			tid, ver, alias, created.UTC().Format(time.RFC3339), ret)
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

	pool := mustPool()
	defer pool.Close()
	ctx := context.Background()

	alias := fmt.Sprintf("vaultdms/tenant/%s", *tenant)
	_, err := pool.Exec(ctx, `
		INSERT INTO tenant_keks (tenant_id, version, alias, created_at)
		VALUES ($1::uuid, 1, $2, now())
		ON CONFLICT (tenant_id, version) DO NOTHING
	`, *tenant, alias)
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

	pool := mustPool()
	defer pool.Close()
	ctx := context.Background()

	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		fatal("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Read the currently-live version.
	var liveVer int
	err = tx.QueryRow(ctx, `
		SELECT version FROM tenant_keks
		 WHERE tenant_id = $1::uuid AND retired_at IS NULL
		 ORDER BY version DESC LIMIT 1
	`, *tenant).Scan(&liveVer)
	if err != nil {
		fatal("no live KEK for tenant %s (run `dms-admin kms create` first): %v", *tenant, err)
	}

	// Retire the current live row.
	if _, err := tx.Exec(ctx, `
		UPDATE tenant_keks SET retired_at = now()
		 WHERE tenant_id = $1::uuid AND version = $2
	`, *tenant, liveVer); err != nil {
		fatal("retire live kek: %v", err)
	}

	// Insert a new live row.
	newVer := liveVer + 1
	newAlias := fmt.Sprintf("vaultdms/tenant/%s@v%d", *tenant, newVer)
	if _, err := tx.Exec(ctx, `
		INSERT INTO tenant_keks (tenant_id, version, alias, created_at)
		VALUES ($1::uuid, $2, $3, now())
	`, *tenant, newVer, newAlias); err != nil {
		fatal("insert new kek: %v", err)
	}

	if err := tx.Commit(ctx); err != nil {
		fatal("commit: %v", err)
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
