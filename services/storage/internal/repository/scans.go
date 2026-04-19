package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
