// Single-use OAuth state nonces for the e-sign connect flow (migration
// document 000112, review finding S-2).
package repository

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// CreateOAuthState stores a nonce hash bound to (tenant, user, provider)
// until expiresAt. Expired rows of the tenant are swept on the way in.
func (r *Repository) CreateOAuthState(ctx context.Context, tenantID, userID, provider, nonceHash string, expiresAt time.Time) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx,
			`DELETE FROM esign_oauth_states WHERE tenant_id = $1 AND expires_at < now()`, tenantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO esign_oauth_states (tenant_id, nonce_hash, user_id, provider, expires_at)
			VALUES ($1, $2, $3, $4, $5)`,
			tenantID, nonceHash, userID, provider, expiresAt)
		return err
	})
}

// ConsumeOAuthState deletes the matching, unexpired nonce and reports
// whether one existed. The single DELETE … RETURNING makes the nonce
// single-use even under concurrent callbacks, and the (tenant, user,
// provider) predicate binds it to the admin who started the flow.
func (r *Repository) ConsumeOAuthState(ctx context.Context, tenantID, userID, provider, nonceHash string) (bool, error) {
	var consumed bool
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		var got string
		err := tx.QueryRow(ctx, `
			DELETE FROM esign_oauth_states
			 WHERE tenant_id = $1 AND nonce_hash = $2 AND user_id = $3
			   AND provider = $4 AND expires_at > now()
			RETURNING nonce_hash`,
			tenantID, nonceHash, userID, provider).Scan(&got)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		consumed = true
		return nil
	})
	return consumed, err
}
