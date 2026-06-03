// ADR 0086 — matrix preferences + snooze + DND + digest accumulator.
package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/notification/internal/model"
)

// ListMatrix returns every cell saved for (tenant, user). Empty slice
// (not nil) when no rows — caller treats that as "use defaults".
func (r *Repository) ListMatrix(ctx context.Context, tenantID, userID string) ([]model.PrefCell, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT tenant_id, user_id, channel, event_type, is_enabled, digest_enabled
		FROM notification_preferences
		WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.PrefCell, 0)
	for rows.Next() {
		var c model.PrefCell
		if err := rows.Scan(&c.TenantID, &c.UserID, &c.Channel, &c.EventType, &c.IsEnabled, &c.DigestEnabled); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UpsertCell writes one matrix cell. Insert + ON CONFLICT keeps the
// flat-prefs back-compat path untouched.
func (r *Repository) UpsertCell(ctx context.Context, c model.PrefCell) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_preferences (tenant_id, user_id, channel, event_type, is_enabled, digest_enabled)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (tenant_id, user_id, channel, event_type)
		DO UPDATE SET is_enabled = EXCLUDED.is_enabled, digest_enabled = EXCLUDED.digest_enabled`,
		c.TenantID, c.UserID, c.Channel, c.EventType, c.IsEnabled, c.DigestEnabled)
	return err
}

// ReplaceMatrix bulk-writes all cells in one tx, removing any
// previously-saved cells that aren't in the new set. Used by the
// PUT /matrix endpoint.
func (r *Repository) ReplaceMatrix(ctx context.Context, tenantID, userID string, cells []model.PrefCell) error {
	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM notification_preferences WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID); err != nil {
		return err
	}
	for _, c := range cells {
		c.TenantID, c.UserID = tenantID, userID
		if _, err := tx.Exec(ctx, `
			INSERT INTO notification_preferences (tenant_id, user_id, channel, event_type, is_enabled, digest_enabled)
			VALUES ($1,$2,$3,$4,$5,$6)`,
			c.TenantID, c.UserID, c.Channel, c.EventType, c.IsEnabled, c.DigestEnabled); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// ----- Snoozes ----------------------------------------------------

// CreateSnooze inserts a row. duration > 0; UntilAt is recomputed to
// `now + duration` so callers don't have to think about clock skew.
func (r *Repository) CreateSnooze(ctx context.Context, s model.Snooze, duration time.Duration) (*model.Snooze, error) {
	if duration <= 0 {
		return nil, errors.New("duration must be positive")
	}
	until := time.Now().UTC().Add(duration)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO notification_snoozes (tenant_id, user_id, event_type, until_at, reason)
		VALUES ($1,$2,$3,$4,NULLIF($5,''))
		RETURNING id, until_at, created_at`,
		s.TenantID, s.UserID, s.EventType, until, s.Reason)
	if err := row.Scan(&s.ID, &s.UntilAt, &s.CreatedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// ListActiveSnoozes returns all of a user's snoozes whose until_at is
// in the future. Used to render the "Active snoozes" UI block.
func (r *Repository) ListActiveSnoozes(ctx context.Context, tenantID, userID string) ([]model.Snooze, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, user_id, event_type, until_at, COALESCE(reason,''), created_at
		FROM notification_snoozes
		WHERE tenant_id = $1 AND user_id = $2 AND until_at > now()
		ORDER BY until_at ASC`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Snooze, 0)
	for rows.Next() {
		var s model.Snooze
		if err := rows.Scan(&s.ID, &s.TenantID, &s.UserID, &s.EventType, &s.UntilAt, &s.Reason, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// IsSnoozed reports whether (user, event_type) has an active snooze.
// Matches both exact event_type and the wildcard '*' row.
func (r *Repository) IsSnoozed(ctx context.Context, tenantID, userID, eventType string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM notification_snoozes
			WHERE tenant_id = $1 AND user_id = $2
			  AND (event_type = $3 OR event_type = '*')
			  AND until_at > now()
		)`, tenantID, userID, eventType).Scan(&exists)
	return exists, err
}

// DeleteSnooze cancels a snooze early.
func (r *Repository) DeleteSnooze(ctx context.Context, tenantID, userID, id string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_snoozes WHERE tenant_id = $1 AND user_id = $2 AND id = $3`,
		tenantID, userID, id)
	return err
}

// ----- DND --------------------------------------------------------

// GetDND returns the user's DND row or nil if none set.
func (r *Repository) GetDND(ctx context.Context, tenantID, userID string) (*model.DND, error) {
	var d model.DND
	var startT, endT time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT tenant_id, user_id, dnd_start, dnd_end, timezone, updated_at
		FROM notification_dnd
		WHERE tenant_id = $1 AND user_id = $2`, tenantID, userID).
		Scan(&d.TenantID, &d.UserID, &startT, &endT, &d.Timezone, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	d.DNDStart = fmt.Sprintf("%02d:%02d", startT.Hour(), startT.Minute())
	d.DNDEnd = fmt.Sprintf("%02d:%02d", endT.Hour(), endT.Minute())
	return &d, nil
}

// UpsertDND saves the user's DND window. start/end are "HH:MM".
func (r *Repository) UpsertDND(ctx context.Context, d model.DND) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO notification_dnd (tenant_id, user_id, dnd_start, dnd_end, timezone)
		VALUES ($1,$2,$3::time,$4::time,$5)
		ON CONFLICT (tenant_id, user_id) DO UPDATE SET
			dnd_start = EXCLUDED.dnd_start,
			dnd_end   = EXCLUDED.dnd_end,
			timezone  = EXCLUDED.timezone,
			updated_at = now()`,
		d.TenantID, d.UserID, d.DNDStart, d.DNDEnd, d.Timezone)
	return err
}

// DeleteDND clears the user's DND row.
func (r *Repository) DeleteDND(ctx context.Context, tenantID, userID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM notification_dnd WHERE tenant_id = $1 AND user_id = $2`,
		tenantID, userID)
	return err
}

// ----- Digests ----------------------------------------------------

// UpsertDigestEvent appends `event` to the open digest for
// (user, event_type, channel) — opening a new row if none exists.
// flush_after_at is set on first-open and never extended; that
// guarantees a slow trickle still flushes within `windowSec` of
// the FIRST event.
func (r *Repository) UpsertDigestEvent(ctx context.Context, d model.Digest, event map[string]any, windowSec int) error {
	raw, err := json.Marshal(event)
	if err != nil {
		return err
	}
	if windowSec <= 0 {
		windowSec = 300
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO notification_digests (
			tenant_id, user_id, event_type, channel,
			events, count, flush_after_at
		)
		VALUES ($1,$2,$3,$4, jsonb_build_array($5::jsonb), 1, now() + ($6 || ' seconds')::interval)
		ON CONFLICT (tenant_id, user_id, event_type, channel) WHERE flushed_at IS NULL
		DO UPDATE SET
			events = notification_digests.events || EXCLUDED.events,
			count  = notification_digests.count + 1`,
		d.TenantID, d.UserID, d.EventType, d.Channel, string(raw), windowSec)
	return err
}

// ClaimReadyDigests atomically marks ripe digests flushed and returns
// what was claimed. Idempotent under concurrent flush ticks because
// `RETURNING` only yields rows the UPDATE actually mutated.
func (r *Repository) ClaimReadyDigests(ctx context.Context, batchSize int) ([]model.Digest, error) {
	if batchSize <= 0 {
		batchSize = 100
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE notification_digests
		SET flushed_at = now()
		WHERE id IN (
			SELECT id FROM notification_digests
			WHERE flushed_at IS NULL AND flush_after_at <= now()
			ORDER BY flush_after_at ASC
			LIMIT $1
			FOR UPDATE SKIP LOCKED
		)
		RETURNING id, tenant_id, user_id, event_type, channel, events, count, flush_after_at`,
		batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]model.Digest, 0)
	for rows.Next() {
		var d model.Digest
		if err := rows.Scan(&d.ID, &d.TenantID, &d.UserID, &d.EventType, &d.Channel, &d.Events, &d.Count, &d.FlushAfterAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
