// Package activities — residency-migration activities (Wave 8.4).
//
// The migrate workflow is resumable: every activity in here is
// idempotent so a worker crash mid-run can pick up exactly where it
// left off. The `residency_migration_items` row carries per-document
// progress — the workflow asks for pending items, moves each, and
// flips the row to `moved` / `failed` in the same activity call.
//
// Scope note: the actual blob re-encrypt (DEK unwrap under source
// KEK, re-wrap under target KEK, copy ciphertext to target bucket)
// lives in the storage service. This activity updates document.
// region_pin + emits the domain event; physical blob move is
// logged out-of-scope as Wave 12 work (needs storage-service
// cross-region copy API).
package activities

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
)

// EnumerateDocsForMigration populates residency_migration_items with
// one pending row per document matching the filter. Idempotent via
// ON CONFLICT DO NOTHING on (migration_id, document_id).
// Returns the count of newly-queued items.
func (a *Activities) EnumerateDocsForMigration(ctx context.Context, tenantID, migrationID, sourceRegion, workspace, documentClass string) (int, error) {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return 0, fmt.Errorf("tenant_id: %w", err)
	}
	var total int
	err = database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		args := []any{migrationID, tenantID, sourceRegion}
		sql := `
			INSERT INTO residency_migration_items (migration_id, tenant_id, document_id, status, updated_at)
			SELECT $1, $2, d.id, 'pending', now()
			  FROM documents d
			 WHERE d.tenant_id = $2
			   AND d.region_pin = $3
			   AND d.deleted_at IS NULL`
		if workspace != "" {
			sql += fmt.Sprintf(" AND d.workspace_id = $%d", len(args)+1)
			args = append(args, workspace)
		}
		if documentClass != "" {
			sql += fmt.Sprintf(" AND d.document_class = $%d", len(args)+1)
			args = append(args, documentClass)
		}
		sql += ` ON CONFLICT DO NOTHING`
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return fmt.Errorf("enumerate: %w", err)
		}
		total = int(tag.RowsAffected())

		// Update the migrations row with the total.
		_, err = tx.Exec(ctx, `
			UPDATE residency_migrations
			   SET total_docs = (SELECT COUNT(*) FROM residency_migration_items WHERE migration_id = $1),
			       status = 'running'
			 WHERE tenant_id = $2 AND id = $1`,
			migrationID, tenantID)
		return err
	})
	return total, err
}

// NextPendingMigrationDoc returns up to `batch` pending document IDs
// from the migration. Returns nil + no error when nothing is pending
// (the workflow loop exits). Rows are NOT locked — the workflow is
// single-runner per migration (Temporal workflow id lock), so no
// two workers can race on the same item.
// Wave 11.1: signature gained tenantID so the Postgres GUC can be set
// and RLS on residency_migration_items fires. Callers updated in
// ResidencyMigrationWorkflow.
func (a *Activities) NextPendingMigrationDoc(ctx context.Context, tenantID, migrationID string, batch int) ([]string, error) {
	if batch <= 0 {
		batch = 50
	}
	var out []string
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT document_id::text
			  FROM residency_migration_items
			 WHERE migration_id = $1 AND status = 'pending'
			 ORDER BY updated_at
			 LIMIT $2`, migrationID, batch)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				return err
			}
			out = append(out, id)
		}
		return rows.Err()
	})
	return out, err
}

// MoveDocumentRegion updates `documents.region_pin` and flips the
// matching migration item. Emits `dms.residency.migrated.v1`.
// Idempotent: rerunning on an already-moved item is a no-op.
//
// Physical blob move is intentionally NOT performed here — the
// storage service owns cross-region copy + re-wrap, which isn't
// built yet. See Wave 12 / out-of-scope.
func (a *Activities) MoveDocumentRegion(ctx context.Context, tenantID, migrationID, documentID, targetRegion string) error {
	tenantUUID, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	docUUID, err := uuid.Parse(documentID)
	if err != nil {
		return fmt.Errorf("document_id: %w", err)
	}

	err = database.WithTenantTx(ctx, a.Pool, tenantUUID, func(tx pgx.Tx) error {
		var prev string
		err := tx.QueryRow(ctx, `
			SELECT region_pin FROM documents WHERE tenant_id = $1 AND id = $2`,
			tenantID, documentID,
		).Scan(&prev)
		if err != nil {
			return fmt.Errorf("fetch region: %w", err)
		}
		if prev == targetRegion {
			// Idempotent: flip the item row and skip the update + event.
			_, _ = tx.Exec(ctx, `
				UPDATE residency_migration_items
				   SET status = 'skipped', updated_at = now()
				 WHERE migration_id = $1 AND document_id = $2`,
				migrationID, documentID)
			return nil
		}

		if _, err := tx.Exec(ctx, `
			UPDATE documents
			   SET region_pin = $1, updated_at = now()
			 WHERE tenant_id = $2 AND id = $3`,
			targetRegion, tenantID, documentID,
		); err != nil {
			return fmt.Errorf("update region: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE residency_migration_items
			   SET status = 'moved', updated_at = now()
			 WHERE migration_id = $1 AND document_id = $2`,
			migrationID, documentID,
		); err != nil {
			return fmt.Errorf("update item: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE residency_migrations
			   SET moved_docs = moved_docs + 1
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, migrationID,
		); err != nil {
			return err
		}

		payload, _ := json.Marshal(map[string]any{
			"specversion": "1.0",
			"type":        "dms.residency.migrated.v1",
			"source":      "/vaultdms/workflow/residency",
			"data": map[string]string{
				"tenant_id":      tenantID,
				"document_id":    documentID,
				"migration_id":   migrationID,
				"source_region":  prev,
				"target_region":  targetRegion,
			},
		})
		evt := database.NewOutboxEvent(tenantUUID, "dms.residency.migrated.v1", "document", docUUID, payload)
		return a.Outbox.Insert(ctx, tx, evt)
	})
	return err
}

// FinalizeMigration stamps the migration row as completed / failed
// based on item counts. Safe to call multiple times.
func (a *Activities) FinalizeMigration(ctx context.Context, tenantID, migrationID string) (string, error) {
	var status string
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			UPDATE residency_migrations
			   SET failed_docs = (SELECT COUNT(*) FROM residency_migration_items WHERE migration_id = $1 AND status = 'failed'),
			       status = CASE
			           WHEN (SELECT COUNT(*) FROM residency_migration_items WHERE migration_id = $1 AND status = 'pending') > 0
			               THEN 'running'
			           WHEN (SELECT COUNT(*) FROM residency_migration_items WHERE migration_id = $1 AND status = 'failed') > 0
			               THEN 'failed'
			           ELSE 'completed'
			       END,
			       completed_at = CASE
			           WHEN (SELECT COUNT(*) FROM residency_migration_items WHERE migration_id = $1 AND status = 'pending') > 0
			               THEN NULL ELSE now()
			       END
			 WHERE tenant_id = $2 AND id = $1
			 RETURNING status`,
			migrationID, tenantID,
		).Scan(&status)
	})
	return status, err
}

// MarkItemFailed flips a specific item to failed with an error
// message. Called when MoveDocumentRegion raises during the worker
// loop. Wave 11.1: added tenantID for RLS scoping.
func (a *Activities) MarkItemFailed(ctx context.Context, tenantID, migrationID, documentID, errMsg string) error {
	return a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE residency_migration_items
			   SET status = 'failed', error_message = $1, updated_at = now()
			 WHERE migration_id = $2 AND document_id = $3`,
			errMsg, migrationID, documentID,
		)
		return err
	})
}

// ResidencyStats is the per-region summary powering the dashboard.
type ResidencyStats struct {
	Region     string `json:"region"`
	DocCount   int64  `json:"doc_count"`
	BlobBytes  int64  `json:"blob_bytes"`
}

// QueryResidencyStats returns per-region counts for the tenant.
// Bytes come from content_blobs (authoritative); doc count joins
// documents.region_pin so the two columns show policy vs physical
// residency side-by-side. Identical values → everything is where
// policy says it should be.
func (a *Activities) QueryResidencyStats(ctx context.Context, tenantID string) ([]ResidencyStats, error) {
	// Two-phase: count docs by region_pin, then left-join blob bytes.
	out := []ResidencyStats{}
	err := a.runTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			WITH doc_regions AS (
			    SELECT region_pin AS region, COUNT(*) AS doc_count
			      FROM documents
			     WHERE tenant_id = $1 AND deleted_at IS NULL
			     GROUP BY region_pin
			),
			blob_regions AS (
			    SELECT storage_region AS region, COALESCE(SUM(size_bytes), 0) AS bytes
			      FROM content_blobs
			     WHERE tenant_id = $1
			     GROUP BY storage_region
			)
			SELECT COALESCE(d.region, b.region) AS region,
			       COALESCE(d.doc_count, 0),
			       COALESCE(b.bytes, 0)
			  FROM doc_regions d
			  FULL OUTER JOIN blob_regions b ON d.region = b.region
			 ORDER BY region`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s ResidencyStats
			if err := rows.Scan(&s.Region, &s.DocCount, &s.BlobBytes); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	return out, err
}

// unused import guard
var _ = time.Now
