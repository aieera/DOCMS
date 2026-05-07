// ADR 0070 — WebAuthn / Passkey + step-up persistence.
//
// Same `pgx.Tx`-wrapping pattern as user_repo.go: each method
// accepts an in-flight tx so the calling service can wrap multiple
// reads/writes in a single tenant-scoped transaction. Tenant
// scoping is via SET LOCAL app.current_tenant on the tx (the
// service does this via pkg/database before calling).
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// WebAuthnRepository is the persistence interface for ADR 0070.
type WebAuthnRepository interface {
	// UpsertCredential persists a new or re-registered credential.
	// Re-registering the same credential_id is treated as an UPDATE
	// (not a duplicate) so a user adding a passkey from the same
	// device twice doesn't end up with two rows.
	UpsertCredential(ctx context.Context, tx pgx.Tx, c *model.WebAuthnCredential) error

	// ListCredentials returns every cred for a user, ordered by
	// created_at ASC so the security UI shows oldest-first.
	ListCredentials(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]*model.WebAuthnCredential, error)

	// DeleteCredential removes a credential. Returns ErrNotFound
	// when the row doesn't exist or doesn't belong to the user.
	DeleteCredential(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, credentialID []byte) error

	// RecordUse bumps sign_count + last_used_at on a successful
	// assertion. Idempotent — re-applying the same sign_count is
	// a no-op (the assertion verification already rejected
	// backward-rolling counters).
	RecordUse(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, credentialID []byte, signCount int64) error

	// InsertStepUpGrant adds a fresh-presence row. ExpiresAt is
	// caller-controlled so the service can pick the 5-min default
	// or a tighter window per scope.
	InsertStepUpGrant(ctx context.Context, tx pgx.Tx, g *model.StepUpGrant) error

	// HasActiveStepUp returns true when any non-expired grant
	// matches (tenant, user, scope-or-wildcard). The middleware
	// path. Indexed lookup on (tenant, user, expires_at DESC).
	HasActiveStepUp(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, scope string) (bool, error)

	// PruneExpiredStepUpGrants — best-effort sweep called by the
	// service on a timer. Not load-bearing for correctness (the
	// HasActiveStepUp predicate filters expired rows already), but
	// keeps the table's row count bounded.
	PruneExpiredStepUpGrants(ctx context.Context, tx pgx.Tx, before time.Time) (int64, error)
}

// NewWebAuthnRepo returns the production impl.
func NewWebAuthnRepo() WebAuthnRepository { return &webAuthnRepo{} }

type webAuthnRepo struct{}

func (r *webAuthnRepo) UpsertCredential(ctx context.Context, tx pgx.Tx, c *model.WebAuthnCredential) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO webauthn_credentials
		    (tenant_id, credential_id, user_id, public_key, sign_count,
		     aaguid, transports, name, backup_eligible, backup_state,
		     created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now())
		ON CONFLICT (tenant_id, credential_id) DO UPDATE
		   SET public_key      = EXCLUDED.public_key,
		       sign_count      = EXCLUDED.sign_count,
		       aaguid          = EXCLUDED.aaguid,
		       transports      = EXCLUDED.transports,
		       name            = EXCLUDED.name,
		       backup_eligible = EXCLUDED.backup_eligible,
		       backup_state    = EXCLUDED.backup_state
	`, c.TenantID, c.CredentialID, c.UserID, c.PublicKey, c.SignCount,
		c.AAGUID, c.Transports, c.Name, c.BackupEligible, c.BackupState)
	return err
}

func (r *webAuthnRepo) ListCredentials(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]*model.WebAuthnCredential, error) {
	rows, err := tx.Query(ctx, `
		SELECT tenant_id, credential_id, user_id, public_key, sign_count,
		       COALESCE(aaguid, ''::bytea), transports, name,
		       backup_eligible, backup_state, created_at, last_used_at
		  FROM webauthn_credentials
		 WHERE tenant_id = $1 AND user_id = $2
		 ORDER BY created_at ASC
	`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.WebAuthnCredential
	for rows.Next() {
		var c model.WebAuthnCredential
		if err := rows.Scan(&c.TenantID, &c.CredentialID, &c.UserID, &c.PublicKey, &c.SignCount,
			&c.AAGUID, &c.Transports, &c.Name, &c.BackupEligible, &c.BackupState,
			&c.CreatedAt, &c.LastUsedAt); err != nil {
			return nil, err
		}
		out = append(out, &c)
	}
	return out, rows.Err()
}

func (r *webAuthnRepo) DeleteCredential(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, credentialID []byte) error {
	tag, err := tx.Exec(ctx, `
		DELETE FROM webauthn_credentials
		 WHERE tenant_id = $1 AND user_id = $2 AND credential_id = $3
	`, tenantID, userID, credentialID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *webAuthnRepo) RecordUse(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, credentialID []byte, signCount int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE webauthn_credentials
		   SET sign_count   = $3,
		       last_used_at = now()
		 WHERE tenant_id = $1 AND credential_id = $2
	`, tenantID, credentialID, signCount)
	return err
}

func (r *webAuthnRepo) InsertStepUpGrant(ctx context.Context, tx pgx.Tx, g *model.StepUpGrant) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO step_up_grants
		    (tenant_id, user_id, scope, granted_via, credential_id,
		     granted_at, expires_at)
		VALUES ($1, $2, $3, $4, $5, now(), $6)
	`, g.TenantID, g.UserID, g.Scope, g.GrantedVia, g.CredentialID, g.ExpiresAt)
	return err
}

func (r *webAuthnRepo) HasActiveStepUp(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, scope string) (bool, error) {
	var n int
	err := tx.QueryRow(ctx, `
		SELECT 1 FROM step_up_grants
		 WHERE tenant_id  = $1
		   AND user_id    = $2
		   AND (scope = '' OR scope = $3)
		   AND expires_at > now()
		 LIMIT 1
	`, tenantID, userID, scope).Scan(&n)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return n == 1, nil
}

func (r *webAuthnRepo) PruneExpiredStepUpGrants(ctx context.Context, tx pgx.Tx, before time.Time) (int64, error) {
	tag, err := tx.Exec(ctx, `
		DELETE FROM step_up_grants WHERE expires_at < $1
	`, before)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
