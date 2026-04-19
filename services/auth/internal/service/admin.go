// Admin operations on users — list, invite, suspend, reset MFA.
// Called by services/auth/internal/handler/admin.go from routes gated by
// middleware.RequireRole("admin", "owner"). Each mutating op writes to the
// outbox so notifications can fan out via the platform's standard event
// bus without this package taking a direct NATS dependency.
package service

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
)

// GetUser returns a single user by id, scoped to the tenant.
func (s *Service) GetUser(ctx context.Context, tenantID, userID uuid.UUID) (*model.User, error) {
	var u *model.User
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		got, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		u = got
		return nil
	})
	if err != nil {
		return nil, err
	}
	return u, nil
}

// ListUsersFilter narrows the tenant-wide user list. Every field is optional.
type ListUsersFilter struct {
	Status string // "active" | "suspended" | "deactivated"
	Role   string // "owner" | "admin" | "member" | "guest"
	Q      string // substring match on email or display_name
	Limit  int    // clamped 1..100, default 50
	Cursor string // opaque; currently encodes the last created_at
}

// ListUsersResult is the paginated wire response.
type ListUsersResult struct {
	Users      []model.PublicView
	NextCursor string
}

// ListUsers returns a page of users in the caller's tenant. Cursor-based
// pagination on (created_at DESC, id DESC).
func (s *Service) ListUsers(ctx context.Context, tenantID uuid.UUID, f ListUsersFilter) (*ListUsersResult, error) {
	limit := f.Limit
	if limit <= 0 || limit > 100 {
		limit = 50
	}

	var (
		out  ListUsersResult
		last time.Time
	)
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Build SQL piecewise — tenant is implicit via RLS, but we still
		// constrain it explicitly because the CI static analyzer flags any
		// query on a tenant-scoped table without a tenant_id predicate.
		sql := `
			SELECT tenant_id, id, email, display_name, role, status, mfa_enabled, created_at, last_login_at
			FROM users
			WHERE tenant_id = $1 AND deleted_at IS NULL`
		args := []any{tenantID}
		next := 2

		if f.Status != "" {
			sql += " AND status = $" + itoa(next)
			args = append(args, f.Status)
			next++
		}
		if f.Role != "" {
			sql += " AND role = $" + itoa(next)
			args = append(args, f.Role)
			next++
		}
		if f.Q != "" {
			sql += " AND (email ILIKE $" + itoa(next) + " OR display_name ILIKE $" + itoa(next) + ")"
			args = append(args, "%"+f.Q+"%")
			next++
		}
		if f.Cursor != "" {
			if t, err := time.Parse(time.RFC3339Nano, f.Cursor); err == nil {
				sql += " AND created_at < $" + itoa(next)
				args = append(args, t)
				next++
			}
		}
		sql += " ORDER BY created_at DESC LIMIT $" + itoa(next)
		args = append(args, limit+1)

		rows, err := tx.Query(ctx, sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var (
				u          model.User
				role, st   string
				lastLogin  *time.Time
			)
			if err := rows.Scan(
				&u.TenantID, &u.ID, &u.Email, &u.DisplayName,
				&role, &st, &u.MFAEnabled, &u.CreatedAt, &lastLogin,
			); err != nil {
				return err
			}
			u.Role = model.Role(role)
			u.Status = model.Status(st)
			u.LastLoginAt = lastLogin
			out.Users = append(out.Users, u.ToPublic())
			last = u.CreatedAt
		}
		return rows.Err()
	})
	if err != nil {
		return nil, err
	}

	if len(out.Users) > limit {
		out.Users = out.Users[:limit]
		out.NextCursor = last.Format(time.RFC3339Nano)
	}
	return &out, nil
}

// InviteInput is the validated shape the handler passes in.
type InviteInput struct {
	Email       string
	DisplayName string
	Role        string
	TenantID    uuid.UUID
	InvitedBy   uuid.UUID
}

// InviteUser creates a placeholder user with an empty password hash, persists
// an invite token in settings.invite_token (72 h TTL), and enqueues
// user.invited.v1 on the outbox. The plaintext invite token is returned to
// the caller for inclusion in the generated email (the outbox event payload
// does NOT carry the plaintext — only the hash — to avoid leaking it through
// the event bus).
func (s *Service) InviteUser(ctx context.Context, in InviteInput) (*model.User, string, error) {
	email, err := validateEmail(in.Email)
	if err != nil {
		return nil, "", err
	}
	displayName, err := validateDisplayName(in.DisplayName)
	if err != nil {
		return nil, "", err
	}
	role := model.Role(in.Role)
	switch role {
	case model.RoleAdmin, model.RoleMember, model.RoleGuest:
	case model.RoleOwner:
		return nil, "", vdmserr.Validation("role", "cannot invite owners via this endpoint")
	default:
		return nil, "", vdmserr.Validation("role", "unsupported")
	}

	plaintextToken, err := randomToken(24) // 48 hex chars
	if err != nil {
		return nil, "", err
	}
	tokenHash := sha256Hex(plaintextToken)
	expiresAt := s.clock().Add(72 * time.Hour)

	userID, err := newUUID()
	if err != nil {
		return nil, "", err
	}
	user := &model.User{
		TenantID:    in.TenantID,
		ID:          userID,
		Email:       email,
		DisplayName: displayName,
		Role:        role,
		Status:      model.StatusActive, // status='active', empty password_hash → login still fails
		Settings: map[string]any{
			"invite_token_hash":  tokenHash,
			"invite_expires_at":  expiresAt.Format(time.RFC3339),
			"invited_by":         in.InvitedBy.String(),
		},
		CreatedAt: s.clock(),
		UpdatedAt: s.clock(),
	}

	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if existing, err := s.users.GetByEmail(ctx, tx, in.TenantID, email); err == nil && existing != nil {
			return vdmserr.Conflict("user with this email already exists")
		}
		if err := s.users.Create(ctx, tx, user); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":            user.ID.String(),
			"tenant_id":          user.TenantID.String(),
			"email":              user.Email,
			"display_name":       user.DisplayName,
			"role":               string(user.Role),
			"invited_by":         in.InvitedBy.String(),
			"invite_token_hash":  tokenHash,
			"invite_expires_at":  expiresAt.Format(time.RFC3339),
		})
		evt := database.NewOutboxEvent(in.TenantID, "dms.user.invited.v1", "user", user.ID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, "", err
	}
	return user, plaintextToken, nil
}

// SuspendUser flips the user's status to "suspended", revokes every active
// session, and emits dms.user.suspended.v1.
func (s *Service) SuspendUser(ctx context.Context, tenantID, actorID, userID uuid.UUID) error {
	if userID == actorID {
		return vdmserr.Validation("user_id", "cannot suspend yourself")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := s.users.SetStatus(ctx, tx, tenantID, userID, model.StatusSuspended); err != nil {
			return err
		}
		if _, err := s.sessions.RevokeAllForUser(ctx, tx, tenantID, userID, nil); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":     userID.String(),
			"tenant_id":   tenantID.String(),
			"email":       u.Email,
			"suspended_by": actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.suspended.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// ResetUserMFA clears the stored MFA secret + recovery codes and disables
// the user's MFA flag. The user is forced through MFA re-enrollment on
// next login. Active sessions are revoked so any already-authenticated
// browser drops to the login screen.
func (s *Service) ResetUserMFA(ctx context.Context, tenantID, actorID, userID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := s.users.ClearMFA(ctx, tx, tenantID, userID); err != nil {
			return err
		}
		if err := s.users.SetMFAEnabled(ctx, tx, tenantID, userID, false); err != nil {
			return err
		}
		if _, err := s.sessions.RevokeAllForUser(ctx, tx, tenantID, userID, nil); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":    userID.String(),
			"tenant_id":  tenantID.String(),
			"email":      u.Email,
			"reset_by":   actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.mfa_reset.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// ---- small internals ------------------------------------------------------

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

