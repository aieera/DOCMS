package database

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// txBeginner is satisfied by *pgx.Conn, pgxpool.Conn.Conn(), and pgx.Tx —
// letting WithTx be used at the top level or nested (savepoint) without
// duplication.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// WithTx begins a transaction on conn, calls fn, and commits on success or
// rolls back on error / panic. When conn is already a pgx.Tx, a savepoint is
// used so the nested unit of work can be rolled back independently.
func WithTx(ctx context.Context, conn txBeginner, fn func(tx pgx.Tx) error) (err error) {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			_ = tx.Rollback(ctx)
			panic(r)
		}
		if err != nil {
			if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = fmt.Errorf("%w (rollback failed: %v)", err, rbErr)
			}
			return
		}
		if cmErr := tx.Commit(ctx); cmErr != nil {
			err = fmt.Errorf("commit tx: %w", cmErr)
		}
	}()

	return fn(tx)
}
