// Package repository contains the Postgres data-access layer for the
// search service. Only saved_searches lives in Postgres — the search
// index itself is in OpenSearch.
//
// RLS contract (Wave A.1.a, issue #70): saved_searches and
// saved_search_subscribers are FORCE ROW LEVEL SECURITY with policies on
// current_setting('app.current_tenant'). Every method here therefore
// runs inside database.WithTenantTx via withTenant — under the prod
// NOBYPASSRLS role a raw-pool query fails closed (0 rows / rejected
// writes), which is exactly how saved-search alerts silently died in
// prod before this fix. The explicit tenant_id SQL predicates remain as
// defense-in-depth; RLS is the backstop. The one deliberate exception
// is federated.go: platform_admins / federated_search_audit carry no
// RLS by design (ADR 0069) and stay on the raw pool.
package repository

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/search/internal/model"
)

// Repository manages saved_searches rows.
type Repository struct{ pool *pgxpool.Pool }

// New constructs a Repository.
func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// withTenant opens a tenant-scoped transaction (SET LOCAL
// app.current_tenant) and runs fn inside it. Same pattern as the
// document service's withTenantTx — the template for A.1.b–f.
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return vdmserr.Validation("tenant_id", "not a uuid")
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// CreateSavedSearch persists a new saved search.
func (r *Repository) CreateSavedSearch(ctx context.Context, ss *model.SavedSearch) error {
	filtersJSON, err := json.Marshal(ss.Filters)
	if err != nil {
		return err
	}
	return r.withTenant(ctx, ss.TenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `
			INSERT INTO saved_searches (id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, ss.ID, ss.TenantID, ss.UserID, ss.Name, ss.Query, filtersJSON,
			ss.Notify, ss.NotifyIntervalMinutes, ss.CreatedAt)
		return err
	})
}

// ListSavedSearches returns all saved searches for a user.
func (r *Repository) ListSavedSearches(ctx context.Context, tenantID, userID string) ([]*model.SavedSearch, error) {
	var out []*model.SavedSearch
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at, last_run_at
			FROM saved_searches
			WHERE tenant_id = $1 AND user_id = $2
			ORDER BY created_at DESC
		`, tenantID, userID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			ss, err := scanSavedSearch(rows)
			if err != nil {
				return err
			}
			out = append(out, ss)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// DeleteSavedSearch removes a saved search by id, scoped to tenant+user.
func (r *Repository) DeleteSavedSearch(ctx context.Context, tenantID, userID, id string) error {
	return r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx,
			`DELETE FROM saved_searches WHERE id = $1 AND tenant_id = $2 AND user_id = $3`,
			id, tenantID, userID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.ErrNotFound
		}
		return nil
	})
}

// GetSavedSearch returns a single saved search.
func (r *Repository) GetSavedSearch(ctx context.Context, tenantID, userID, id string) (*model.SavedSearch, error) {
	var out *model.SavedSearch
	err := r.withTenant(ctx, tenantID, func(tx pgx.Tx) error {
		row := tx.QueryRow(ctx, `
			SELECT id, tenant_id, user_id, name, query, filters, notify, notify_interval_minutes, created_at, last_run_at
			FROM saved_searches
			WHERE id = $1 AND tenant_id = $2 AND user_id = $3
		`, id, tenantID, userID)
		ss, err := scanSavedSearchRow(row)
		if err != nil {
			return err
		}
		out = ss
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
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
