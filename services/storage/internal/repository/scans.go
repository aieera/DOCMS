package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/services/storage/internal/model"
)

type scanRepo struct{}

func (r *scanRepo) Record(ctx context.Context, tx pgx.Tx, rec *model.ScanRecord) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO scan_results (id, tenant_id, upload_id, result, signature, scanned_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6)
	`, rec.ID, rec.TenantID, rec.UploadID, string(rec.Result), rec.Signature, rec.ScannedAt)
	return mapPgError(err)
}

func (r *scanRepo) GetByUpload(ctx context.Context, tx pgx.Tx, tenantID, uploadID uuid.UUID) (*model.ScanRecord, error) {
	row := tx.QueryRow(ctx, `
		SELECT id, tenant_id, upload_id, result, COALESCE(signature, ''), scanned_at
		FROM scan_results
		WHERE tenant_id = $1 AND upload_id = $2
		ORDER BY scanned_at DESC
		LIMIT 1
	`, tenantID, uploadID)
	var (
		rec     model.ScanRecord
		result  string
		scanned time.Time
	)
	if err := row.Scan(&rec.ID, &rec.TenantID, &rec.UploadID, &result, &rec.Signature, &scanned); err != nil {
		return nil, mapPgError(err)
	}
	rec.Result = model.ScanResult(result)
	rec.ScannedAt = scanned
	return &rec, nil
}

// ListStuckPending reads rows from the partial index
// idx_scan_results_pending. The caller must provide a pool connected with
// a role that has BYPASSRLS — the reconciliation worker sweeps across all
// tenants in one pass, which is fundamentally incompatible with per-tx
// tenant scoping.
func (r *scanRepo) ListStuckPending(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]model.ScanRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := pool.Query(ctx, `
		SELECT id, tenant_id, upload_id, result, COALESCE(signature, ''), scanned_at
		FROM scan_results
		WHERE result = 'pending' AND scanned_at < $1
		ORDER BY scanned_at
		LIMIT $2
	`, cutoff, limit)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.ScanRecord
	for rows.Next() {
		var rec model.ScanRecord
		var result string
		if err := rows.Scan(&rec.ID, &rec.TenantID, &rec.UploadID, &result, &rec.Signature, &rec.ScannedAt); err != nil {
			return nil, mapPgError(err)
		}
		rec.Result = model.ScanResult(result)
		out = append(out, rec)
	}
	return out, rows.Err()
}
