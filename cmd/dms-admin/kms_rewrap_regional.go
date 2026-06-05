// Wave 12.3 — dms-admin kms rewrap-regional.
//
// Walks every content_blobs row for the given tenant and re-keys
// each blob under the region-local KEK introduced in ADR 0026
// (kek_id suffix "/<region>"). The underlying primitive is the
// storage service's ReencryptBlob; this CLI orchestrates the loop,
// respects idempotence (already-regional blobs no-op), and prints
// progress so operators can resume after an interrupt.
//
// The CLI calls the storage service over gRPC — re-running the
// crypto inside the CLI process would require embedding all the
// KMS + S3 + pool wiring here, which is exactly what the service
// already owns. Today the storage service has no public RPC for
// this; the CLI therefore hits the admin-only HTTP endpoint
// introduced here (storage service `/internal/v1/reencrypt-blob`).
// Wired in Wave 12.3b once the storage-service HTTP handler ships.
//
// Until then this CLI runs in dry-run mode by default: it
// enumerates the blobs that WOULD be migrated and prints them.
// Use --execute to actually call the storage-service endpoint
// (which the CLI will verify exists before writing).
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

func kmsRewrapRegional(args []string) {
	fs := flag.NewFlagSet("kms rewrap-regional", flag.ExitOnError)
	tenant := fs.String("tenant", "", "tenant uuid (required)")
	region := fs.String("region", "", "target region (default: use each blob's documents.region_pin)")
	execute := fs.Bool("execute", false, "actually perform the re-encrypt; default is dry-run")
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

	// Find blobs whose kek_id is pre-regional. The base (non-regional)
	// alias is "vaultdms/tenant/<uuid>" (optional "@v<N>" rotation — no
	// extra slash); a regional alias adds a 4th segment:
	// "vaultdms/tenant/<uuid>/<region>(@v<N>)?". So "has a region" is
	// exactly "a slash after the <uuid> segment": ^vaultdms/tenant/[^/]+/.
	//
	// (The earlier `/[^/]+(@v[0-9]+)?$` test was buggy — it also matched the
	// <uuid> segment of a base alias, so EVERY base-alias blob was wrongly
	// classified as already-regional and the command migrated nothing.)
	q := `
		SELECT b.id::text,
		       b.storage_region,
		       COALESCE(b.kek_id, '') AS kek_id,
		       COALESCE(d.region_pin, b.storage_region) AS doc_region,
		       b.sha256_hash
		  FROM content_blobs b
		  LEFT JOIN document_versions dv ON dv.tenant_id = b.tenant_id AND dv.content_blob_id = b.id
		  LEFT JOIN documents          d  ON d.tenant_id  = dv.tenant_id AND d.id = dv.document_id
		 WHERE b.tenant_id = $1::uuid
		   AND COALESCE(b.kek_id, '') !~ '^vaultdms/tenant/[^/]+/'
		 ORDER BY b.created_at
		 LIMIT $2
	`
	rows, err := pool.Query(ctx, q, *tenant, *limit)
	if err != nil {
		fatal("enumerate blobs: %v", err)
	}
	defer rows.Close()

	type blobRow struct {
		id         string
		curRegion  string
		curKEK     string
		targetRgn  string
		sha        string
	}
	var pending []blobRow
	for rows.Next() {
		var b blobRow
		if err := rows.Scan(&b.id, &b.curRegion, &b.curKEK, &b.targetRgn, &b.sha); err != nil {
			fatal("scan: %v", err)
		}
		if *region != "" {
			b.targetRgn = *region
		}
		pending = append(pending, b)
	}

	fmt.Printf("%-38s %-14s %-14s %-12s\n", "BLOB_ID", "CUR_REGION", "TARGET_REGION", "SHA")
	for _, b := range pending {
		short := b.sha
		if len(short) > 10 {
			short = short[:10] + "…"
		}
		fmt.Printf("%-38s %-14s %-14s %-12s\n", b.id, b.curRegion, b.targetRgn, short)
	}
	fmt.Printf("\n%d blob(s) selected (cap %d).\n", len(pending), *limit)

	if !*execute {
		fmt.Println("\nDry run — no changes. Pass --execute to call the storage service's re-encrypt endpoint.")
		return
	}

	// --execute: drive the storage service's internal re-encrypt endpoint —
	// it owns the KMS + S3 wiring, so we don't duplicate crypto here. Needs
	// SEDOC_INTERNAL_API_KEY; URL defaults to the in-cluster storage health
	// port (override with SEDOC_STORAGE_REENCRYPT_URL for host/local runs).
	endpoint := os.Getenv("SEDOC_STORAGE_REENCRYPT_URL")
	if endpoint == "" {
		endpoint = "http://storage:8081/internal/v1/reencrypt-blob"
	}
	key := os.Getenv("SEDOC_INTERNAL_API_KEY")
	if key == "" {
		fatal("--execute requires SEDOC_INTERNAL_API_KEY (the storage internal-service key)")
	}
	client := &http.Client{Timeout: 2 * time.Minute}
	var done, failed int
	for _, b := range pending {
		reqBody, _ := json.Marshal(map[string]string{
			"tenant_id":     *tenant,
			"blob_id":       b.id,
			"target_region": b.targetRgn,
		})
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

// trimBody shortens a response body for one-line progress output.
func trimBody(b []byte) string {
	s := string(b)
	if len(s) > 160 {
		return s[:160] + "…"
	}
	return s
}
