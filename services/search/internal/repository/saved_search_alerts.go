// ADR 0085 — saved-search alert + subscriber operations.
//
// Lives next to the existing repository.go; every method runs inside
// a tenant-scoped transaction via withTenant (see repository.go — RLS
// is FORCED on the tables; the tenant_id predicates remain as
// defense-in-depth).
//
// Wave A.1.a note (issue #70): the former "admin" variants
// (GetSavedSearchAdmin, ListSubscribersAdmin), the alert-cursor writes
// (UpdateLastMatchDocIDs, UpdateLastRunAt) and the cross-tenant
// ListNotifiable scan were deleted — they had no callers, and their
// comments described an out-of-tenant RLS contract nothing satisfied.
// The real alert flow lives in services/workflow's saved-search-alert
// activities, which own their tenant context.
package repository

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/search/internal/model"
)

// UpdateSavedSearch patches the editable fields. Pass nil for any
// field you don't want to change. Returns ErrNotFound when the row
// doesn't exist or doesn't belong to the (tenant, user).
func (r *Repository) UpdateSavedSearch(
	ctx context.Context,
	tenantID, userID, id string,
	patch SavedSearchPatch,
) error {
	// Build the SET clause dynamically so an unset field doesn't
	// clobber the existing value with NULL.
	setClauses := []string{}
	args := []any{id, tenantID, userID}
	pos := 4

	if patch.Name != nil {
		setClauses = append(setClauses, "name = $"+itoa(pos))
		args = append(args, *patch.Name)
		pos++
	}
	if patch.Query != nil {
		setClauses = append(setClauses, "query = $"+itoa(pos))
		args = append(args, *patch.Query)
		pos++
	}
	if patch.Filters != nil {
		b, err := json.Marshal(patch.Filters)
		if err != nil {
			return err
		}
		setClauses = append(setClauses, "filters = $"+itoa(pos))
		args = append(args, b)
		pos++
	}
	if patch.Notify != nil {
		setClauses = append(setClauses, "notify = $"+itoa(pos))
		args = append(args, *patch.Notify)
		pos++
	}
	if patch.NotifyIntervalMinutes != nil {
		setClauses = append(setClauses, "notify_interval_minutes = $"+itoa(pos))
		args = append(args, *patch.NotifyIntervalMinutes)
		pos++
	}
	if patch.AlertFrequencyCron != nil {
		setClauses = append(setClauses, "alert_frequency_cron = $"+itoa(pos))
		args = append(args, *patch.AlertFrequencyCron)
		pos++
	}
	if patch.WorkflowID != nil {
		setClauses = append(setClauses, "workflow_id = $"+itoa(pos))
		args = append(args, *patch.WorkflowID)
		pos++
	}

	if len(setClauses) == 0 {
		return nil // no-op patch
	}

	sql := "UPDATE saved_searches SET " + joinComma(setClauses) +
		" WHERE id = $1 AND tenant_id = $2 AND user_id = $3"
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, sql, args...)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.ErrNotFound
		}
		return nil
	})
}

// SavedSearchPatch carries the optional fields for UpdateSavedSearch.
// Pointer fields so callers can distinguish "explicit empty string"
// from "unchanged".
type SavedSearchPatch struct {
	Name                  *string
	Query                 *string
	Filters               *model.SearchFilters
	Notify                *bool
	NotifyIntervalMinutes *int
	AlertFrequencyCron    *string
	WorkflowID            *string
}

// ---- subscribers --------------------------------------------------

// AddSubscriber upserts a subscription row. Idempotent — re-subscribing
// updates the channel preference rather than failing.
func (r *Repository) AddSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID, subscribedBy string,
	channels []string,
) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO saved_search_subscribers
			    (tenant_id, saved_search_id, user_id, channels, subscribed_by, subscribed_at)
			VALUES ($1, $2, $3, $4, $5, now())
			ON CONFLICT (tenant_id, saved_search_id, user_id) DO UPDATE
			   SET channels      = EXCLUDED.channels,
			       subscribed_by = EXCLUDED.subscribed_by,
			       subscribed_at = now()
		`, tenantID, savedSearchID, userID, channels, subscribedBy)
		return err
	})
}

// RemoveSubscriber drops a subscription row. Returns ErrNotFound when
// the user wasn't subscribed in the first place.
func (r *Repository) RemoveSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID string,
) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			DELETE FROM saved_search_subscribers
			 WHERE tenant_id = $1 AND saved_search_id = $2 AND user_id = $3
		`, tenantID, savedSearchID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.ErrNotFound
		}
		return nil
	})
}

// ListSubscribers returns the subscribers for one saved search.
// Used by the GET response to embed `subscribers[]` and by the
// alert workflow to fan out notifications.
func (r *Repository) ListSubscribers(
	ctx context.Context,
	tenantID, savedSearchID string,
) ([]model.SavedSearchSubscriber, error) {
	var out []model.SavedSearchSubscriber
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT user_id, channels, subscribed_by, subscribed_at
			  FROM saved_search_subscribers
			 WHERE tenant_id = $1 AND saved_search_id = $2
			 ORDER BY subscribed_at ASC
		`, tenantID, savedSearchID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var s model.SavedSearchSubscriber
			if err := rows.Scan(&s.UserID, &s.Channels, &s.SubscribedBy, &s.SubscribedAt); err != nil {
				return err
			}
			out = append(out, s)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteAllSubscribers wipes every subscription row for a saved
// search. Used by the cascade path on saved-search delete; called
// before the parent row is removed so a SIGTERM mid-operation
// doesn't leave orphan subscribers.
func (r *Repository) DeleteAllSubscribers(ctx context.Context, tenantID, savedSearchID string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx,
			`DELETE FROM saved_search_subscribers WHERE tenant_id = $1 AND saved_search_id = $2`,
			tenantID, savedSearchID)
		return err
	})
}

// ---- string helpers (kept here so this file is self-contained) ---

func itoa(n int) string {
	// Tight inner loop on UpdateSavedSearch — avoid the strconv import.
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if negative {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}
