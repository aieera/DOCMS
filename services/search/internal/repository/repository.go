// Package repository contains the Postgres data-access layer for the
// search service. Only saved_searches lives in Postgres — the search
// index itself is in OpenSearch.
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/search/internal/model"
)

// Repository manages saved_searches rows.
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// CreateSavedSearch persists a new saved search.
func (r *Repository) CreateSavedSearch(ctx context.Context, ss *model.SavedSearch) error {
	filtersJSON, err := json.Marshal(ss.Filters)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO saved_searches (id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, ss.ID, ss.TenantID, ss.UserID, ss.Name, ss.Query, filtersJSON,
		ss.Notify, ss.NotifyIntervalMinutes, ss.CreatedAt)
	return err
}

// ListSavedSearches returns all saved searches for a user.
func (r *Repository) ListSavedSearches(ctx context.Context, tenantID, userID string) ([]*model.SavedSearch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at, last_run_at
		FROM saved_searches
		WHERE tenant_id = $1 AND user_id = $2
		ORDER BY created_at DESC
	`, tenantID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.SavedSearch
	for rows.Next() {
		ss, err := scanSavedSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// DeleteSavedSearch removes a saved search by id, scoped to tenant+user.
func (r *Repository) DeleteSavedSearch(ctx context.Context, tenantID, userID, id string) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM saved_searches WHERE id = $1 AND tenant_id = $2 AND user_id = $3`,
		id, tenantID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// GetSavedSearch returns a single saved search.
func (r *Repository) GetSavedSearch(ctx context.Context, tenantID, userID, id string) (*model.SavedSearch, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at, last_run_at
		FROM saved_searches
		WHERE id = $1 AND tenant_id = $2 AND user_id = $3
	`, id, tenantID, userID)
	return scanSavedSearchRow(row)
}

// UpdateLastRunAt stamps the last execution time for notification checks.
func (r *Repository) UpdateLastRunAt(ctx context.Context, id string, t time.Time) error {
	_, err := r.pool.Exec(ctx,
		`UPDATE saved_searches SET last_run_at = $2 WHERE id = $1`, id, t)
	return err
}

// ListNotifiable returns saved searches with notify=true, due for a run.
func (r *Repository) ListNotifiable(ctx context.Context) ([]*model.SavedSearch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at, last_run_at
		FROM saved_searches
		WHERE notify = true
		AND (last_run_at IS NULL OR last_run_at + (notify_interval_minutes || ' minutes')::interval < now())
		ORDER BY created_at ASC
		LIMIT 100
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.SavedSearch
	for rows.Next() {
		ss, err := scanSavedSearch(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// NewID returns a new v7 UUID string.
func NewID() string {
	id, err := uuid.NewV7()
	if err != nil {
		return uuid.New().String()
	}
	return id.String()
}

// ---- scan helpers ---------------------------------------------------------

func scanSavedSearch(rows pgx.Rows) (*model.SavedSearch, error) {
	var (
		ss          model.SavedSearch
		filtersJSON []byte
	)
	if err := rows.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.Name, &ss.Query, &filtersJSON,
		&ss.Notify, &ss.NotifyIntervalMinutes, &ss.CreatedAt, &ss.LastRunAt,
	); err != nil {
		return nil, err
	}
	_ = json.Unmarshal(filtersJSON, &ss.Filters)
	return &ss, nil
}

func scanSavedSearchRow(row pgx.Row) (*model.SavedSearch, error) {
	var (
		ss          model.SavedSearch
		filtersJSON []byte
	)
	if err := row.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.Name, &ss.Query, &filtersJSON,
		&ss.Notify, &ss.NotifyIntervalMinutes, &ss.CreatedAt, &ss.LastRunAt,
	); err != nil {
		if err == pgx.ErrNoRows {
			return nil, vdmserr.ErrNotFound
		}
		return nil, err
	}
	_ = json.Unmarshal(filtersJSON, &ss.Filters)
	return &ss, nil
}
