// Package repository persists signature requests and signer state.
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/services/signature/internal/model"
)

// Querier is the common slice of pgx.Tx and *pgxpool.Pool the repository
// uses for writes. It lets callers participate in an outer transaction
// (outbox pattern) without forcing every code path through a tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

func (r *Repository) Create(ctx context.Context, req *model.SignatureRequest) error {
	return r.CreateTx(ctx, r.pool, req)
}

// CreateTx inserts the request using the supplied executor (pool or tx).
// Callers participating in the transactional outbox pattern pass their
// pgx.Tx so the business write and the outbox row commit atomically.
func (r *Repository) CreateTx(ctx context.Context, q Querier, req *model.SignatureRequest) error {
	signersJSON, _ := json.Marshal(req.Signers)
	_, err := q.Exec(ctx, `
		INSERT INTO signature_requests (id, tenant_id, document_id, version_id, created_by, status, provider, external_id, signers, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, req.ID, req.TenantID, req.DocumentID, req.VersionID, req.CreatedBy,
		req.Status, req.Provider, req.ExternalID, signersJSON, req.CreatedAt, req.ExpiresAt)
	return err
}

func (r *Repository) GetByID(ctx context.Context, tenantID, id string) (*model.SignatureRequest, error) {
	req := &model.SignatureRequest{}
	var signersJSON []byte
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, version_id, created_by, status, provider, external_id, signers, created_at, completed_at, expires_at
		FROM signature_requests WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&req.ID, &req.TenantID, &req.DocumentID, &req.VersionID, &req.CreatedBy,
			&req.Status, &req.Provider, &req.ExternalID, &signersJSON, &req.CreatedAt, &req.CompletedAt, &req.ExpiresAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	_ = json.Unmarshal(signersJSON, &req.Signers)
	return req, err
}

func (r *Repository) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*model.SignatureRequest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, document_id, version_id, created_by, status, provider, external_id, signers, created_at, completed_at, expires_at
		FROM signature_requests WHERE tenant_id = $1 AND document_id = $2 ORDER BY created_at DESC`, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.SignatureRequest
	for rows.Next() {
		req := &model.SignatureRequest{}
		var signersJSON []byte
		if err := rows.Scan(&req.ID, &req.TenantID, &req.DocumentID, &req.VersionID, &req.CreatedBy,
			&req.Status, &req.Provider, &req.ExternalID, &signersJSON, &req.CreatedAt, &req.CompletedAt, &req.ExpiresAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(signersJSON, &req.Signers)
		out = append(out, req)
	}
	return out, rows.Err()
}

func (r *Repository) UpdateStatus(ctx context.Context, tenantID, id, status string) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE signature_requests SET status = $1 WHERE tenant_id = $2 AND id = $3`, status, tenantID, id)
	return err
}

func (r *Repository) Complete(ctx context.Context, tenantID, id string) error {
	return r.CompleteTx(ctx, r.pool, tenantID, id)
}

func (r *Repository) CompleteTx(ctx context.Context, q Querier, tenantID, id string) error {
	now := time.Now().UTC()
	_, err := q.Exec(ctx,
		`UPDATE signature_requests SET status = 'completed', completed_at = $1 WHERE tenant_id = $2 AND id = $3`,
		now, tenantID, id)
	return err
}

func (r *Repository) UpdateSigners(ctx context.Context, tenantID, id string, signers []model.Signer) error {
	return r.UpdateSignersTx(ctx, r.pool, tenantID, id, signers)
}

func (r *Repository) UpdateSignersTx(ctx context.Context, q Querier, tenantID, id string, signers []model.Signer) error {
	signersJSON, _ := json.Marshal(signers)
	_, err := q.Exec(ctx,
		`UPDATE signature_requests SET signers = $1 WHERE tenant_id = $2 AND id = $3`,
		signersJSON, tenantID, id)
	return err
}

func NewID() string {
	id, _ := uuid.NewV7()
	return id.String()
}
