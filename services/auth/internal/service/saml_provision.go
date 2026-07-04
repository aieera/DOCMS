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
	"github.com/aieera/sedoc/services/auth/internal/sso"
)

// FindOrCreateSAMLUser is called by the SSO layer (SAML or OIDC) after it
// has validated an assertion. Resolution is SUBJECT-FIRST to prevent
// account takeover via email reassignment (blueprint §24.1):
//
//  1. (tenant, provider, subject) → user. The IdP subject (OIDC `sub` /
//     SAML NameID) is immutable, so this binding is authoritative and
//     survives the IdP changing the user's email.
//  2. Only when no subject link exists do we fall back to matching by
//     email — the first-time link. If that email already belongs to a
//     user linked to a DIFFERENT subject, we refuse (takeover guard).
//  3. New users are created SSO-only (no password) with role=member.
//
// `subject` may be empty for legacy IdPs that send no stable id; the
// flow then degrades to the old email-only behavior (less secure, but
// keeps those IdPs working). A fresh session is created in all cases.
func (s *Service) FindOrCreateSAMLUser(ctx context.Context, tenantID uuid.UUID, provider, subject, email, displayName string, groups []string, ip, ua string) (string, time.Time, error) {
	provider = strings.TrimSpace(provider)
	subject = strings.TrimSpace(subject)
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" && subject == "" {
		return "", time.Time{}, vdmserr.Validation("identity", "email or subject required")
	}
	if displayName == "" {
		displayName = emailLocalPart(email)
	}
	hasSubject := subject != "" && provider != ""

	// Resolve the JIT role from the IdP's group/role claim → role mapping.
	// Applied only when creating a new user (below); existing users keep
	// their role so an admin's manual promotion isn't clobbered on login.
	jitRole := s.resolveSSORole(ctx, tenantID, provider, groups)

	var (
		user       *model.User
		sessionTok string
		expiresAt  time.Time
	)

	err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		// 1. Subject-first: an existing link is the authoritative user.
		if hasSubject {
			var uid uuid.UUID
			switch err := tx.QueryRow(ctx,
				`SELECT user_id FROM sso_identities
				 WHERE tenant_id = $1 AND provider = $2 AND subject = $3`,
				tenantID, provider, subject).Scan(&uid); err {
			case nil:
				u, gErr := s.users.GetByID(ctx, tx, tenantID, uid)
				if gErr != nil {
					return gErr
				}
				user = u
			case pgx.ErrNoRows:
				// fall through to email matching
			default:
				return err
			}
		}

		// 2. No subject link → match by email (first-time link / legacy).
		if user == nil {
			existing, err := s.users.GetByEmail(ctx, tx, tenantID, email)
			switch {
			case err == nil && existing != nil:
				// Takeover guard: refuse if this email already maps to a
				// user bound to a different subject for this provider.
				if hasSubject {
					var linked string
					switch qErr := tx.QueryRow(ctx,
						`SELECT subject FROM sso_identities
						 WHERE tenant_id = $1 AND provider = $2 AND user_id = $3`,
						tenantID, provider, existing.ID).Scan(&linked); qErr {
					case nil:
						if linked != subject {
							return vdmserr.ErrUnauthorized
						}
					case pgx.ErrNoRows:
						// no prior link — safe to link below
					default:
						return qErr
					}
				}
				user = existing
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
					Role:        jitRole,
					Status:      model.StatusActive,
					CreatedAt:   s.clock(),
					UpdatedAt:   s.clock(),
				}
				if err := s.enforceSeatLimit(ctx, tx, tenantID); err != nil {
					return err
				}
				if err := s.users.Create(ctx, tx, user); err != nil {
					return err
				}
				if err := s.emitAuth(ctx, tx, tenantID, user.ID, "dms.auth.user_registered.v1", map[string]any{
					"user_id":   user.ID.String(),
					"tenant_id": tenantID.String(),
					"email":     email,
					"method":    provider,
				}); err != nil {
					return err
				}
			default:
				return err
			}
		}

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

		// Persist / refresh the (tenant, provider, subject) → user link so
		// subsequent logins resolve by subject. The unique index on
		// (tenant, provider, user_id) is the DB-level twin of the takeover
		// guard above: a second subject for an already-linked user errors.
		if hasSubject {
			if _, err := tx.Exec(ctx,
				`INSERT INTO sso_identities (tenant_id, provider, subject, user_id, last_login_at)
				 VALUES ($1, $2, $3, $4, now())
				 ON CONFLICT (tenant_id, provider, subject)
				 DO UPDATE SET last_login_at = now()`,
				tenantID, provider, subject, user.ID,
			); err != nil {
				return err
			}
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
			"method":     provider,
			"ip":         ip,
			"user_agent": ua,
		})
	})
	if err != nil {
		return "", time.Time{}, err
	}
	return sessionTok, expiresAt, nil
}

// resolveSSORole loads the tenant's active IdP config for the provider and maps
// the incoming group/role claim values to a SeDoc role (JIT). Any lookup/parse
// failure fails safe to member.
func (s *Service) resolveSSORole(ctx context.Context, tenantID uuid.UUID, provider string, groups []string) model.Role {
	pt := sso.ProviderSAML
	if provider == "oidc" {
		pt = sso.ProviderOIDC
	}
	cfg, err := sso.NewConfigRepo().GetActiveByTenantProvider(ctx, s.pool, tenantID, pt)
	if err != nil || cfg == nil {
		return model.RoleMember
	}
	return model.Role(sso.ResolveRole(sso.RoleMappingFromConfig(cfg.Config), groups))
}

// emailLocalPart returns everything before @ for use as a display-name
// fallback when the IdP didn't provide one.
func emailLocalPart(email string) string {
	if i := strings.IndexByte(email, '@'); i > 0 {
		return email[:i]
	}
	return email
}
