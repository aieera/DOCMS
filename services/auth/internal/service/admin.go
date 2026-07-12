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

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
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

// SeatUsage returns the tenant's seat-consuming user count — the same
// active/non-deleted set enforceSeatLimit gates user creation on — so
// the License page can show live "seats in use" next to the JWT's
// seat_limit claim (ADR 0095 Phase 4).
func (s *Service) SeatUsage(ctx context.Context, tenantID uuid.UUID) (int, error) {
	var used int
	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		n, err := s.users.CountActive(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		used = n
		return nil
	})
	if err != nil {
		return 0, err
	}
	return used, nil
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
				u         model.User
				role, st  string
				lastLogin *time.Time
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
			"invite_token_hash": tokenHash,
			"invite_expires_at": expiresAt.Format(time.RFC3339),
			"invited_by":        in.InvitedBy.String(),
		},
		CreatedAt: s.clock(),
		UpdatedAt: s.clock(),
	}

	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if existing, err := s.users.GetByEmail(ctx, tx, in.TenantID, email); err == nil && existing != nil {
			return vdmserr.Conflict("user with this email already exists")
		}
		if err := s.enforceSeatLimit(ctx, tx, in.TenantID); err != nil {
			return err
		}
		if err := s.users.Create(ctx, tx, user); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":           user.ID.String(),
			"tenant_id":         user.TenantID.String(),
			"email":             user.Email,
			"display_name":      user.DisplayName,
			"role":              string(user.Role),
			"invited_by":        in.InvitedBy.String(),
			"invite_token_hash": tokenHash,
			"invite_expires_at": expiresAt.Format(time.RFC3339),
		})
		evt := database.NewOutboxEvent(in.TenantID, "dms.user.invited.v1", "user", user.ID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, "", err
	}
	return user, plaintextToken, nil
}

// ---- A) Direct create-with-password ---------------------------------------

// CreateUserDirectInput is the validated shape consumed by CreateUserAdmin.
type CreateUserDirectInput struct {
	Email       string
	Password    string
	DisplayName string
	Role        string
	TenantID    uuid.UUID
	CreatedBy   uuid.UUID
}

// CreateUserAdmin creates an active user with a known password — bypasses
// the email-invite round trip. Useful for dev/demo bootstrap and for
// air-gapped tenants. Validation rules match Register so the password
// policy stays consistent across surfaces.
func (s *Service) CreateUserAdmin(ctx context.Context, in CreateUserDirectInput) (*model.User, error) {
	email, err := validateEmail(in.Email)
	if err != nil {
		return nil, err
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	displayName, err := validateDisplayName(in.DisplayName)
	if err != nil {
		return nil, err
	}
	role := model.Role(in.Role)
	switch role {
	case model.RoleAdmin, model.RoleMember, model.RoleGuest:
	case model.RoleOwner:
		return nil, vdmserr.Validation("role", "cannot create owners via this endpoint")
	default:
		return nil, vdmserr.Validation("role", "unsupported")
	}
	hash, err := bcryptHash(in.Password)
	if err != nil {
		return nil, err
	}
	userID, err := newUUID()
	if err != nil {
		return nil, err
	}
	user := &model.User{
		TenantID:     in.TenantID,
		ID:           userID,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		Role:         role,
		Status:       model.StatusActive,
		Settings: map[string]any{
			"created_by": in.CreatedBy.String(),
		},
		CreatedAt: s.clock(),
		UpdatedAt: s.clock(),
	}
	err = database.WithTenantTx(ctx, s.pool, in.TenantID, func(tx pgx.Tx) error {
		if existing, gErr := s.users.GetByEmail(ctx, tx, in.TenantID, email); gErr == nil && existing != nil {
			return vdmserr.Conflict("user with this email already exists")
		}
		if seErr := s.enforceSeatLimit(ctx, tx, in.TenantID); seErr != nil {
			return seErr
		}
		if cErr := s.users.Create(ctx, tx, user); cErr != nil {
			return cErr
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":      user.ID.String(),
			"tenant_id":    user.TenantID.String(),
			"email":        user.Email,
			"display_name": user.DisplayName,
			"role":         string(user.Role),
			"created_by":   in.CreatedBy.String(),
		})
		evt := database.NewOutboxEvent(in.TenantID, "dms.user.created.v1", "user", user.ID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// ---- B) Accept invite -----------------------------------------------------

// AcceptInviteInput carries everything the public accept-invite handler
// needs. Tenant slug travels in the URL alongside the token because the
// invitee has no session yet (and we don't trust client headers).
type AcceptInviteInput struct {
	TenantSlug string
	Token      string
	Password   string
}

// AcceptInvite consumes the plaintext invite token: looks up the user
// in the named tenant by token-hash, sets the password, and clears the
// invite metadata. Returns ErrNotFound on bad/expired tokens (the wire
// message is intentionally generic to avoid token-existence oracles).
func (s *Service) AcceptInvite(ctx context.Context, in AcceptInviteInput) (*model.User, error) {
	if in.Token == "" {
		return nil, vdmserr.Validation("token", "required")
	}
	if err := validatePassword(in.Password); err != nil {
		return nil, err
	}
	org, err := s.users.FindOrganizationBySlug(ctx, s.pool, in.TenantSlug)
	if err != nil || org == nil {
		return nil, vdmserr.Validation("tenant_slug", "unknown tenant")
	}
	tokenHash := sha256Hex(in.Token)
	hash, err := bcryptHash(in.Password)
	if err != nil {
		return nil, err
	}
	var out *model.User
	err = database.WithTenantTx(ctx, s.pool, org.ID, func(tx pgx.Tx) error {
		u, gErr := s.users.GetByInviteTokenHash(ctx, tx, org.ID, tokenHash)
		if gErr != nil {
			if vdmserr.KindOf(gErr) == vdmserr.KindNotFound {
				return errInviteFailed
			}
			return gErr
		}
		// Expiry check.
		if exp, ok := u.Settings["invite_expires_at"].(string); ok {
			if t, perr := time.Parse(time.RFC3339, exp); perr == nil && s.clock().After(t) {
				return errInviteFailed
			}
		}
		if sErr := s.users.SetPasswordHash(ctx, tx, org.ID, u.ID, hash); sErr != nil {
			return sErr
		}
		if cErr := s.users.ClearInviteToken(ctx, tx, org.ID, u.ID); cErr != nil {
			return cErr
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":   u.ID.String(),
			"tenant_id": org.ID.String(),
			"email":     u.Email,
		})
		evt := database.NewOutboxEvent(org.ID, "dms.user.invite_accepted.v1", "user", u.ID, payload)
		if oErr := s.outbox.Insert(ctx, tx, evt); oErr != nil {
			return oErr
		}
		out = u
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// errInviteFailed is the generic "bad/expired token" surface — same
// message regardless of cause so we don't leak whether a token exists.
var errInviteFailed = vdmserr.Validation("token", "invitation invalid or expired")

// OrgSlugByID is a thin wrapper used by the admin handler to enrich the
// invite response with the tenant slug (so the frontend can build the
// activation URL). Returns "" on lookup failure rather than propagating
// because the slug is non-essential — the invite itself still succeeded.
func (s *Service) OrgSlugByID(ctx context.Context, id uuid.UUID) (string, error) {
	org, err := s.users.GetOrganizationByID(ctx, s.pool, id)
	if err != nil || org == nil {
		return "", err
	}
	return org.Slug, nil
}

// ChangeUserRole sets a user's role (owner / admin / member / viewer).
// Enforces three invariants:
//   - role must be one of the allowed values (CHECK constraint catches
//     stray values too, but we surface a friendlier 400 here).
//   - actor can't change their own role (prevents an owner accidentally
//     demoting themselves out of admin access).
//   - the last owner can't be demoted (locks tenant out otherwise).
//
// Emits dms.user.role_changed.v1 with the before/after pair.
func (s *Service) ChangeUserRole(ctx context.Context, tenantID, actorID, userID uuid.UUID, role string) error {
	switch role {
	case "owner", "admin", "member", "viewer", "compliance_officer":
	default:
		return vdmserr.Validation("role", "must be owner / admin / member / viewer / compliance_officer")
	}
	if userID == actorID {
		return vdmserr.Validation("user_id", "cannot change your own role")
	}
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		// Refuse to demote the last owner — otherwise tenant locks out.
		if string(u.Role) == "owner" && role != "owner" {
			n, err := s.users.CountOwners(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			if n <= 1 {
				return vdmserr.Validation("role", "cannot demote the last remaining owner")
			}
		}
		if string(u.Role) == role {
			return nil // no-op
		}
		if err := s.users.SetRole(ctx, tx, tenantID, userID, role); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":    userID.String(),
			"tenant_id":  tenantID.String(),
			"email":      u.Email,
			"old_role":   u.Role,
			"new_role":   role,
			"changed_by": actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.role_changed.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// SuspendUser flips the user's status to "suspended", revokes every active
// session, and emits dms.user.suspended.v1.
func (s *Service) SuspendUser(ctx context.Context, tenantID, actorID, userID uuid.UUID) error {
	if userID == actorID {
		return vdmserr.Validation("user_id", "cannot suspend yourself")
	}
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
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
		// A suspended user's programmatic access must die with their
		// sessions — ValidateAPIKey checks the key's revoked_at but not
		// the owner's status, so an un-revoked key would keep working.
		if _, err := s.apiKeys.RevokeAllForUser(ctx, tx, tenantID, userID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":      userID.String(),
			"tenant_id":    tenantID.String(),
			"email":        u.Email,
			"suspended_by": actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.suspended.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	}); err != nil {
		return err
	}
	// Drop the user's cached sessions so the suspension is enforced
	// within seconds rather than at TTL expiry.
	s.invalidateUserSessions(ctx, tenantID, userID)
	return nil
}

// ReactivateUser is the inverse of SuspendUser: flips status back to
// "active" so the user can log in again. Refuses on a user who is
// already active OR has been hard-deactivated (status="deactivated") —
// the latter is reserved for compliance-driven removals (GDPR erase)
// where a soft reactivate would skip an audit-meaningful path. Emits
// dms.user.reactivated.v1; sessions stay revoked (the user must
// re-authenticate).
func (s *Service) ReactivateUser(ctx context.Context, tenantID, actorID, userID uuid.UUID) error {
	return database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, err := s.users.GetByID(ctx, tx, tenantID, userID)
		if err != nil {
			return err
		}
		if err := validateReactivableStatus(u.Status); err != nil {
			return err
		}
		if err := s.users.SetStatus(ctx, tx, tenantID, userID, model.StatusActive); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]any{
			"user_id":        userID.String(),
			"tenant_id":      tenantID.String(),
			"email":          u.Email,
			"reactivated_by": actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.reactivated.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	})
}

// ResetUserMFA clears the stored MFA secret + recovery codes and disables
// the user's MFA flag. The user is forced through MFA re-enrollment on
// next login. Active sessions are revoked so any already-authenticated
// browser drops to the login screen.
func (s *Service) ResetUserMFA(ctx context.Context, tenantID, actorID, userID uuid.UUID) error {
	if err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
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
			"user_id":   userID.String(),
			"tenant_id": tenantID.String(),
			"email":     u.Email,
			"reset_by":  actorID.String(),
		})
		evt := database.NewOutboxEvent(tenantID, "dms.user.mfa_reset.v1", "user", userID, payload)
		return s.outbox.Insert(ctx, tx, evt)
	}); err != nil {
		return err
	}
	// Enforce the "drop to login screen" intent within seconds.
	s.invalidateUserSessions(ctx, tenantID, userID)
	return nil
}

// ---- small internals ------------------------------------------------------

// validateReactivableStatus reports whether a user in the given state
// can transition back to active via ReactivateUser. Only StatusSuspended
// is allowed: StatusActive is a no-op (rejected so the audit event
// doesn't fire spuriously), and any other state (notably
// StatusDeactivated which is reserved for compliance-driven erasures)
// must go through a dedicated restore path.
func validateReactivableStatus(s model.Status) error {
	switch s {
	case model.StatusActive:
		return vdmserr.Validation("user_id", "user is already active")
	case model.StatusSuspended:
		return nil
	default:
		return vdmserr.Validation("user_id", "user cannot be reactivated from current state")
	}
}

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
