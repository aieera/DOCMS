// ADR 0070 — repo for tsp_signing_sessions + qes_certificates.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// TSPSession mirrors one tsp_signing_sessions row.
type TSPSession struct {
	ID            string
	TenantID      string
	RequestID     string
	SignerID      string
	Provider      string
	Status        string
	DocumentHash  string
	ExternalID    string
	RedirectURL   string
	ReturnURL     string
	StateSecret   string
	FailureReason string
	CreatedAt     time.Time
	AuthorizedAt  *time.Time
	CompletedAt   *time.Time
	ExpiresAt     time.Time
}

// QESCertificate mirrors one qes_certificates row.
type QESCertificate struct {
	ID            string
	TenantID      string
	SignerID      string
	RequestID     string
	SessionID     string
	Provider      string
	SubjectDN     string
	IssuerDN      string
	SerialHex     string
	NotBefore     time.Time
	NotAfter      time.Time
	CertPEM       string
	ChainPEM      string
	LTVRevocation []byte
	CreatedAt     time.Time
}

// CreateTSPSessionTx inserts a session inside the caller's tx. Used by
// StartQES so the audit outbox event commits atomically with the row.
func (r *Repository) CreateTSPSessionTx(ctx context.Context, q Querier, s *TSPSession) error {
	_, err := q.Exec(ctx, `
		INSERT INTO tsp_signing_sessions (
			id, tenant_id, request_id, signer_id, provider, status,
			document_hash, external_id, redirect_url, return_url,
			state_secret, created_at, expires_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		s.ID, s.TenantID, s.RequestID, s.SignerID, s.Provider, s.Status,
		s.DocumentHash, s.ExternalID, s.RedirectURL, s.ReturnURL,
		s.StateSecret, s.CreatedAt, s.ExpiresAt)
	return err
}

// GetTSPSession returns a session row. Returns (nil, nil) when not found
// so callers can branch without errors.Is(pgx.ErrNoRows).
func (r *Repository) GetTSPSession(ctx context.Context, tenantID, id string) (*TSPSession, error) {
	var s *TSPSession
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		row, err := r.GetTSPSessionTx(ctx, tx, tenantID, id)
		s = row
		return err
	})
	if err != nil {
		return nil, err
	}
	return s, nil
}

// GetTSPSessionTx reads a session inside the caller's tenant tx. Returns
// (nil, nil) when not found.
func (r *Repository) GetTSPSessionTx(ctx context.Context, q RowQuerier, tenantID, id string) (*TSPSession, error) {
	s := &TSPSession{}
	err := q.QueryRow(ctx, `
		SELECT id, tenant_id, request_id, signer_id, provider, status,
			document_hash, COALESCE(external_id,''), redirect_url, return_url,
			state_secret, COALESCE(failure_reason,''), created_at,
			authorized_at, completed_at, expires_at
		FROM tsp_signing_sessions
		WHERE tenant_id = $1 AND id = $2`, tenantID, id).
		Scan(&s.ID, &s.TenantID, &s.RequestID, &s.SignerID, &s.Provider, &s.Status,
			&s.DocumentHash, &s.ExternalID, &s.RedirectURL, &s.ReturnURL,
			&s.StateSecret, &s.FailureReason, &s.CreatedAt,
			&s.AuthorizedAt, &s.CompletedAt, &s.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// MarkTSPSessionAuthorized records that the user has returned from the
// QTSP and we are about to call Sign(). Defensive against the user
// hitting the return URL twice (browser back, double-submit) — we
// only flip pending → authorized.
func (r *Repository) MarkTSPSessionAuthorized(ctx context.Context, q Querier, tenantID, id string) error {
	_, err := q.Exec(ctx, `
		UPDATE tsp_signing_sessions
		   SET status = 'authorized', authorized_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND status = 'pending'`,
		tenantID, id)
	return err
}

// MarkTSPSessionCompletedTx flips authorized → completed once the
// signed hash has been embedded.
func (r *Repository) MarkTSPSessionCompletedTx(ctx context.Context, q Querier, tenantID, id string) error {
	_, err := q.Exec(ctx, `
		UPDATE tsp_signing_sessions
		   SET status = 'completed', completed_at = now()
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id)
	return err
}

// MarkTSPSessionFailed records a failure reason. Always succeeds —
// even if the row isn't in pending/authorized any more — so the
// caller can use it as a final cleanup step.
func (r *Repository) MarkTSPSessionFailed(ctx context.Context, tenantID, id, reason string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE tsp_signing_sessions
			   SET status = 'failed', failure_reason = $3
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, id, reason)
		return err
	})
}

// ReapExpiredTSPSessions flips pending/authorized rows past expires_at
// to 'expired'. Called by the 5-min reaper loop.
// Enumerates tenants: tsp_signing_sessions is FORCE RLS, so a raw-pool
// UPDATE under the production role would match nothing.
func (r *Repository) ReapExpiredTSPSessions(ctx context.Context) (int64, error) {
	var total int64
	err := r.forEachTenant(ctx, func(tenantID uuid.UUID, tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE tsp_signing_sessions
			   SET status = 'expired'
			 WHERE tenant_id = $1
			   AND status IN ('pending','authorized')
			   AND expires_at < now()`, tenantID)
		if err != nil {
			return err
		}
		total += tag.RowsAffected()
		return nil
	})
	if err != nil {
		return 0, err
	}
	return total, nil
}

// CreateQESCertificateTx persists the cert + chain + LTV material.
func (r *Repository) CreateQESCertificateTx(ctx context.Context, q Querier, c *QESCertificate) error {
	_, err := q.Exec(ctx, `
		INSERT INTO qes_certificates (
			id, tenant_id, signer_id, request_id, session_id, provider,
			subject_dn, issuer_dn, serial_hex, not_before, not_after,
			cert_pem, chain_pem, ltv_revocation, created_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
		c.ID, c.TenantID, c.SignerID, c.RequestID, c.SessionID, c.Provider,
		c.SubjectDN, c.IssuerDN, c.SerialHex, c.NotBefore, c.NotAfter,
		c.CertPEM, c.ChainPEM, c.LTVRevocation, c.CreatedAt)
	return err
}

// GetQESCertificateByRequest returns the cert(s) for a signature request
// (one per signer). Used by /qes/cert UI.
func (r *Repository) GetQESCertificateByRequest(ctx context.Context, tenantID, requestID string) ([]*QESCertificate, error) {
	out := make([]*QESCertificate, 0)
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, signer_id, request_id, session_id, provider,
				subject_dn, issuer_dn, serial_hex, not_before, not_after,
				cert_pem, COALESCE(chain_pem,''), ltv_revocation, created_at
			FROM qes_certificates
			WHERE tenant_id = $1 AND request_id = $2
			ORDER BY created_at ASC`, tenantID, requestID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c := &QESCertificate{}
			if err := rows.Scan(&c.ID, &c.TenantID, &c.SignerID, &c.RequestID, &c.SessionID, &c.Provider,
				&c.SubjectDN, &c.IssuerDN, &c.SerialHex, &c.NotBefore, &c.NotAfter,
				&c.CertPEM, &c.ChainPEM, &c.LTVRevocation, &c.CreatedAt); err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}
