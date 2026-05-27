package repository

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// UploadPolicy is the per-tenant format allowlist mirrored from
// tenant_upload_policies (migration 000060). Empty slices mean
// "no allowlist enforced" — only the executable blocklist applies.
type UploadPolicy struct {
	AllowedMimeTypes  []string
	AllowedExtensions []string
}

// UploadPolicyRepo reads tenant_upload_policies. Write side lives in
// the document service's admin handler — storage only enforces.
type UploadPolicyRepo interface {
	Get(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*UploadPolicy, error)
}

type uploadPolicyRepo struct{}

// NewUploadPolicyRepo wires the read-only repo for InitiateUpload's
// allowlist check. Missing row -> empty policy (graceful default).
func NewUploadPolicyRepo() UploadPolicyRepo { return &uploadPolicyRepo{} }

func (r *uploadPolicyRepo) Get(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) (*UploadPolicy, error) {
	var mimeRaw, extRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT allowed_mime_types, allowed_extensions
		FROM tenant_upload_policies
		WHERE tenant_id = $1
	`, tenantID).Scan(&mimeRaw, &extRaw)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return &UploadPolicy{}, nil
		}
		return nil, mapPgError(err)
	}
	p := &UploadPolicy{}
	if len(mimeRaw) > 0 {
		_ = json.Unmarshal(mimeRaw, &p.AllowedMimeTypes)
	}
	if len(extRaw) > 0 {
		_ = json.Unmarshal(extRaw, &p.AllowedExtensions)
	}
	return p, nil
}
