// ADR 0070 — repo for tsp_signing_sessions + qes_certificates.
package repository

import (
	"context"
	"errors"
	"time"

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
	s := &TSPSession{}
	err := r.pool.QueryRow(ctx, `
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
	return s, err
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
	_, err := r.pool.Exec(ctx, `
		UPDATE tsp_signing_sessions
		   SET status = 'failed', failure_reason = $3
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, id, reason)
	return err
}

// ReapExpiredTSPSessions flips pending/authorized rows past expires_at
// to 'expired'. Called by the 5-min reaper loop.
func (r *Repository) ReapExpiredTSPSessions(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE tsp_signing_sessions
		   SET status = 'expired'
		 WHERE status IN ('pending','authorized')
		   AND expires_at < now()`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
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
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, signer_id, request_id, session_id, provider,
			subject_dn, issuer_dn, serial_hex, not_before, not_after,
			cert_pem, COALESCE(chain_pem,''), ltv_revocation, created_at
		FROM qes_certificates
		WHERE tenant_id = $1 AND request_id = $2
		ORDER BY created_at ASC`, tenantID, requestID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*QESCertificate, 0)
	for rows.Next() {
		c := &QESCertificate{}
		if err := rows.Scan(&c.ID, &c.TenantID, &c.SignerID, &c.RequestID, &c.SessionID, &c.Provider,
			&c.SubjectDN, &c.IssuerDN, &c.SerialHex, &c.NotBefore, &c.NotAfter,
			&c.CertPEM, &c.ChainPEM, &c.LTVRevocation, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
