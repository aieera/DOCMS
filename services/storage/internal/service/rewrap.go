package service

import (
	"bytes"
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/storage/internal/model"
)

// liveKEKAlias returns the tenant's currently-live KEK alias from
// tenant_keks (the non-retired row), falling back to the base
// aliasForTenant form when the tenant has no tenant_keks row.
//
// This is the single source of truth for "which KEK version do new
// encrypts and re-wraps target". Before this, the encrypt path always
// used the base alias and ignored `kms rotate`'s versioned rows; routing
// both encrypt and rewrap through here makes a rotation actually take
// effect. The fallback keeps every tenant that never ran `kms create`
// (i.e. all of dev today) on the exact same alias as before — a no-op.
func (s *Service) liveKEKAlias(ctx context.Context, tenantID uuid.UUID) string {
	base := aliasForTenant(tenantID)
	if s.cfg.Pool == nil {
		return base
	}
	var alias string
	err := database.WithTenantTx(ctx, s.cfg.Pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT alias FROM tenant_keks
			  WHERE tenant_id = $1 AND retired_at IS NULL
			  ORDER BY version DESC LIMIT 1`,
			tenantID).Scan(&alias)
	})
	if err != nil || alias == "" {
		return base
	}
	return alias
}

// RewrapDEKInput identifies one blob to re-wrap.
type RewrapDEKInput struct {
	TenantID uuid.UUID
	BlobID   uuid.UUID
}

// RewrapDEKResult summarizes the outcome.
type RewrapDEKResult struct {
	BlobID      uuid.UUID `json:"blob_id"`
	OldKEKAlias string    `json:"old_kek_alias"`
	NewKEKAlias string    `json:"new_kek_alias"`
	Changed     bool      `json:"changed"` // false = already on the live alias (no-op)
}

// RewrapDEK re-wraps a blob's DEK under the tenant's currently-live KEK
// alias WITHOUT moving or re-encrypting the blob bytes (rotation re-wrap).
// Only content_blobs.encrypted_dek + kek_id change; the ciphertext and its
// dek_nonce are untouched, so the blob keeps decrypting under the same DEK.
//
// Safety: the DEK is unwrapped under the old kek_id, re-wrapped under the
// target, and the result is VERIFIED to decrypt back to the identical DEK
// before any DB write. A backend wrap bug therefore fails closed (the row
// is left exactly as it was) rather than producing an undecryptable blob.
//
// No-op (Changed=false) when the blob is already on the live alias, when
// the blob isn't envelope-encrypted, or when the blob carries a regional
// alias (those are owned by ReencryptBlob / `kms rewrap-regional`, which
// move bytes between region buckets — re-wrapping them here would strip
// the region scoping).
func (s *Service) RewrapDEK(ctx context.Context, in RewrapDEKInput) (*RewrapDEKResult, error) {
	if s.cfg.KMS == nil {
		return nil, vdmserr.Internal("envelope encryption: KeyManager not configured")
	}

	var blob *model.ContentBlob
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		b, err := s.cfg.Repos.ContentBlobs.GetByID(ctx, tx, in.TenantID, in.BlobID)
		if err != nil {
			return err
		}
		blob = b
		return nil
	}); err != nil {
		return nil, fmt.Errorf("load blob: %w", err)
	}
	if blob == nil {
		return nil, vdmserr.NotFound("content blob not found")
	}

	res := &RewrapDEKResult{BlobID: blob.ID, OldKEKAlias: blob.KEKID}

	// Not envelope-encrypted → nothing to re-wrap.
	if len(blob.EncryptedDEK) == 0 || blob.KEKID == "" {
		res.NewKEKAlias = blob.KEKID
		return res, nil
	}
	// Regional aliases are owned by the region-migration path; leave them.
	if isRegionalAlias(blob.KEKID, in.TenantID) {
		res.NewKEKAlias = blob.KEKID
		return res, nil
	}

	target := s.liveKEKAlias(ctx, in.TenantID)
	res.NewKEKAlias = target
	if target == blob.KEKID {
		return res, nil // already live — no-op
	}

	// Unwrap under the current KEK, re-wrap under the live KEK.
	plainDEK, err := s.cfg.KMS.DecryptDataKey(ctx, blob.KEKID, blob.EncryptedDEK)
	if err != nil {
		return nil, fmt.Errorf("unwrap dek under %s: %w", blob.KEKID, err)
	}
	defer zero(plainDEK)

	newEnc, err := s.cfg.KMS.EncryptDataKey(ctx, target, plainDEK)
	if err != nil {
		return nil, fmt.Errorf("re-wrap dek under %s: %w", target, err)
	}

	// VERIFY before persisting: the new wrapping must decrypt back to the
	// exact same DEK. If it doesn't, abort without touching the DB.
	check, err := s.cfg.KMS.DecryptDataKey(ctx, target, newEnc)
	if err != nil {
		return nil, fmt.Errorf("re-wrap verify (unwrap under %s) failed: %w", target, err)
	}
	defer zero(check)
	if !bytes.Equal(check, plainDEK) {
		return nil, vdmserr.Internal("re-wrap verification failed: new wrapping does not round-trip to the original DEK; DB left unchanged")
	}

	// Persist only the envelope change. region/bucket/key/dek_nonce stay
	// as-is, so the S3 object is never read or rewritten.
	updated := *blob
	updated.EncryptedDEK = newEnc
	updated.KEKID = target
	if err := database.WithTenantTx(ctx, s.cfg.Pool, in.TenantID, func(tx pgx.Tx) error {
		return s.cfg.Repos.ContentBlobs.UpdateMigration(ctx, tx, &updated)
	}); err != nil {
		return nil, fmt.Errorf("update blob envelope: %w", err)
	}
	res.Changed = true
	return res, nil
}

func zero(b []byte) {
	for i := range b {
		b[i] = 0
	}
}

// isRegionalAlias reports whether kekID carries a region segment — i.e. a
// slash after the tenant uuid: "vaultdms/tenant/<uuid>/<region>(@v<N>)?".
// The base/versioned forms ("vaultdms/tenant/<uuid>" / "...@v2") do not.
func isRegionalAlias(kekID string, tenantID uuid.UUID) bool {
	prefix := aliasForTenant(tenantID) // "vaultdms/tenant/<uuid>"
	if len(kekID) <= len(prefix) {
		return false
	}
	// A regional alias continues with "/<region>"; a versioned base alias
	// continues with "@v<N>". Only the slash means regional.
	return kekID[len(prefix)] == '/'
}
