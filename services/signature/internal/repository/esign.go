// ADR 0071 — repo for esign_oauth_tokens + esign_envelope_events.
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
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
	return r.withTenant(ctx, t.TenantID, func(tx pgx.Tx) error {
		return r.upsertESignTokenTx(ctx, tx, t)
	})
}

func (r *Repository) upsertESignTokenTx(ctx context.Context, q Querier, t *ESignToken) error {
	_, err := q.Exec(ctx, `
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
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT tenant_id, provider, access_token, refresh_token,
				expires_at, account_id, base_uri, scope, connected_by, connected_at, updated_at
			FROM esign_oauth_tokens
			WHERE tenant_id = $1 AND provider = $2`, tenantID, provider).
			Scan(&t.TenantID, &t.Provider, &t.AccessToken, &refresh,
				&t.ExpiresAt, &account, &base, &scope, &connectedBy, &t.ConnectedAt, &t.UpdatedAt)
	})
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
	out := make([]*ESignToken, 0)
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, provider, '' AS access_token, '' AS refresh_token,
				expires_at, COALESCE(account_id,''), COALESCE(base_uri,''), COALESCE(scope,''),
				COALESCE(connected_by::text,''), connected_at, updated_at
			FROM esign_oauth_tokens
			WHERE tenant_id = $1`, tenantID)
		if err != nil {
			return err
		}
		toks, err := scanESignTokens(rows)
		out = append(out, toks...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func scanESignTokens(rows pgx.Rows) ([]*ESignToken, error) {
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

// ListESignTokensExpiringWithin returns every tenant's tokens that
// will expire within `window` (including already-expired ones). The
// caller is the refresh worker, which runs outside any tenant scope, so
// the sweep enumerates tenants and reads each one inside its own tenant
// transaction (esign_oauth_tokens is FORCE RLS).
// access_token / refresh_token come back sealed so the caller can
// hand them straight to the seal-unseal cycle without re-reading.
func (r *Repository) ListESignTokensExpiringWithin(ctx context.Context, window time.Duration) ([]*ESignToken, error) {
	cutoff := time.Now().Add(window)
	out := make([]*ESignToken, 0)
	err := r.forEachTenant(ctx, func(tenantID uuid.UUID, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, provider, access_token, COALESCE(refresh_token,''),
				expires_at, COALESCE(account_id,''), COALESCE(base_uri,''), COALESCE(scope,''),
				COALESCE(connected_by::text,''), connected_at, updated_at
			FROM esign_oauth_tokens
			WHERE tenant_id = $1
			  AND expires_at <= $2
			  AND refresh_token IS NOT NULL`, tenantID, cutoff)
		if err != nil {
			return err
		}
		toks, err := scanESignTokens(rows)
		out = append(out, toks...)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteESignToken disconnects a tenant's link to a provider.
func (r *Repository) DeleteESignToken(ctx context.Context, tenantID, provider string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM esign_oauth_tokens WHERE tenant_id = $1 AND provider = $2`,
			tenantID, provider)
		return err
	})
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

// StaleRequest is one in-progress vendor envelope the reconciler polls.
type StaleRequest struct{ TenantID, ID, EnvelopeID, Provider string }

// FindStaleInProgressRequests returns request ids the reconciler
// should poll. Anything 'in_progress' whose updated state hasn't
// flipped in `since` minutes — covers the case where the vendor
// ate the webhook. Cross-tenant by nature, so it enumerates tenants and
// reads each inside its own tenant transaction (signature_requests is
// FORCE RLS).
func (r *Repository) FindStaleInProgressRequests(ctx context.Context, since time.Duration) ([]StaleRequest, error) {
	cutoff := time.Now().Add(-since)
	out := make([]StaleRequest, 0)
	err := r.forEachTenant(ctx, func(tenantID uuid.UUID, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, id, COALESCE(provider_envelope_id,''), provider
			FROM signature_requests
			WHERE tenant_id = $1
			  AND status = 'in_progress'
			  AND provider IN ('docusign','adobe_sign')
			  AND COALESCE(provider_envelope_id,'') <> ''
			  AND created_at < $2`, tenantID, cutoff)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sr StaleRequest
			if err := rows.Scan(&sr.TenantID, &sr.ID, &sr.EnvelopeID, &sr.Provider); err != nil {
				return err
			}
			out = append(out, sr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// FindInProgressESignRequests returns one tenant's in-progress vendor
// envelopes for the admin status tab.
func (r *Repository) FindInProgressESignRequests(ctx context.Context, tenantID string) ([]StaleRequest, error) {
	out := make([]StaleRequest, 0)
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT tenant_id, id, COALESCE(provider_envelope_id,''), provider
			FROM signature_requests
			WHERE tenant_id = $1
			  AND status = 'in_progress'
			  AND provider IN ('docusign','adobe_sign')
			  AND COALESCE(provider_envelope_id,'') <> ''`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var sr StaleRequest
			if err := rows.Scan(&sr.TenantID, &sr.ID, &sr.EnvelopeID, &sr.Provider); err != nil {
				return err
			}
			out = append(out, sr)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetEnvelopeIDTx stamps the vendor envelope id onto a pending, not yet
// sent request and moves it to in_progress. ErrStateConflict when the
// request was already sent or is no longer pending, so a request can never
// be rebound to a second envelope (S-1).
func (r *Repository) SetEnvelopeIDTx(ctx context.Context, q Querier, tenantID, requestID, envelopeID string) error {
	ct, err := q.Exec(ctx, `
		UPDATE signature_requests
		   SET provider_envelope_id = $3, status = 'in_progress'
		 WHERE tenant_id = $1 AND id = $2
		   AND status = 'pending'
		   AND COALESCE(provider_envelope_id, '') = ''`,
		tenantID, requestID, envelopeID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrStateConflict
	}
	return nil
}

// FindRequestByEnvelope returns the signature_request id for a vendor
// envelope id. Webhook handler uses this to correlate the inbound
// callback with our row when our customField didn't survive.
func (r *Repository) FindRequestByEnvelope(ctx context.Context, tenantID, envelopeID string) (string, error) {
	var id string
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT id FROM signature_requests
			 WHERE tenant_id = $1 AND provider_envelope_id = $2 LIMIT 1`,
			tenantID, envelopeID).Scan(&id)
	})
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
