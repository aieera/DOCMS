package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/storage/internal/model"
)

// ContentBlobRepo manages the content_blobs table. Deduplication lives here
// in a follow-up; Phase B2 only covers insert + get-by-id for the
// post-complete path.
type ContentBlobRepo interface {
	Insert(ctx context.Context, tx pgx.Tx, b *model.ContentBlob) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.ContentBlob, error)
	GetByHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, region, sha string) (*model.ContentBlob, error)
	IncrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	DecrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	ListZeroRefOlderThan(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]*model.ContentBlob, error)
	HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// Wave 12.2: update storage location + envelope fields after a
	// cross-region re-encrypt. Sha256 is preserved (same plaintext);
	// every other storage / envelope field rotates.
	UpdateMigration(ctx context.Context, tx pgx.Tx, b *model.ContentBlob) error
	// Shred (ADR 0036): null encrypted_dek + dek_nonce and stamp
	// shredded_at on a single blob row. Returns one of three outcomes
	// the caller maps onto the gRPC response (shredded / already / not_found).
	Shred(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (ShredOutcome, error)

	// TransitionStorageClass updates the persisted storage_class on the
	// blob row to reflect a tier change applied at the S3 layer. Returns
	// the per-blob outcome the caller maps onto the gRPC response.
	TransitionStorageClass(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, targetClass string) (TransitionOutcome, error)
}

// TransitionOutcome is the per-blob result from TransitionStorageClass.
type TransitionOutcome int

const (
	TransitionOutcomeMoved        TransitionOutcome = iota // class changed by this call
	TransitionOutcomeAlreadyInTarget                       // already in target class on entry
	TransitionOutcomeNotFound                              // no such blob for this tenant
)

// ShredOutcome is the per-blob result from Shred. Distinct from a Go
// error because "blob already shredded" and "blob not found for tenant"
// are not failures — the caller reports them in the gRPC response so
// the executor knows which slots to update vs skip.
type ShredOutcome int

const (
	ShredOutcomeShredded        ShredOutcome = iota // newly shredded by this call
	ShredOutcomeAlreadyShredded                     // shredded_at IS NOT NULL on entry
	ShredOutcomeNotFound                            // no such blob for this tenant
)

type contentBlobRepo struct{}

func (r *contentBlobRepo) Insert(ctx context.Context, tx pgx.Tx, b *model.ContentBlob) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO content_blobs (
			id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
			storage_class, size_bytes, mime_type, encryption_key_id,
			encrypted_dek, dek_nonce, kek_id,
			reference_count, created_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, NULLIF($10, ''),
			$11, $12, NULLIF($13, ''),
			$14, $15
		)
	`,
		b.ID, b.TenantID, b.SHA256Hash, b.StorageRegion, b.StorageBucket, b.StorageKey,
		b.StorageClass, b.SizeBytes, b.MimeType, b.EncryptionKeyID,
		nullableBytes(b.EncryptedDEK), nullableBytes(b.DEKNonce), b.KEKID,
		b.ReferenceCount, b.CreatedAt,
	)
	return mapPgError(err)
}

func (r *contentBlobRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.ContentBlob, error) {
	row := tx.QueryRow(ctx, selectContentBlobSQL+` WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return scanContentBlob(row)
}

func (r *contentBlobRepo) GetByHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, region, sha string) (*model.ContentBlob, error) {
	row := tx.QueryRow(ctx,
		selectContentBlobSQL+` WHERE tenant_id = $1 AND storage_region = $2 AND sha256_hash = $3`,
		tenantID, region, sha)
	return scanContentBlob(row)
}

func (r *contentBlobRepo) IncrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`UPDATE content_blobs SET reference_count = reference_count + 1 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *contentBlobRepo) DecrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`UPDATE content_blobs SET reference_count = GREATEST(reference_count - 1, 0)
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ListZeroRefOlderThan returns blobs with reference_count=0 created before
// cutoff, across all tenants. The 24h grace window is the caller's
// responsibility — pass now.Add(-24*time.Hour). Runs `SET LOCAL
// row_security = off` so the reaper sees rows regardless of RLS tenant
// scope; requires the DB role to have BYPASSRLS (or the NOBYPASSRLS role
// to own the table's policies such that row_security=off takes effect).
func (r *contentBlobRepo) ListZeroRefOlderThan(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]*model.ContentBlob, error) {
	var out []*model.ContentBlob
	err := pgx.BeginTxFunc(ctx, pool, pgx.TxOptions{AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SET LOCAL row_security = off"); err != nil {
			return err
		}
		rows, err := tx.Query(ctx,
			selectContentBlobSQL+` WHERE reference_count = 0 AND created_at < $1 ORDER BY created_at ASC LIMIT $2`,
			cutoff, limit)
		if err != nil {
			return mapPgError(err)
		}
		defer rows.Close()
		out = make([]*model.ContentBlob, 0, limit)
		for rows.Next() {
			b, err := scanContentBlob(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	return out, err
}

// UpdateMigration rotates the storage location + envelope fields
// (Wave 12.2). Caller must provide the blob row with the NEW values
// already set — sha256 stays, everything else rotates atomically.
// Reference count is deliberately NOT touched; re-encrypt preserves
// sharing.
func (r *contentBlobRepo) UpdateMigration(ctx context.Context, tx pgx.Tx, b *model.ContentBlob) error {
	tag, err := tx.Exec(ctx, `
		UPDATE content_blobs
		   SET storage_region = $1,
		       storage_bucket = $2,
		       storage_key    = $3,
		       encrypted_dek  = $4,
		       dek_nonce      = $5,
		       kek_id         = NULLIF($6, '')
		 WHERE tenant_id = $7 AND id = $8`,
		b.StorageRegion, b.StorageBucket, b.StorageKey,
		nullableBytes(b.EncryptedDEK), nullableBytes(b.DEKNonce), b.KEKID,
		b.TenantID, b.ID,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Shred zeros the wrapped DEK + nonce and stamps shredded_at. Idempotent
// against repeat calls — the WHERE filters on shredded_at IS NULL so a
// second call returns AlreadyShredded without touching the row. The
// CHECK constraint content_blobs_shred_consistency makes the (NULL DEK
// ∧ shredded_at NOT NULL) invariant DB-enforced.
func (r *contentBlobRepo) Shred(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (ShredOutcome, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE content_blobs
		   SET encrypted_dek = NULL,
		       dek_nonce     = NULL,
		       shredded_at   = now()
		 WHERE tenant_id = $1
		   AND id        = $2
		   AND shredded_at IS NULL`,
		tenantID, id)
	if err != nil {
		return ShredOutcomeNotFound, mapPgError(err)
	}
	if tag.RowsAffected() == 1 {
		return ShredOutcomeShredded, nil
	}
	// Zero rows affected: either the blob doesn't exist for this tenant,
	// or it's already shredded. Distinguish so the caller can report
	// honestly.
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM content_blobs WHERE tenant_id = $1 AND id = $2)`,
		tenantID, id,
	).Scan(&exists); err != nil {
		return ShredOutcomeNotFound, mapPgError(err)
	}
	if exists {
		return ShredOutcomeAlreadyShredded, nil
	}
	return ShredOutcomeNotFound, nil
}

// TransitionStorageClass updates the storage_class column under
// WHERE storage_class != target so the second call is naturally
// idempotent (returns AlreadyInTarget without writing).
func (r *contentBlobRepo) TransitionStorageClass(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, targetClass string) (TransitionOutcome, error) {
	tag, err := tx.Exec(ctx, `
		UPDATE content_blobs
		   SET storage_class = $1
		 WHERE tenant_id = $2 AND id = $3 AND storage_class <> $1
	`, targetClass, tenantID, id)
	if err != nil {
		return TransitionOutcomeNotFound, mapPgError(err)
	}
	if tag.RowsAffected() == 1 {
		return TransitionOutcomeMoved, nil
	}
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM content_blobs WHERE tenant_id = $1 AND id = $2)`,
		tenantID, id,
	).Scan(&exists); err != nil {
		return TransitionOutcomeNotFound, mapPgError(err)
	}
	if exists {
		return TransitionOutcomeAlreadyInTarget, nil
	}
	return TransitionOutcomeNotFound, nil
}

func (r *contentBlobRepo) HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx,
		`DELETE FROM content_blobs WHERE tenant_id = $1 AND id = $2 AND reference_count = 0`,
		tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

const selectContentBlobSQL = `
	SELECT id, tenant_id, sha256_hash, storage_region, storage_bucket, storage_key,
	       storage_class, size_bytes, COALESCE(mime_type, ''),
	       COALESCE(encryption_key_id, ''),
	       encrypted_dek, dek_nonce, COALESCE(kek_id, ''),
	       reference_count, created_at
	FROM content_blobs`

type rowScanner interface{ Scan(...any) error }

func scanContentBlob(r rowScanner) (*model.ContentBlob, error) {
	var (
		b        model.ContentBlob
		encDEK   []byte
		nonce    []byte
		created  time.Time
	)
	if err := r.Scan(
		&b.ID, &b.TenantID, &b.SHA256Hash, &b.StorageRegion, &b.StorageBucket, &b.StorageKey,
		&b.StorageClass, &b.SizeBytes, &b.MimeType,
		&b.EncryptionKeyID,
		&encDEK, &nonce, &b.KEKID,
		&b.ReferenceCount, &created,
	); err != nil {
		if mapped := mapPgError(err); vdmserr.KindOf(mapped) == vdmserr.KindNotFound {
			return nil, mapped
		}
		return nil, mapPgError(err)
	}
	b.EncryptedDEK = encDEK
	b.DEKNonce = nonce
	b.CreatedAt = created
	return &b, nil
}

func nullableBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}
