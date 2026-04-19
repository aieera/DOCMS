package repository

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// mapPgError translates pgx / pgconn errors into domain errors so that
// services never leak database specifics to their callers.
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return vdmserr.Wrap(vdmserr.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return vdmserr.Wrap(vdmserr.ErrAlreadyExists, err)
		case "23503": // foreign_key_violation
			return vdmserr.Wrap(vdmserr.Conflict("foreign key violation"), err)
		case "23514": // check_violation
			return vdmserr.Wrap(vdmserr.Validation("", pgErr.Message), err)
		case "23502": // not_null_violation
			return vdmserr.Wrap(vdmserr.Validation(pgErr.ColumnName, "field is required"), err)
		}
	}
	return fmt.Errorf("database error: %w", err)
}
