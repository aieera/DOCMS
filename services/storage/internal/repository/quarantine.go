package repository

import (
	"context"

	"github.com/jackc/pgx/v5"
)

type quarantineRepo struct{}

func (r *quarantineRepo) Record(ctx context.Context, tx pgx.Tx, e *QuarantineEvent) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO quarantine_events (
			id, tenant_id, upload_id, reason,
			signature, declared_mime, detected_mime,
			storage_bucket, storage_key, created_at
		)
		VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), $8, $9, $10)
	`,
		e.ID, e.TenantID, e.UploadID, e.Reason,
		e.Signature, e.DeclaredMIME, e.DetectedMIME,
		e.StorageBucket, e.StorageKey, e.CreatedAt,
	)
	return mapPgError(err)
}
