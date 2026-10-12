package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTenantTx runs fn inside a transaction whose app.current_tenant GUC
// (the value every RLS policy reads) is set to tenantID.
//
// The GUC is set with set_config(..., true) INSIDE the transaction, so it
// is bound to that transaction: every query fn issues is filtered by
// Postgres RLS, and the value is cleared on commit/rollback so it cannot
// leak to another caller that reuses the pooled connection. Setting it
// outside a transaction does not work — in autocommit the "local" value
// dies with the set_config statement itself (review finding C-5).
//
// A zero tenantID is rejected: a zero tenant must never reach the
// database.
func WithTenantTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID uuid.UUID,
	fn func(tx pgx.Tx) error,
) error {
	if tenantID == uuid.Nil {
		return fmt.Errorf("WithTenantTx: tenant id is zero")
	}
	return WithTx(ctx, pool, func(tx pgx.Tx) error {
		if err := setTenantGUC(ctx, tx, tenantID); err != nil {
			return err
		}
		return fn(tx)
	})
}

// setTenantGUC binds app.current_tenant to tenantID for the remainder of
// tx. It does not reject uuid.Nil: the outbox drain must be able to act on
// whatever tenant a queued row carries rather than wedge on it. Callers
// outside this package use WithTenantTx, which does reject a zero tenant.
func setTenantGUC(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) error {
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)",
		tenantID.String(),
	); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	return nil
}
