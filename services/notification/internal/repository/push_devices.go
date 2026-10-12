// Push-device registry (ADR 0117 — mobile app). push_devices is FORCE RLS
// (notification migration 000004, M-28): every query runs inside a tenant
// transaction and keeps its explicit tenant_id predicate.
package repository

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/notification/internal/model"
)

// UpsertPushDevice registers (or refreshes) a device token for a user.
// A token re-registered by a different user (logout → login on the same
// phone) is reassigned to the new user; revocation is cleared.
func (r *Repository) UpsertPushDevice(ctx context.Context, d *model.PushDevice) error {
	return r.withTenant(ctx, d.TenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
		INSERT INTO push_devices (tenant_id, user_id, platform, token, label)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, token) DO UPDATE SET
			user_id = EXCLUDED.user_id,
			platform = EXCLUDED.platform,
			label = EXCLUDED.label,
			last_seen_at = now(),
			revoked_at = NULL
		RETURNING id, created_at
	`, d.TenantID, d.UserID, d.Platform, d.Token, d.Label).Scan(&d.ID, &d.CreatedAt)
	})
}

// ListPushDevices returns a user's active (non-revoked) devices.
func (r *Repository) ListPushDevices(ctx context.Context, tenantID, userID string) ([]model.PushDevice, error) {
	var out []model.PushDevice
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, user_id, platform, token, label, created_at, last_seen_at
			FROM push_devices
			WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL
			ORDER BY created_at
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d model.PushDevice
			if err := rows.Scan(&d.ID, &d.TenantID, &d.UserID, &d.Platform, &d.Token, &d.Label, &d.CreatedAt, &d.LastSeenAt); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// DeletePushDevice removes a device the caller owns. Returns false when
// no matching row existed.
func (r *Repository) DeletePushDevice(ctx context.Context, tenantID, userID, deviceID string) (bool, error) {
	var n int64
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM push_devices
			WHERE tenant_id = $1 AND user_id = $2 AND id = $3
		`, tenantID, userID, deviceID)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RevokePushTokens marks tokens dead (Expo's DeviceNotRegistered) so the
// fan-out stops addressing them. Revocation (not deletion) keeps an
// audit trail of when a device fell off.
func (r *Repository) RevokePushTokens(ctx context.Context, tenantID string, tokens []string) error {
	if len(tokens) == 0 {
		return nil
	}
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			UPDATE push_devices SET revoked_at = now()
			WHERE tenant_id = $1 AND token = ANY($2) AND revoked_at IS NULL
		`, tenantID, tokens)
		return err
	})
}
