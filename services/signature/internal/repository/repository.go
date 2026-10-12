// Package repository persists signature requests and signer state.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/signature/internal/model"
)

// Querier is the common slice of pgx.Tx and *pgxpool.Pool the repository
// uses for writes. It lets callers participate in an outer transaction
// (outbox pattern) without forcing every code path through a tx.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// RowQuerier is the read counterpart of Querier — satisfied by both
// *pgxpool.Pool and pgx.Tx. It lets a caller read inside an outer tenant tx
// (which sets app.current_tenant) so RLS-forced tables return rows; a raw pool
// read from a non-HTTP context (e.g. a NATS consumer) has no tenant GUC and
// fails closed to 0 rows.
type RowQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ErrNotFound is returned by writes that matched no row for the tenant.
var ErrNotFound = errors.New("signature: not found")

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// withTenant runs fn inside database.WithTenantTx so app.current_tenant is
// set. Every signature table is FORCE ROW LEVEL SECURITY: under the
// production NOBYPASSRLS role a raw-pool read returns zero rows and a
// raw-pool write is rejected by the WITH CHECK policy (review finding M-15).
// The non-Tx methods below all go through here; the *Tx variants run inside
// a transaction the caller already opened with WithTenantTx.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant_id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// forEachTenant runs fn once per tenant inside that tenant's transaction.
// Used by the background sweeps (token refresh, envelope reconcile, QES
// reaper), which are legitimately cross-tenant but cannot scan a FORCE-RLS
// table without a tenant context.
func (r *Repository) forEachTenant(ctx context.Context, fn func(tenantID uuid.UUID, tx pgx.Tx) error) error {
	return database.ForEachTenant(ctx, r.pool, fn)
}

func (r *Repository) Create(ctx context.Context, req *model.SignatureRequest) error {
	return r.withTenant(ctx, req.TenantID, func(tx pgx.Tx) error {
		return r.CreateTx(ctx, tx, req)
	})
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
	var out *model.SignatureRequest
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		req, err := r.GetByIDTx(ctx, tx, tenantID, id)
		out = req
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SignerSigningToken returns the stored per-signer signing_url_token (the
// "/sign/{req}/{signer}?token=<tok>" string) used to authorize a sign action.
// Empty string (nil error) when the signer is absent for this tenant/request.
func (r *Repository) SignerSigningToken(ctx context.Context, tenantID, requestID, signerID string) (string, error) {
	var tok string
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT COALESCE(signing_url_token,'') FROM signature_signers
			  WHERE tenant_id = $1 AND request_id = $2 AND id = $3`,
			tenantID, requestID, signerID).Scan(&tok)
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil
		}
		return "", err
	}
	return tok, nil
}

// GetByIDTx reads a request + its signers via an explicit querier so callers
// outside an HTTP request (e.g. the seal consumer) can read inside a
// database.WithTenantTx (which sets app.current_tenant for RLS).
func (r *Repository) GetByIDTx(ctx context.Context, q RowQuerier, tenantID, id string) (*model.SignatureRequest, error) {
	req := &model.SignatureRequest{}
	var envelope, message, finalHash *string
	err := q.QueryRow(ctx, `
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
	signers, err := r.signersForTx(ctx, q, tenantID, req.ID)
	if err != nil {
		return nil, err
	}
	req.Signers = signers
	return req, nil
}

func (r *Repository) ListByDocument(ctx context.Context, tenantID, documentID string) ([]*model.SignatureRequest, error) {
	var out []*model.SignatureRequest
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		reqs, err := r.listByDocumentTx(ctx, tx, tenantID, documentID)
		out = reqs
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *Repository) listByDocumentTx(ctx context.Context, q RowQuerier, tenantID, documentID string) ([]*model.SignatureRequest, error) {
	rows, err := q.Query(ctx, `
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
		signers, err := r.signersForTx(ctx, q, tenantID, req.ID)
		if err != nil {
			return nil, err
		}
		req.Signers = signers
	}
	return out, nil
}

// ClaimSealTx atomically claims the seal for a request (sets sealed_at when it
// was NULL). Returns true when THIS caller won the claim; false means it was
// already claimed/sealed (idempotent skip). Guards against duplicate seals on
// NATS redelivery.
func (r *Repository) ClaimSealTx(ctx context.Context, q Querier, tenantID, id string) (bool, error) {
	ct, err := q.Exec(ctx,
		`UPDATE signature_requests SET sealed_at = now()
		  WHERE tenant_id = $1 AND id = $2 AND sealed_at IS NULL`, tenantID, id)
	if err != nil {
		return false, err
	}
	return ct.RowsAffected() > 0, nil
}

// ReleaseSealTx clears the claim so a failed seal can be retried on redelivery.
func (r *Repository) ReleaseSealTx(ctx context.Context, q Querier, tenantID, id string) error {
	_, err := q.Exec(ctx,
		`UPDATE signature_requests SET sealed_at = NULL WHERE tenant_id = $1 AND id = $2`, tenantID, id)
	return err
}

// signersForTx returns the signature_signers rows attached to one
// request, ordered by signing position.
func (r *Repository) signersForTx(ctx context.Context, q RowQuerier, tenantID, requestID string) ([]model.Signer, error) {
	rows, err := q.Query(ctx, `
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

// UpdateStatus sets the request status. ErrNotFound when no row matched —
// an unknown id, another tenant's id, or a row RLS hides — so a caller never
// reports a transition that did not happen.
func (r *Repository) UpdateStatus(ctx context.Context, tenantID, id, status string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		ct, err := tx.Exec(ctx,
			`UPDATE signature_requests SET status = $1 WHERE tenant_id = $2 AND id = $3`, status, tenantID, id)
		if err != nil {
			return err
		}
		if ct.RowsAffected() == 0 {
			return ErrNotFound
		}
		return nil
	})
}

func (r *Repository) Complete(ctx context.Context, tenantID, id string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return r.CompleteTx(ctx, tx, tenantID, id)
	})
}

func (r *Repository) CompleteTx(ctx context.Context, q Querier, tenantID, id string) error {
	now := time.Now().UTC()
	_, err := q.Exec(ctx,
		`UPDATE signature_requests SET status = 'completed', completed_at = $1 WHERE tenant_id = $2 AND id = $3`,
		now, tenantID, id)
	return err
}

func (r *Repository) UpdateSigners(ctx context.Context, tenantID, id string, signers []model.Signer) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return r.UpdateSignersTx(ctx, tx, tenantID, id, signers)
	})
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
