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

	pool := mustPool()
	defer pool.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()

	// Resolve the live KEK alias (the re-wrap target).
	var target string
	if err := pool.QueryRow(ctx, `
		SELECT alias FROM tenant_keks
		 WHERE tenant_id = $1::uuid AND retired_at IS NULL
		 ORDER BY version DESC LIMIT 1
	`, *tenant).Scan(&target); err != nil {
		fatal("no live KEK for tenant %s (run `dms-admin kms create`/`rotate` first): %v", *tenant, err)
	}

	// Enumerate non-regional blobs not already on the live alias. The
	// region filter (no slash after the uuid) keeps this orthogonal to
	// rewrap-regional; the kek_id <> target filter skips already-live rows.
	rows, err := pool.Query(ctx, `
		SELECT id::text, COALESCE(kek_id, ''), sha256_hash
		  FROM content_blobs
		 WHERE tenant_id = $1::uuid
		   AND COALESCE(kek_id, '') <> ''
		   AND COALESCE(kek_id, '') !~ '^vaultdms/tenant/[^/]+/'
		   AND kek_id <> $2
		 ORDER BY created_at
		 LIMIT $3
	`, *tenant, target, *limit)
	if err != nil {
		fatal("enumerate blobs: %v", err)
	}
	defer rows.Close()

	type blobRow struct{ id, curKEK, sha string }
	var pending []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.id, &b.curKEK, &b.sha); err != nil {
			fatal("scan: %v", err)
		}
		pending = append(pending, b)
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
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Internal-Service-Key", key)
		resp, err := client.Do(req)
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
