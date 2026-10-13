// Package repository contains the Postgres data-access layer for the auth service.
package repository

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/pkg/database"
)

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// withTenant runs fn in the tenant's transaction. The per-tenant provider
// config tables (tenant_twilio_configs, tenant_smtp_configs) are FORCE
// ROW LEVEL SECURITY: on the raw pool the production NOBYPASSRLS role
// reads nothing and every write is rejected (M-27 inventory).
func (r *Repository) withTenant(ctx context.Context, tenantID string, fn func(tx pgx.Tx) error) error {
	tid, err := uuid.Parse(tenantID)
	if err != nil {
		return fmt.Errorf("tenant id: %w", err)
	}
	return database.WithTenantTx(ctx, r.pool, tid, fn)
}

// nullableUUID returns nil for empty strings so an FK column can be
// inserted as NULL when no caller is recorded. Postgres rejects an
// empty string cast to UUID, so an explicit nil interface is required.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
