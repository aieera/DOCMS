package service

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// pgxpoolConn is an alias so cache loaders don't need to import pgxpool.
type pgxpoolConn = pgxpool.Conn

// withTx begins a tx on a pool connection, runs fn, commits on success.
// Service-layer loaders use this because Redis misses still want the
// strong consistency of a read-repair within a transaction (so a permission
// we read here is not subject to a mid-query revoke).
func withTx(ctx context.Context, conn *pgxpool.Conn, fn func(tx pgx.Tx) error) (err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback(ctx)
			return
		}
		if cerr := tx.Commit(ctx); cerr != nil {
			err = cerr
		}
	}()
	return fn(tx)
}
