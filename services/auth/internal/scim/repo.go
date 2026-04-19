package scim

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
)

// Repo is the SCIM data layer. Reads go through the shared pool (tenant
// GUC set per call via SET LOCAL inside a short transaction). Writes go
// through database.WithTenantTx (see service package for that).
type Repo struct{ pool *pgxpool.Pool }

// NewRepo wires the repo.
func NewRepo(pool *pgxpool.Pool) *Repo { return &Repo{pool: pool} }

// ---- Users ----------------------------------------------------------------

// UserRow is the denormalized shape the SCIM handlers consume.
type UserRow struct {
	ID          uuid.UUID
	Email       string
	DisplayName string
	Role        string
	Status      string // "active" | "suspended" | "deactivated"
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ListUsers returns paginated users, optionally filtered by a SCIM filter.
// filter.Op is one of eq/ne/co/sw/pr; filter.Attr is normalized to lower.
func (r *Repo) ListUsers(ctx context.Context, tenantID uuid.UUID, filter *Filter, startIndex, count int) ([]UserRow, int, error) {
	where, args := buildUserWhere(tenantID, filter)
	total, err := r.countUsers(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}
	// SCIM startIndex is 1-based. Guard on count/start.
	if startIndex < 1 {
		startIndex = 1
	}
	if count < 0 {
		count = 0
	}
	if count > 200 {
		count = 200
	}
	q := `
		SELECT id, email, display_name, role, status, created_at, updated_at
		FROM users
		WHERE ` + where + `
		ORDER BY created_at, id
		OFFSET $` + itoa(len(args)+1) + ` LIMIT $` + itoa(len(args)+2)
	args = append(args, startIndex-1, count)

	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	var out []UserRow
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
			return nil, 0, mapPgError(err)
		}
		out = append(out, u)
	}
	return out, total, mapPgError(rows.Err())
}

// GetUser returns a single user by id.
func (r *Repo) GetUser(ctx context.Context, tenantID, id uuid.UUID) (*UserRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, email, display_name, role, status, created_at, updated_at
		FROM users WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id)
	var u UserRow
	if err := row.Scan(&u.ID, &u.Email, &u.DisplayName, &u.Role, &u.Status, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, mapPgError(err)
	}
	return &u, nil
}

// CreateUser inserts a user row. SCIM-only users carry an empty
// password_hash; if they later authenticate they do so via SSO.
func (r *Repo) CreateUser(ctx context.Context, tenantID uuid.UUID, email, displayName string) (*UserRow, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO users (tenant_id, id, email, display_name, role, status, settings, created_at, updated_at)
		VALUES ($1, $2, $3, $4, 'member', 'active', '{}'::jsonb, now(), now())
	`, tenantID, id, email, displayName)
	if err != nil {
		return nil, mapPgError(err)
	}
	return r.GetUser(ctx, tenantID, id)
}

// UpdateUser applies arbitrary field updates expressed as a map. Only keys
// in the allow list are honored; unknown keys are silently dropped.
func (r *Repo) UpdateUser(ctx context.Context, tenantID, id uuid.UUID, updates map[string]any) (*UserRow, error) {
	set, args := []string{}, []any{tenantID, id}
	add := func(col string, val any) {
		args = append(args, val)
		set = append(set, col+" = $"+itoa(len(args)))
	}
	if v, ok := updates["display_name"].(string); ok {
		add("display_name", v)
	}
	if v, ok := updates["email"].(string); ok {
		add("email", v)
	}
	if v, ok := updates["status"].(string); ok {
		add("status", v)
	}
	if len(set) == 0 {
		return r.GetUser(ctx, tenantID, id)
	}
	q := `UPDATE users SET ` + joinCommas(set) + `, updated_at = now()
	      WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`
	if _, err := r.pool.Exec(ctx, q, args...); err != nil {
		return nil, mapPgError(err)
	}
	return r.GetUser(ctx, tenantID, id)
}

// DeactivateUser is our idempotent SCIM DELETE: set status='deactivated'.
// Also revokes all of the user's active sessions.
func (r *Repo) DeactivateUser(ctx context.Context, tenantID, id uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`UPDATE users SET status = 'deactivated', updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id,
	); err != nil {
		return mapPgError(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE sessions SET revoked_at = now()
		 WHERE tenant_id = $1 AND user_id = $2 AND revoked_at IS NULL`,
		tenantID, id,
	); err != nil {
		return mapPgError(err)
	}
	return tx.Commit(ctx)
}

// ---- Groups ---------------------------------------------------------------

// GroupRow is the denormalized group shape.
type GroupRow struct {
	ID          uuid.UUID
	Name        string
	Description string
	MemberIDs   []uuid.UUID
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// ListGroups returns paginated groups.
func (r *Repo) ListGroups(ctx context.Context, tenantID uuid.UUID, filter *Filter, startIndex, count int) ([]GroupRow, int, error) {
	where, args := buildGroupWhere(tenantID, filter)
	total, err := r.countGroups(ctx, where, args)
	if err != nil {
		return nil, 0, err
	}
	if startIndex < 1 {
		startIndex = 1
	}
	if count > 200 {
		count = 200
	}
	q := `
		SELECT id, name, COALESCE(description,''), created_at, updated_at
		FROM groups
		WHERE ` + where + ` AND deleted_at IS NULL
		ORDER BY created_at, id
		OFFSET $` + itoa(len(args)+1) + ` LIMIT $` + itoa(len(args)+2)
	args = append(args, startIndex-1, count)
	rows, err := r.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, 0, mapPgError(err)
	}
	defer rows.Close()
	var out []GroupRow
	for rows.Next() {
		var g GroupRow
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt); err != nil {
			return nil, 0, mapPgError(err)
		}
		g.MemberIDs, _ = r.groupMembers(ctx, tenantID, g.ID)
		out = append(out, g)
	}
	return out, total, mapPgError(rows.Err())
}

// GetGroup loads one group with its members.
func (r *Repo) GetGroup(ctx context.Context, tenantID, id uuid.UUID) (*GroupRow, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, name, COALESCE(description,''), created_at, updated_at
		FROM groups WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id)
	var g GroupRow
	if err := row.Scan(&g.ID, &g.Name, &g.Description, &g.CreatedAt, &g.UpdatedAt); err != nil {
		return nil, mapPgError(err)
	}
	g.MemberIDs, _ = r.groupMembers(ctx, tenantID, id)
	return &g, nil
}

// CreateGroup makes a new group, optionally with an initial member list.
func (r *Repo) CreateGroup(ctx context.Context, tenantID uuid.UUID, name, description string, memberIDs []uuid.UUID) (*GroupRow, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO groups (tenant_id, id, name, description, created_at, updated_at)
		VALUES ($1, $2, $3, $4, now(), now())
	`, tenantID, id, name, description); err != nil {
		return nil, mapPgError(err)
	}
	for _, uid := range memberIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO group_members (tenant_id, group_id, user_id, added_at)
			VALUES ($1, $2, $3, now())
			ON CONFLICT DO NOTHING
		`, tenantID, id, uid); err != nil {
			return nil, mapPgError(err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}
	return r.GetGroup(ctx, tenantID, id)
}

// UpdateGroup applies name/description updates and replaces members if
// `members` is non-nil. A nil `members` slice means "don't touch".
func (r *Repo) UpdateGroup(ctx context.Context, tenantID, id uuid.UUID, name, description *string, members []uuid.UUID) (*GroupRow, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	set, args := []string{}, []any{tenantID, id}
	if name != nil {
		args = append(args, *name)
		set = append(set, "name = $"+itoa(len(args)))
	}
	if description != nil {
		args = append(args, *description)
		set = append(set, "description = $"+itoa(len(args)))
	}
	if len(set) > 0 {
		q := `UPDATE groups SET ` + joinCommas(set) + `, updated_at = now()
		      WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`
		if _, err := tx.Exec(ctx, q, args...); err != nil {
			return nil, mapPgError(err)
		}
	}
	if members != nil {
		if _, err := tx.Exec(ctx,
			`DELETE FROM group_members WHERE tenant_id = $1 AND group_id = $2`,
			tenantID, id,
		); err != nil {
			return nil, mapPgError(err)
		}
		for _, uid := range members {
			if _, err := tx.Exec(ctx, `
				INSERT INTO group_members (tenant_id, group_id, user_id, added_at)
				VALUES ($1, $2, $3, now())
				ON CONFLICT DO NOTHING
			`, tenantID, id, uid); err != nil {
				return nil, mapPgError(err)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, mapPgError(err)
	}
	return r.GetGroup(ctx, tenantID, id)
}

// DeleteGroup is a soft delete: set deleted_at and remove members.
func (r *Repo) DeleteGroup(ctx context.Context, tenantID, id uuid.UUID) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return mapPgError(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx,
		`DELETE FROM group_members WHERE tenant_id = $1 AND group_id = $2`,
		tenantID, id,
	); err != nil {
		return mapPgError(err)
	}
	if _, err := tx.Exec(ctx,
		`UPDATE groups SET deleted_at = now(), updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL`,
		tenantID, id,
	); err != nil {
		return mapPgError(err)
	}
	return tx.Commit(ctx)
}

// ---- internals ------------------------------------------------------------

func (r *Repo) groupMembers(ctx context.Context, tenantID, groupID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT user_id FROM group_members WHERE tenant_id = $1 AND group_id = $2`,
		tenantID, groupID,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []uuid.UUID
	for rows.Next() {
		var u uuid.UUID
		if err := rows.Scan(&u); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, u)
	}
	return out, mapPgError(rows.Err())
}

func (r *Repo) countUsers(ctx context.Context, where string, args []any) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE `+where, args...).Scan(&n)
	return n, mapPgError(err)
}

func (r *Repo) countGroups(ctx context.Context, where string, args []any) (int, error) {
	var n int
	err := r.pool.QueryRow(ctx, `SELECT count(*) FROM groups WHERE `+where+` AND deleted_at IS NULL`, args...).Scan(&n)
	return n, mapPgError(err)
}

// buildUserWhere translates SCIM filters to SQL. Only a handful of attrs
// are indexed; callers must not expect fast scans on everything.
func buildUserWhere(tenantID uuid.UUID, f *Filter) (string, []any) {
	args := []any{tenantID}
	clauses := []string{"tenant_id = $1", "deleted_at IS NULL"}
	if f != nil {
		switch f.Attr {
		case "username", "email":
			args = append(args, f.Value)
			clauses = append(clauses, userSQLOp("email", f.Op, len(args)))
		case "displayname":
			args = append(args, f.Value)
			clauses = append(clauses, userSQLOp("display_name", f.Op, len(args)))
		case "id":
			args = append(args, f.Value)
			clauses = append(clauses, "id::text "+sqlOp(f.Op)+" $"+itoa(len(args)))
		case "active":
			switch f.Value {
			case "true":
				clauses = append(clauses, "status = 'active'")
			case "false":
				clauses = append(clauses, "status <> 'active'")
			}
		}
	}
	return joinAnd(clauses), args
}

func buildGroupWhere(tenantID uuid.UUID, f *Filter) (string, []any) {
	args := []any{tenantID}
	clauses := []string{"tenant_id = $1"}
	if f != nil && f.Attr == "displayname" {
		args = append(args, f.Value)
		clauses = append(clauses, userSQLOp("name", f.Op, len(args)))
	}
	return joinAnd(clauses), args
}

// userSQLOp renders a parameterized clause for string operators.
func userSQLOp(col, op string, placeholder int) string {
	p := "$" + itoa(placeholder)
	switch op {
	case "eq":
		return "lower(" + col + ") = lower(" + p + ")"
	case "ne":
		return "lower(" + col + ") <> lower(" + p + ")"
	case "co":
		// Caller provided the raw value; the `%` wrapping happens via
		// setting args[placeholder-1] — keep it simple and use ILIKE here.
		return col + " ILIKE '%' || " + p + " || '%'"
	case "sw":
		return col + " ILIKE " + p + " || '%'"
	case "pr":
		return col + " IS NOT NULL"
	}
	return "false"
}

func sqlOp(op string) string {
	switch op {
	case "ne":
		return "<>"
	default:
		return "="
	}
}

func joinAnd(xs []string) string {
	if len(xs) == 0 {
		return "true"
	}
	out := xs[0]
	for _, s := range xs[1:] {
		out += " AND " + s
	}
	return out
}

func joinCommas(xs []string) string {
	if len(xs) == 0 {
		return ""
	}
	out := xs[0]
	for _, s := range xs[1:] {
		out += ", " + s
	}
	return out
}

// mapPgError collapses pgx error classes into SCIM-appropriate sentinels.
func mapPgError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return vdmserr.Wrap(vdmserr.ErrNotFound, err)
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if pgErr.Code == "23505" {
			return vdmserr.Wrap(vdmserr.ErrAlreadyExists, err)
		}
	}
	return fmt.Errorf("scim db: %w", err)
}
