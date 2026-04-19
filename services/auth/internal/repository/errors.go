package repository

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

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
		case "23505":
			return vdmserr.Wrap(vdmserr.ErrAlreadyExists, err)
		case "23503":
			return vdmserr.Wrap(vdmserr.Conflict("foreign key violation"), err)
		case "23514":
			return vdmserr.Wrap(vdmserr.Validation("", pgErr.Message), err)
		case "23502":
			return vdmserr.Wrap(vdmserr.Validation(pgErr.ColumnName, "required"), err)
		}
	}
	return fmt.Errorf("auth db: %w", err)
}
