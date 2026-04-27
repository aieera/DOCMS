// Wave 12.3 — dms-admin kms rewrap-regional.
//
// Walks every content_blobs row for the given tenant and re-keys
// each blob under the region-local KEK introduced in ADR 0026
// (kek_id suffix "/<region>"). The underlying primitive is the
// storage service's ReencryptBlob; this CLI orchestrates the loop,
// respects idempotence (already-regional blobs no-op), and prints
// progress so operators can resume after an interrupt.
//
// CURRENT STATE (dry-run only):
// The CLI enumerates pre-regional blobs from Postgres directly and
// prints what WOULD be migrated. The --execute path is unimplemented;
// triggering re-encrypt requires a storage-service RPC the storage
// service does not yet expose.
//
// REMAINING WORK to close GAP-6 (estimated 1 day):
//
//   1. Storage proto: add `rpc ReencryptBlob(...)` mirroring ShredBlobs
//      + TransitionBlobsTier (commits c433d49 + 52a2f43). Service
//      method service.ReencryptBlob already exists in
//      services/storage/internal/service/reencrypt.go — only the
//      handler binding + proto definition are missing.
//
//   2. dms-admin go.mod: add the proto/gen/go dependency and a gRPC
//      client init. cmd/dms-admin is a separate go module; the proto
//      package needs explicit replace + require lines.
//
//   3. Replace the per-blob fmt.Printf below with the gRPC client
//      call. Idempotency + progress are already correct.
//
// Until that lands, --execute exits non-zero with the documented
// error so a CI-driven runbook can detect the gap.
package main

import (
	"context"
	"flag"
	"fmt"
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

	// Find blobs whose kek_id is pre-regional (no "/<region>"
	// suffix). The alias pattern is "vaultdms/tenant/<uuid>"
	// (optional "@v<N>" rotation); regional aliases end with
	// "/<region>" or "/<region>@v<N>".
	//
	// Postgres regex: start/end anchors + the slash check. Anything
	// matching "/[^/]+(@v[0-9]+)?$" at the end has a region.
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
		   AND COALESCE(b.kek_id, '') !~ '/[^/]+(@v[0-9]+)?$'
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
		fmt.Println("Storage-service internal endpoint (`/internal/v1/reencrypt-blob`) ships in Wave 12.3b;")
		fmt.Println("until then the CLI enumerates + prints but does not re-encrypt.")
		return
	}

	// --execute path: the CLI doesn't hold KMS / S3 clients, so
	// actual re-encryption lives in the storage service. Rather
	// than embed a second KMS wiring here we print the command
	// operators should run once the HTTP endpoint ships. The shape
	// is stable and documented so scripts can build against it.
	fmt.Println()
	fmt.Println("--execute is not yet implemented (Wave 12.3b wires the storage-service endpoint).")
	fmt.Println("Each blob below would be POSTed to /internal/v1/reencrypt-blob:")
	for _, b := range pending {
		fmt.Printf("  POST /internal/v1/reencrypt-blob {\"tenant_id\":%q,\"blob_id\":%q,\"target_region\":%q}\n",
			*tenant, b.id, b.targetRgn)
	}
	os.Exit(2)
}
