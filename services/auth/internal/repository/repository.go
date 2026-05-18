// Package repository contains the Postgres data-access layer for the auth service.
package repository

import "github.com/jackc/pgx/v5/pgxpool"

type Repository struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Repository { return &Repository{pool: pool} }

// nullableUUID returns nil for empty strings so an FK column can be
// inserted as NULL when no caller is recorded. Postgres rejects an
// empty string cast to UUID, so an explicit nil interface is required.
func nullableUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
