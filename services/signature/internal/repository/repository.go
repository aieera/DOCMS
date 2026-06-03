// Package repository persists signature requests and signer state.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/services/signature/internal/model"
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

// CreateTx inserts the request + every non-empty signer in the
// caller's tx. The schema (000001_initial_schema.up.sql) splits
// these across two tables — signature_requests + signature_signers
// — so we write both. The model layer keeps the signers embedded
// for the API surface; this repo is the materialization boundary.
func (r *Repository) CreateTx(ctx context.Context, q Querier, req *model.SignatureRequest) error {
	mode := req.SigningMode
	if mode == "" {
		mode = "remote"
	}
	if _, err := q.Exec(ctx, `
		INSERT INTO signature_requests (
			id, tenant_id, document_id, version_id, requested_by,
			status, provider, provider_envelope_id, message,
			created_at, expires_at, signing_mode)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULL,$9,$10,$11)`,
		req.ID, req.TenantID, req.DocumentID, req.VersionID, req.CreatedBy,
		req.Status, req.Provider, req.ExternalID,
		req.CreatedAt, req.ExpiresAt, mode); err != nil {
		return err
	}
	for i, s := range req.Signers {
		if s.ID == "" {
			s.ID = NewID()
			req.Signers[i].ID = s.ID
		}
		token := s.SigningURL // already includes ?token=...; pull it
		// Token strictly required (UNIQUE NOT NULL) — fall back to
		// the signer id when nothing else was set.
		if token == "" {
			token = s.ID
		}
		if _, err := q.Exec(ctx, `
			INSERT INTO signature_signers (
				id, tenant_id, request_id, signer_email, signer_name,
				role, order_index, status, signing_url_token, ip_address)
			VALUES ($1,$2,$3,$4,NULLIF($5,''),$6,$7,$8,$9,NULLIF($10,'')::inet)`,
			s.ID, req.TenantID, req.ID, s.Email, s.Name,
			defaultRole(s.Role), defaultOrder(s.Order, i+1), defaultStatus(s.Status),
			token, s.IPAddress); err != nil {
			return err
		}
	}
	return nil
}

func (r *Repository) GetByID(ctx context.Context, tenantID, id string) (*model.SignatureRequest, error) {
	req := &model.SignatureRequest{}
	var envelope, message, finalHash *string
	err := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, document_id, version_id, COALESCE(requested_by::text,''),
		       status, provider, provider_envelope_id, message,
		       created_at, completed_at, expires_at,
		       COALESCE(signing_mode,'remote'), final_hash_sha256
		FROM signature_requests WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&req.ID, &req.TenantID, &req.DocumentID, &req.VersionID, &req.CreatedBy,
			&req.Status, &req.Provider, &envelope, &message,
			&req.CreatedAt, &req.CompletedAt, &req.ExpiresAt,
			&req.SigningMode, &finalHash)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if envelope != nil {
		req.ExternalID = *envelope
	}
	if finalHash != nil {
		req.FinalHashSHA256 = *finalHash
	}
	signers, err := r.signersFor(ctx, tenantID, req.ID)
	if err != nil {
		return nil, err
	}
	req.Signers = signers
	return req, nil
}

func (r *Repository) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*model.SignatureRequest, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, document_id, version_id, COALESCE(requested_by::text,''),
		       status, provider, provider_envelope_id,
		       created_at, completed_at, expires_at,
		       COALESCE(signing_mode,'remote'), final_hash_sha256
		FROM signature_requests WHERE tenant_id = $1 AND document_id = $2 ORDER BY created_at DESC`, tenantID, documentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*model.SignatureRequest, 0)
	for rows.Next() {
		req := &model.SignatureRequest{}
		var envelope, finalHash *string
		if err := rows.Scan(&req.ID, &req.TenantID, &req.DocumentID, &req.VersionID, &req.CreatedBy,
			&req.Status, &req.Provider, &envelope,
			&req.CreatedAt, &req.CompletedAt, &req.ExpiresAt,
			&req.SigningMode, &finalHash); err != nil {
			return nil, err
		}
		if envelope != nil {
			req.ExternalID = *envelope
		}
		if finalHash != nil {
			req.FinalHashSHA256 = *finalHash
		}
		out = append(out, req)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Hydrate signers in a second pass to keep the main query
	// JOIN-free; usually 0–3 signature requests per doc.
	for _, req := range out {
		signers, err := r.signersFor(ctx, tenantID, req.ID)
		if err != nil {
			return nil, err
		}
		req.Signers = signers
	}
	return out, nil
}

// signersFor returns the signature_signers rows attached to one
// request, ordered by signing position.
func (r *Repository) signersFor(ctx context.Context, tenantID, requestID string) ([]model.Signer, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, COALESCE(signer_email,''), COALESCE(signer_name,''),
		       COALESCE(role,'signer'), COALESCE(order_index,1),
		       COALESCE(status,'pending'),
		       COALESCE(signing_url_token,''),
		       signed_at, COALESCE(ip_address::text,''),
		       signature_svg_path, signed_doc_hash_sha256, device_kind
		FROM signature_signers
		WHERE tenant_id = $1 AND request_id = $2
		ORDER BY order_index ASC`, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Signer, 0)
	for rows.Next() {
		var s model.Signer
		var svg, hash, dev *string
		if err := rows.Scan(&s.ID, &s.Email, &s.Name, &s.Role, &s.Order,
			&s.Status, &s.SigningURL, &s.SignedAt, &s.IPAddress,
			&svg, &hash, &dev); err != nil {
			return nil, err
		}
		if svg != nil {
			s.SignatureSVGPath = *svg
		}
		if hash != nil {
			s.SignedDocHashSHA256 = *hash
		}
		if dev != nil {
			s.DeviceKind = *dev
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func defaultRole(s string) string {
	if s == "" {
		return "signer"
	}
	return s
}
func defaultStatus(s string) string {
	if s == "" {
		return "pending"
	}
	return s
}
func defaultOrder(n, fallback int) int {
	if n <= 0 {
		return fallback
	}
	return n
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

// UpdateSignersTx updates the per-signer rows in signature_signers.
// Status / signed_at / ip_address / svg + hash / device are the
// fields that change after the request is in flight; everything else
// (email/name/role/order) stays as written at create-time.
func (r *Repository) UpdateSignersTx(ctx context.Context, q Querier, tenantID, id string, signers []model.Signer) error {
	for _, s := range signers {
		if s.ID == "" {
			continue
		}
		if _, err := q.Exec(ctx, `
			UPDATE signature_signers
			   SET status = $4,
			       signed_at = $5,
			       ip_address = NULLIF($6,'')::inet,
			       signature_svg_path = COALESCE(NULLIF($7,''), signature_svg_path),
			       signed_doc_hash_sha256 = COALESCE(NULLIF($8,''), signed_doc_hash_sha256),
			       device_kind = COALESCE(NULLIF($9,''), device_kind)
			 WHERE tenant_id = $1 AND request_id = $2 AND id = $3`,
			tenantID, id, s.ID, defaultStatus(s.Status), s.SignedAt, s.IPAddress,
			s.SignatureSVGPath, s.SignedDocHashSHA256, s.DeviceKind); err != nil {
			return err
		}
	}
	return nil
}

// SetFinalHashTx records the document-bytes hash captured when the
// last signer commits. Verify re-hashes and compares.
func (r *Repository) SetFinalHashTx(ctx context.Context, q Querier, tenantID, id, hashHex string) error {
	if hashHex == "" {
		return nil
	}
	_, err := q.Exec(ctx,
		`UPDATE signature_requests SET final_hash_sha256 = $1 WHERE tenant_id = $2 AND id = $3`,
		hashHex, tenantID, id)
	return err
}

func NewID() string {
	id, _ := uuid.NewV7()
	return id.String()
}
