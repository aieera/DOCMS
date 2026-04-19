package database

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// WithTenant acquires a connection from the pool, sets the
// app.current_tenant session variable used by RLS policies, and calls fn. The
// connection is released to the pool when fn returns.
//
// CRITICAL: the tenant is set before fn runs; every query fn issues will be
// filtered by Postgres RLS. If tenantID is uuid.Nil, WithTenant returns an
// error — a zero tenant must never reach the database.
func WithTenant(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID uuid.UUID,
	fn func(conn *pgxpool.Conn) error,
) error {
	if tenantID == uuid.Nil {
		return fmt.Errorf("WithTenant: tenant id is zero")
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire conn: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)",
		tenantID.String(),
	); err != nil {
		return fmt.Errorf("set tenant guc: %w", err)
	}
	return fn(conn)
}

// WithTenantTx is like WithTenant but wraps fn in a transaction. The tenant
// variable is scoped to the transaction (local=true), so it is cleared on
// commit/rollback and cannot leak to another caller that reuses the conn.
func WithTenantTx(
	ctx context.Context,
	pool *pgxpool.Pool,
	tenantID uuid.UUID,
	fn func(tx pgx.Tx) error,
) error {
	if tenantID == uuid.Nil {
		return fmt.Errorf("WithTenantTx: tenant id is zero")
	}
	return WithTenant(ctx, pool, tenantID, func(conn *pgxpool.Conn) error {
		return WithTx(ctx, conn.Conn(), func(tx pgx.Tx) error {
			// Re-apply inside the TX: the outer set_config(local=true) was
			// scoped to the acquire; setting again under the TX with
			// local=true makes it bound to this TX.
			if _, err := tx.Exec(ctx,
				"SELECT set_config('app.current_tenant', $1, true)",
				tenantID.String(),
			); err != nil {
				return fmt.Errorf("set tenant guc (tx): %w", err)
			}
			return fn(tx)
		})
	})
}
