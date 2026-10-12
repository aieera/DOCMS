// dms-admin kms rewrap — rotation re-wrap.
//
// After `dms-admin kms rotate` mints a new live KEK version in
// tenant_keks, existing blobs are still wrapped under the retired
// version. This walks the tenant's content_blobs and re-wraps each
// stale DEK under the live alias IN PLACE — no bytes move — via the
// storage service's /internal/v1/rewrap-dek endpoint (which verifies
// each re-wrap round-trips before touching the row).
//
// Scope: only NON-regional aliases (base "vaultdms/tenant/<uuid>" or
// versioned "...@v<N>"). Regional blobs ("…/<region>…") are owned by
// `kms rewrap-regional`; this command leaves them alone.
//
// Dry-run by default; --execute performs the re-wrap.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

func kmsRewrap(args []string) {
	fs := flag.NewFlagSet("kms rewrap", flag.ExitOnError)
	tenant := fs.String("tenant", "", "tenant uuid (required)")
	execute := fs.Bool("execute", false, "actually perform the re-wrap; default is dry-run")
	limit := fs.Int("limit", 1000, "max blobs to process in one run (resumable)")
	_ = fs.Parse(args)
	if *tenant == "" {
		fmt.Fprintln(os.Stderr, "--tenant is required")
		os.Exit(2)
	}

	tenantID := mustTenant(*tenant)
	pool := mustPool()
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	target, pending, err := rewrapPlan(ctx, pool, tenantID, *limit)
	if err != nil {
		fatal("plan rewrap for tenant %s: %v", *tenant, err)
	}

	fmt.Printf("target live alias: %s\n\n", target)
	fmt.Printf("%-38s %-34s %-12s\n", "BLOB_ID", "CURRENT_KEK", "SHA")
	for _, b := range pending {
		short := b.sha
		if len(short) > 10 {
			short = short[:10] + "…"
		}
		fmt.Printf("%-38s %-34s %-12s\n", b.id, b.curKEK, short)
	}
	fmt.Printf("\n%d blob(s) selected (cap %d).\n", len(pending), *limit)

	if !*execute {
		fmt.Println("\nDry run — no changes. Pass --execute to call the storage service's rewrap-dek endpoint.")
		return
	}

	endpoint := os.Getenv("SEDOC_STORAGE_REWRAP_URL")
	if endpoint == "" {
		endpoint = "http://storage:8081/internal/v1/rewrap-dek"
	}
	key := os.Getenv("SEDOC_INTERNAL_API_KEY")
	if key == "" {
		fatal("--execute requires SEDOC_INTERNAL_API_KEY (the storage internal-service key)")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var done, failed int
	for _, b := range pending {
		reqBody, _ := json.Marshal(map[string]string{"tenant_id": *tenant, "blob_id": b.id})
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody)) // #nosec G704 -- endpoint is the operator-set SEDOC_STORAGE_REWRAP_URL (in-cluster storage default)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Key", key)
		resp, err := client.Do(req) // #nosec G704 -- endpoint is the operator-set SEDOC_STORAGE_REWRAP_URL (in-cluster storage default)
		if err != nil {
			fmt.Printf("  %s  ERROR  %v\n", b.id, err)
			failed++
			continue
		}
		rb, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Printf("  %s  HTTP %d  %s\n", b.id, resp.StatusCode, trimBody(rb))
			failed++
			continue
		}
		fmt.Printf("  %s  OK  %s\n", b.id, trimBody(rb))
		done++
	}
	fmt.Printf("\nre-wrapped %d, failed %d (of %d selected).\n", done, failed, len(pending))
	if failed > 0 {
		os.Exit(1)
	}
}

// rewrapBlob is one blob still wrapped under a non-live, non-regional KEK.
type rewrapBlob struct{ id, curKEK, sha string }

// rewrapPlan resolves the tenant's live KEK alias (the re-wrap target) and
// the non-regional blobs not yet on it. tenant_keks and content_blobs are
// FORCE ROW LEVEL SECURITY, so both reads run in the tenant's context.
func rewrapPlan(ctx context.Context, pool *pgxpool.Pool, tenantID uuid.UUID, limit int) (target string, pending []rewrapBlob, err error) {
	err = database.WithTenantTx(ctx, pool, tenantID, func(tx pgx.Tx) error {
		if qErr := tx.QueryRow(ctx, `
			SELECT alias FROM tenant_keks
			 WHERE tenant_id = $1 AND retired_at IS NULL
			 ORDER BY version DESC LIMIT 1
		`, tenantID).Scan(&target); qErr != nil {
			return fmt.Errorf("no live KEK (run `dms-admin kms create`/`rotate` first): %w", qErr)
		}

		// Enumerate non-regional blobs not already on the live alias. The
		// region filter (no slash after the uuid) keeps this orthogonal to
		// rewrap-regional; the kek_id <> target filter skips already-live rows.
		rows, qErr := tx.Query(ctx, `
			SELECT id::text, COALESCE(kek_id, ''), sha256_hash
			  FROM content_blobs
			 WHERE tenant_id = $1
			   AND COALESCE(kek_id, '') <> ''
			   AND COALESCE(kek_id, '') !~ '^vaultdms/tenant/[^/]+/'
			   AND kek_id <> $2
			 ORDER BY created_at
			 LIMIT $3
		`, tenantID, target, limit)
		if qErr != nil {
			return fmt.Errorf("enumerate blobs: %w", qErr)
		}
		var collectErr error
		pending, collectErr = pgx.CollectRows(rows, func(r pgx.CollectableRow) (rewrapBlob, error) {
			var b rewrapBlob
			scanErr := r.Scan(&b.id, &b.curKEK, &b.sha)
			return b, scanErr
		})
		return collectErr
	})
	if err != nil {
		return "", nil, err
	}
	return target, pending, nil
}
