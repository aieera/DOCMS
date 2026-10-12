package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

// ContentBlobRepo manages the content_blobs table. Deduplication lives here
// in a follow-up; Phase B2 only covers insert + get-by-id for the
// post-complete path.
type ContentBlobRepo interface {
	Insert(ctx context.Context, tx pgx.Tx, b *model.ContentBlob) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.ContentBlob, error)
	// GetByHash finds the tenant's blob for a SHA-256. It matches the
	// UNIQUE (tenant_id, sha256_hash) constraint exactly — no region
	// filter — so a duplicate-key insert always resolves to the row that
	// caused it; callers compare StorageRegion themselves (L-6).
	GetByHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sha string) (*model.ContentBlob, error)
	IncrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	DecrementRefCount(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	ListZeroRefOlderThan(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]*model.ContentBlob, error)
	HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// Wave 12.2: update storage location + envelope fields after a
	// re-encrypt / WORM move. Sha256 is preserved (same plaintext); every
	// other storage / envelope field takes updated's value — but only if
	// the row still holds old's location and envelope (compare-and-set,
	// D-18). A row that changed since `old` was read returns Conflict.
	UpdateMigration(ctx context.Context, tx pgx.Tx, old, updated *model.ContentBlob) error
	// UpdateEnvelope re-wraps a blob's DEK in place: it writes ONLY
	// encrypted_dek + kek_id, and only while the row still holds old's
	// envelope and nonce (i.e. the same ciphertext). Conflict otherwise.
	UpdateEnvelope(ctx context.Context, tx pgx.Tx, old *model.ContentBlob, encryptedDEK []byte, kekID string) error
}

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

func (r *contentBlobRepo) GetByHash(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sha string) (*model.ContentBlob, error) {
	row := tx.QueryRow(ctx,
		selectContentBlobSQL+` WHERE tenant_id = $1 AND sha256_hash = $2`,
		tenantID, sha)
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

// ListZeroRefOlderThan returns up to `limit` blobs with reference_count=0
// created before cutoff (24h grace is the caller's responsibility — pass
// now.Add(-24*time.Hour)). Every content_blobs row carries a tenant_id
// (there are no ownerless blobs), so this is legitimately per-tenant
// work, not a cross-tenant scan: it enumerates tenants from the
// organizations registry and reads each tenant's zero-ref blobs under
// that tenant's app.current_tenant (Wave A.1, issue #76). The former
// `SET LOCAL row_security = off` ERRORED under the prod dms_app
// (NOBYPASSRLS) role — the reaper died every cycle. No bypass now;
// index idx_content_blobs_refcount_zero(tenant_id) WHERE reference_count=0
// supports the per-tenant predicate.
func (r *contentBlobRepo) ListZeroRefOlderThan(ctx context.Context, pool *pgxpool.Pool, cutoff time.Time, limit int) ([]*model.ContentBlob, error) {
	out := make([]*model.ContentBlob, 0, limit)
	err := database.ForEachTenant(ctx, pool, func(tenantID uuid.UUID, tx pgx.Tx) error {
		if len(out) >= limit {
			return nil // batch already full; remaining tenants are no-ops
		}
		rows, err := tx.Query(ctx,
			selectContentBlobSQL+` WHERE tenant_id = $1 AND reference_count = 0 AND created_at < $2
			                       ORDER BY created_at ASC LIMIT $3`,
			tenantID, cutoff, limit-len(out))
		if err != nil {
			return mapPgError(err)
		}
		defer rows.Close()
		for rows.Next() {
			b, err := scanContentBlob(rows)
			if err != nil {
				return err
			}
			out = append(out, b)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// UpdateMigration rotates the storage location + envelope fields
// (Wave 12.2) from old to updated. The WHERE clause is a compare-and-set
// on everything the caller read and is about to replace — region, bucket,
// key, nonce, wrapped DEK and KEK id — so a concurrent re-encrypt, WORM
// move or rewrap that committed first makes this a Conflict instead of
// being silently reverted (D-18). Reference count is deliberately NOT
// touched; re-encrypt preserves sharing.
func (r *contentBlobRepo) UpdateMigration(ctx context.Context, tx pgx.Tx, old, updated *model.ContentBlob) error {
	tag, err := tx.Exec(ctx, `
		UPDATE content_blobs
		   SET storage_region = $1,
		       storage_bucket = $2,
		       storage_key    = $3,
		       encrypted_dek  = $4,
		       dek_nonce      = $5,
		       kek_id         = NULLIF($6, '')
		 WHERE tenant_id = $7 AND id = $8
		   AND storage_region = $9
		   AND storage_bucket = $10
		   AND storage_key    = $11
		   AND dek_nonce      IS NOT DISTINCT FROM $12
		   AND encrypted_dek  IS NOT DISTINCT FROM $13
		   AND COALESCE(kek_id, '') = $14`,
		updated.StorageRegion, updated.StorageBucket, updated.StorageKey,
		nullableBytes(updated.EncryptedDEK), nullableBytes(updated.DEKNonce), updated.KEKID,
		old.TenantID, old.ID,
		old.StorageRegion, old.StorageBucket, old.StorageKey,
		nullableBytes(old.DEKNonce), nullableBytes(old.EncryptedDEK), old.KEKID,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return casMiss(ctx, tx, old.TenantID, old.ID)
	}
	return nil
}

// UpdateEnvelope swaps only the wrapped DEK + KEK id (rotation re-wrap),
// conditioned on the row still carrying the envelope and nonce the caller
// unwrapped — the ciphertext is unchanged, so the same DEK still opens it.
func (r *contentBlobRepo) UpdateEnvelope(ctx context.Context, tx pgx.Tx, old *model.ContentBlob, encryptedDEK []byte, kekID string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE content_blobs
		   SET encrypted_dek = $1,
		       kek_id        = NULLIF($2, '')
		 WHERE tenant_id = $3 AND id = $4
		   AND dek_nonce      IS NOT DISTINCT FROM $5
		   AND encrypted_dek  IS NOT DISTINCT FROM $6
		   AND COALESCE(kek_id, '') = $7`,
		nullableBytes(encryptedDEK), kekID,
		old.TenantID, old.ID,
		nullableBytes(old.DEKNonce), nullableBytes(old.EncryptedDEK), old.KEKID,
	)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return casMiss(ctx, tx, old.TenantID, old.ID)
	}
	return nil
}

// casMiss classifies a compare-and-set that matched no row: the blob is
// gone (NotFound) or it changed since it was read (Conflict).
func casMiss(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	var exists bool
	if err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM content_blobs WHERE tenant_id = $1 AND id = $2)`,
		tenantID, id).Scan(&exists); err != nil {
		return mapPgError(err)
	}
	if !exists {
		return vdmserr.ErrNotFound
	}
	return vdmserr.Conflict("content blob changed concurrently; re-read and retry")
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
		b       model.ContentBlob
		encDEK  []byte
		nonce   []byte
		created time.Time
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
