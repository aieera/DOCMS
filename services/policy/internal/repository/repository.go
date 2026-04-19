// Package repository holds the Postgres data-access layer for the policy
// service. Callers invoke methods inside database.WithTenant* so RLS fires.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
)

// PermissionRepo manages the permissions table.
type PermissionRepo interface {
	ListByResource(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, at time.Time) ([]model.Permission, error)
	ListByResourceAsOf(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.ResourceType, id uuid.UUID, asOf time.Time) ([]model.Permission, error)
	ListByPrincipal(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, kind model.PrincipalType, id uuid.UUID, at time.Time) ([]model.Permission, error)
	Insert(ctx context.Context, tx pgx.Tx, p *model.Permission) error
	Revoke(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, at time.Time) (*model.Permission, error)
}

// GroupRepo returns a user's group memberships.
type GroupRepo interface {
	GroupsForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]uuid.UUID, error)
	UsersInGroup(ctx context.Context, tx pgx.Tx, tenantID, groupID uuid.UUID) ([]uuid.UUID, error)
}

// WorkspaceRepo returns workspace memberships (for the workspace-admin rule).
type WorkspaceRepo interface {
	MembershipsForUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.WorkspaceMember, error)
}

// Bundle wires all repos. Stateless; share one per process.
type Bundle struct {
	Pool        *pgxpool.Pool
	Permissions PermissionRepo
	Groups      GroupRepo
	Workspaces  WorkspaceRepo
}

// New returns a wired repo bundle.
func New(pool *pgxpool.Pool) *Bundle {
	return &Bundle{
		Pool:        pool,
		Permissions: &permissionRepo{},
		Groups:      &groupRepo{},
		Workspaces:  &workspaceRepo{},
	}
}

// ---- shared error mapping -------------------------------------------------

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
		}
	}
	return fmt.Errorf("policy db: %w", err)
}
