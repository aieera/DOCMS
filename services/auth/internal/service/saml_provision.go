package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/model"
)

// FindOrCreateSAMLUser is called by the SSO layer after it has validated
// a SAML assertion. It looks the user up by (tenant_id, email) and either:
//   - returns the existing row (JIT refresh of display_name), or
//   - creates a new one with role=member, status=active, NO password (the
//     user is SSO-only and can never log in with a password unless an admin
//     later sets one).
//
// In both cases a fresh session is created. Returns (session_token,
// expires_at). Group sync lands in Phase A2.1.
func (s *Service) FindOrCreateSAMLUser(ctx context.Context, tenantID uuid.UUID, email, displayName string, _ []string, ip, ua string) (string, time.Time, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		return "", time.Time{}, vdmserr.Validation("email", "required")
	}
	if displayName == "" {
		displayName = emailLocalPart(email)
	}

	var (
		user       *model.User
		sessionTok string
		expiresAt  time.Time
	)

	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// Find existing user.
		existing, err := s.users.GetByEmail(ctx, tx, tenantID, email)
		switch {
		case err == nil && existing != nil:
			user = existing
			if user.Status != model.StatusActive {
				return vdmserr.ErrUnauthorized
			}
			// Keep display_name in sync with the IdP.
			if displayName != "" && displayName != user.DisplayName {
				if _, err := tx.Exec(ctx,
					`UPDATE users SET display_name = $3, updated_at = now()
					 WHERE tenant_id = $1 AND id = $2`,
					tenantID, user.ID, displayName,
				); err != nil {
					return err
				}
				user.DisplayName = displayName
			}
		case vdmserr.KindOf(err) == vdmserr.KindNotFound:
			// Create new user. No password (PasswordHash stays empty).
			id, err := newUUID()
			if err != nil {
				return err
			}
			user = &model.User{
				TenantID:    tenantID,
				ID:          id,
				Email:       email,
				DisplayName: displayName,
				Role:        model.RoleMember,
				Status:      model.StatusActive,
				CreatedAt:   s.clock(),
				UpdatedAt:   s.clock(),
			}
			if err := s.users.Create(ctx, tx, user); err != nil {
				return err
			}
			if err := s.emitAuth(ctx, tx, tenantID, user.ID, "dms.auth.user_registered.v1", map[string]any{
				"user_id":   user.ID.String(),
				"tenant_id": tenantID.String(),
				"email":     email,
				"method":    "saml",
			}); err != nil {
				return err
			}
		default:
			return err
		}

		// Issue a session in the same TX as login_success.
		created, err := s.createSessionInTx(ctx, tx, user, ip, ua)
		if err != nil {
			return err
		}
		sessionTok = created.Token
		expiresAt = created.Session.ExpiresAt

		if err := s.users.UpdateLastLogin(ctx, tx, tenantID, user.ID, s.clock()); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, tenantID, user.ID, "dms.auth.login_success.v1", map[string]any{
			"user_id":    user.ID.String(),
			"tenant_id":  tenantID.String(),
			"method":     "saml",
			"ip":         ip,
			"user_agent": ua,
		})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return sessionTok, expiresAt, nil
}

// emailLocalPart returns everything before @ for use as a display-name
// fallback when the IdP didn't provide one.
func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}
