// ADR 0071 — repo for esign_oauth_tokens + esign_envelope_events.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ESignToken mirrors esign_oauth_tokens. AccessToken / RefreshToken
// are STORED SEALED — caller passes ciphertext in, gets ciphertext
// out. The service layer wraps Seal/Unseal at the boundary.
type ESignToken struct {
	TenantID     string
	Provider     string
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
	AccountID    string
	BaseURI      string
	Scope        string
	ConnectedBy  string
	ConnectedAt  time.Time
	UpdatedAt    time.Time
}

// UpsertESignToken creates or replaces the per-tenant token row.
// Idempotent: re-connecting with a new account overwrites the
// previous row.
func (r *Repository) UpsertESignToken(ctx context.Context, t *ESignToken) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO esign_oauth_tokens (
			tenant_id, provider, access_token, refresh_token,
			expires_at, account_id, base_uri, scope, connected_by, connected_at, updated_at)
		VALUES ($1,$2,$3,NULLIF($4,''),$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),$9,$10,$11)
		ON CONFLICT (tenant_id, provider) DO UPDATE SET
			access_token = EXCLUDED.access_token,
			refresh_token = EXCLUDED.refresh_token,
			expires_at = EXCLUDED.expires_at,
			account_id = EXCLUDED.account_id,
			base_uri = EXCLUDED.base_uri,
			scope = EXCLUDED.scope,
			updated_at = now()`,
		t.TenantID, t.Provider, t.AccessToken, t.RefreshToken,
		t.ExpiresAt, t.AccountID, t.BaseURI, t.Scope,
		nullableUUID(t.ConnectedBy), t.ConnectedAt, t.UpdatedAt)
	return err
}

// GetESignToken returns the row or (nil, nil) if not connected.
func (r *Repository) GetESignToken(ctx context.Context, tenantID, provider string) (*ESignToken, error) {
	t := &ESignToken{}
	var account, base, scope, connectedBy *string
	var refresh *string
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, provider, access_token, refresh_token,
			expires_at, account_id, base_uri, scope, connected_by, connected_at, updated_at
		FROM esign_oauth_tokens
		WHERE tenant_id = $1 AND provider = $2`, tenantID, provider).
		Scan(&t.TenantID, &t.Provider, &t.AccessToken, &refresh,
			&t.ExpiresAt, &account, &base, &scope, &connectedBy, &t.ConnectedAt, &t.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if refresh != nil {
		t.RefreshToken = *refresh
	}
	if account != nil {
		t.AccountID = *account
	}
	if base != nil {
		t.BaseURI = *base
	}
	if scope != nil {
		t.Scope = *scope
	}
	if connectedBy != nil {
		t.ConnectedBy = *connectedBy
	}
	return t, nil
}

// ListESignTokens returns every connected provider for a tenant.
// Used by the /admin/integrations status block.
func (r *Repository) ListESignTokens(ctx context.Context, tenantID string) ([]*ESignToken, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tenant_id, provider, '' AS access_token, '' AS refresh_token,
			expires_at, COALESCE(account_id,''), COALESCE(base_uri,''), COALESCE(scope,''),
			COALESCE(connected_by::text,''), connected_at, updated_at
		FROM esign_oauth_tokens
		WHERE tenant_id = $1`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]*ESignToken, 0)
	for rows.Next() {
		t := &ESignToken{}
		if err := rows.Scan(&t.TenantID, &t.Provider, &t.AccessToken, &t.RefreshToken,
			&t.ExpiresAt, &t.AccountID, &t.BaseURI, &t.Scope,
			&t.ConnectedBy, &t.ConnectedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteESignToken disconnects a tenant's link to a provider.
func (r *Repository) DeleteESignToken(ctx context.Context, tenantID, provider string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM esign_oauth_tokens WHERE tenant_id = $1 AND provider = $2`,
		tenantID, provider)
	return err
}

// ESignEvent mirrors one webhook event row.
type ESignEvent struct {
	ID         string
	TenantID   string
	RequestID  string
	Provider   string
	EnvelopeID string
	EventType  string
	ExternalID string
	RawPayload []byte
	ReceivedAt time.Time
}

// InsertESignEventTx writes an event inside the caller's tx. The
// (provider, envelope_id, external_id) unique index ensures
// idempotency at the DB level — a re-delivery returns a unique-
// violation error the caller can ignore.
func (r *Repository) InsertESignEventTx(ctx context.Context, q Querier, e *ESignEvent) error {
	_, err := q.Exec(ctx, `
		INSERT INTO esign_envelope_events (
			id, tenant_id, request_id, provider, envelope_id,
			event_type, external_id, raw_payload, received_at)
		VALUES ($1,$2,$3,$4,$5,$6,NULLIF($7,''),$8,$9)`,
		e.ID, e.TenantID, e.RequestID, e.Provider, e.EnvelopeID,
		e.EventType, e.ExternalID, e.RawPayload, e.ReceivedAt)
	return err
}

// FindStaleInProgressRequests returns request ids the reconciler
// should poll. Anything 'in_progress' whose updated state hasn't
// flipped in `since` minutes — covers the case where the vendor
// ate the webhook.
func (r *Repository) FindStaleInProgressRequests(ctx context.Context, since time.Duration) ([]struct {
	TenantID, ID, EnvelopeID, Provider string
}, error) {
	cutoff := time.Now().Add(-since)
	rows, err := r.pool.Query(ctx, `
		SELECT tenant_id, id, COALESCE(provider_envelope_id,''), provider
		FROM signature_requests
		WHERE status = 'in_progress'
		  AND provider IN ('docusign','adobe_sign')
		  AND COALESCE(provider_envelope_id,'') <> ''
		  AND created_at < $1`, cutoff)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type row struct{ TenantID, ID, EnvelopeID, Provider string }
	out := make([]struct{ TenantID, ID, EnvelopeID, Provider string }, 0)
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.TenantID, &r.ID, &r.EnvelopeID, &r.Provider); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetEnvelopeID stamps the vendor envelope id onto the request.
func (r *Repository) SetEnvelopeIDTx(ctx context.Context, q Querier, tenantID, requestID, envelopeID string) error {
	_, err := q.Exec(ctx, `
		UPDATE signature_requests
		   SET provider_envelope_id = $3, status = 'in_progress'
		 WHERE tenant_id = $1 AND id = $2`,
		tenantID, requestID, envelopeID)
	return err
}

// FindRequestByEnvelope returns the signature_request id for a vendor
// envelope id. Webhook handler uses this to correlate the inbound
// callback with our row when our customField didn't survive.
func (r *Repository) FindRequestByEnvelope(ctx context.Context, tenantID, envelopeID string) (string, error) {
	var id string
	err := r.pool.QueryRow(ctx,
		`SELECT id FROM signature_requests
		 WHERE tenant_id = $1 AND provider_envelope_id = $2 LIMIT 1`,
		tenantID, envelopeID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
