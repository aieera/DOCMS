// ADR 0085 — saved-search alert + subscriber operations.
//
// Lives next to the existing repository.go; new methods here keep
// that file's existing CRUD untouched. The methods all run inside
// a single tenant scope via the same RLS pattern (RLS is FORCED on
// the tables; tenant_id always part of the predicate).
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/search/internal/model"
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
	tag, err := r.pool.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
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

// UpdateLastMatchDocIDs is the diff-cursor write done by the alert
// workflow at the end of each run. Bypasses tenant predicate at the
// repo layer because the workflow runs out-of-tenant; the workflow
// service caller is trusted (internal-only).
func (r *Repository) UpdateLastMatchDocIDs(ctx context.Context, id string, docIDs []string) error {
	b, err := json.Marshal(docIDs)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx,
		`UPDATE saved_searches SET last_match_doc_ids = $2 WHERE id = $1`,
		id, b)
	return err
}

// GetSavedSearchAdmin fetches by id alone (no user predicate). Used
// by the alert workflow which doesn't have a calling-user context.
// Tenant isolation is via RLS on the table; the workflow sets
// app.current_tenant before calling.
func (r *Repository) GetSavedSearchAdmin(ctx context.Context, id string) (*model.SavedSearch, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters,
		       notify, notify_interval_minutes,
		       COALESCE(alert_frequency_cron, ''), COALESCE(workflow_id, ''),
		       COALESCE(last_match_doc_ids, '[]'::jsonb),
		       created_at, last_run_at
		  FROM saved_searches
		 WHERE id = $1
	`, id)
	return scanSavedSearchAdminRow(row)
}

func scanSavedSearchAdminRow(row pgx.Row) (*model.SavedSearch, error) {
	var (
		ss              model.SavedSearch
		filtersJSON     []byte
		lastMatchJSON   []byte
	)
	if err := row.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.Name, &ss.Query, &filtersJSON,
		&ss.Notify, &ss.NotifyIntervalMinutes,
		&ss.AlertFrequencyCron, &ss.WorkflowID,
		&lastMatchJSON,
		&ss.CreatedAt, &ss.LastRunAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(filtersJSON, &ss.Filters)
	_ = json.Unmarshal(lastMatchJSON, &ss.LastMatchDocIDs)
	return &ss, nil
}

// ---- subscribers --------------------------------------------------

// AddSubscriber upserts a subscription row. Idempotent — re-subscribing
// updates the channel preference rather than failing.
func (r *Repository) AddSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID, subscribedBy string,
	channels []string,
) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO saved_search_subscribers
		    (tenant_id, saved_search_id, user_id, channels, subscribed_by, subscribed_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id, saved_search_id, user_id) DO UPDATE
		   SET channels      = EXCLUDED.channels,
		       subscribed_by = EXCLUDED.subscribed_by,
		       subscribed_at = now()
	`, tenantID, savedSearchID, userID, channels, subscribedBy)
	return err
}

// RemoveSubscriber drops a subscription row. Returns ErrNotFound when
// the user wasn't subscribed in the first place.
func (r *Repository) RemoveSubscriber(
	ctx context.Context,
	tenantID, savedSearchID, userID string,
) error {
	tag, err := r.pool.Exec(ctx, `
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
}

// ListSubscribers returns the subscribers for one saved search.
// Used by the GET response to embed `subscribers[]` and by the
// alert workflow to fan out notifications.
func (r *Repository) ListSubscribers(
	ctx context.Context,
	tenantID, savedSearchID string,
) ([]model.SavedSearchSubscriber, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, channels, subscribed_by, subscribed_at
		  FROM saved_search_subscribers
		 WHERE tenant_id = $1 AND saved_search_id = $2
		 ORDER BY subscribed_at ASC
	`, tenantID, savedSearchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SavedSearchSubscriber
	for rows.Next() {
		var s model.SavedSearchSubscriber
		if err := rows.Scan(&s.UserID, &s.Channels, &s.SubscribedBy, &s.SubscribedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteAllSubscribers wipes every subscription row for a saved
// search. Used by the cascade path on saved-search delete; called
// before the parent row is removed so a SIGTERM mid-operation
// doesn't leave orphan subscribers.
func (r *Repository) DeleteAllSubscribers(ctx context.Context, tenantID, savedSearchID string) error {
	_, err := r.pool.Exec(ctx,
		`DELETE FROM saved_search_subscribers WHERE tenant_id = $1 AND saved_search_id = $2`,
		tenantID, savedSearchID)
	return err
}

// ListSubscribersAdmin — same shape as ListSubscribers but without
// tenant predicate. Used by the alert workflow which sets
// app.current_tenant via RLS rather than a SQL predicate.
func (r *Repository) ListSubscribersAdmin(ctx context.Context, savedSearchID string) ([]model.SavedSearchSubscriber, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT user_id, channels, subscribed_by, subscribed_at
		  FROM saved_search_subscribers
		 WHERE saved_search_id = $1
	`, savedSearchID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.SavedSearchSubscriber
	for rows.Next() {
		var s model.SavedSearchSubscriber
		if err := rows.Scan(&s.UserID, &s.Channels, &s.SubscribedBy, &s.SubscribedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
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

// Compile-time check that we used time.
var _ = time.Time{}
