// Package service holds the task service's business logic: core CRUD
// (Task 4), status transitions / assignee & document management (Task
// 5), comments + activity feed + sweep (Task 6). This file wires the
// shared TaskService struct plus the caller/tenant-tx plumbing every
// other file in this package builds on.
package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/services/task/internal/repository"
)

// Sentinel errors this service returns. Handlers (Task 7) map these to
// wire codes with errors.Is: ErrValidation -> 400, ErrForbidden -> 403,
// ErrNotFound -> 404. Kept as plain stdlib sentinels (not the richer
// pkg/errors.Error taxonomy the document service uses) per the task-4
// brief — this service's error surface is small enough that a field/kind
// struct would be more machinery than the callers need right now.
var (
	ErrValidation = errors.New("validation error")
	ErrForbidden  = errors.New("forbidden")
	ErrNotFound   = errors.New("not found")
)

// validationErr/forbiddenErr/notFoundErr wrap a human-readable message
// around the matching sentinel with fmt.Errorf's %w, so callers can
// errors.Is(err, ErrValidation) etc. while still getting a useful
// Error() string for logs.
func validationErr(msg string) error { return fmt.Errorf("%s: %w", msg, ErrValidation) }
func forbiddenErr(msg string) error  { return fmt.Errorf("%s: %w", msg, ErrForbidden) }
func notFoundErr(msg string) error   { return fmt.Errorf("%s: %w", msg, ErrNotFound) }

// TaskService is the task service's business-logic layer: every
// exported method below (and in tasks.go) takes an authenticated ctx,
// resolves tenant/user/role via mustCaller, and runs its DB work inside
// a single database.WithTenantTx so the write + its activity row(s) +
// its outbox event(s) commit or roll back together.
type TaskService struct {
	Repos *repository.Repos
	Pool  *pgxpool.Pool
	Log   zerolog.Logger
}

// New wires a TaskService against pool, constructing its own Repos via
// repository.New. Signature is unchanged from Task 1's placeholder so
// cmd/server/main.go's call site (`service.New(pool, *log.Z())`) keeps
// compiling without edits.
func New(pool *pgxpool.Pool, log zerolog.Logger) *TaskService {
	return &TaskService{
		Repos: repository.New(pool),
		Pool:  pool,
		Log:   log,
	}
}

// mustCaller extracts tenant/user/role from ctx via auth.User — ported
// from the document service's service.go:871 mustCaller, but returning
// role too (this service's permission gates need it on every call,
// whereas the document service re-derives it separately via
// callerRole(ctx)). Unlike the document service's version, an absent or
// zero-value caller is always an error here: every task-service route
// sits behind middleware.SessionAuth (see cmd/server/main.go), so a
// missing UserInfo on ctx by the time a service method runs indicates a
// wiring bug, not a legitimate anonymous/system caller.
func mustCaller(ctx context.Context) (tenantID, userID uuid.UUID, role string, err error) {
	u, err := auth.User(ctx)
	if err != nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("task service: caller: %w", err)
	}
	if u.TenantID == uuid.Nil || u.ID == uuid.Nil {
		return uuid.Nil, uuid.Nil, "", fmt.Errorf("task service: caller: %w", auth.ErrMissing)
	}
	return u.TenantID, u.ID, u.Role, nil
}

// withTenantTx is a thin wrapper around database.WithTenantTx, matching
// the document service's service.go:886 helper of the same name.
func (s *TaskService) withTenantTx(ctx context.Context, tenantID uuid.UUID, fn func(tx pgx.Tx) error) error {
	return database.WithTenantTx(ctx, s.Pool, tenantID, fn)
}
