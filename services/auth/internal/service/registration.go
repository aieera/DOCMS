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

// RegisterInput is the validated shape the service consumes (handler
// assembles it from the HTTP request).
type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	TenantSlug  string
}

// Register creates a new user. It is intentionally "registration failed" for
// every user-visible error class below ErrInvalidCredentials semantics —
// we only surface specific field validation errors for obvious cases
// (malformed email, weak password). "Email already exists" is disguised
// as a generic failure per spec #3 of the security rules.
func (s *Service) Register(ctx context.Context, in RegisterInput) (*model.User, error) {
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

	// Tenant lookup is outside any tenant-scoped transaction.
	org, err := s.users.FindOrganizationBySlug(ctx, s.pool, in.TenantSlug)
	if err != nil || org == nil {
		return nil, vdmserr.Validation("tenant_slug", "unknown tenant")
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
		TenantID:     org.ID,
		ID:           userID,
		Email:        email,
		DisplayName:  displayName,
		PasswordHash: hash,
		Role:         model.RoleMember,
		Status:       model.StatusActive,
		CreatedAt:    s.clock(),
		UpdatedAt:    s.clock(),
	}

	err = database.WithTenantTx(ctx, s.pool, org.ID, func(tx pgx.Tx) error {
		// Duplicate-email check before insert. RLS ensures we only hit this
		// tenant's users table. The UNIQUE(tenant_id, email) index would
		// also catch this, but checking first lets us emit a stable error
		// without losing the hash cost to wasted inserts.
		existing, err := s.users.GetByEmail(ctx, tx, org.ID, email)
		if err == nil && existing != nil {
			// Spec: never say "email already registered" on the wire.
			// The service returns ErrRegistrationFailed; handlers map it
			// to a generic message.
			s.auditLoginFailure(ctx, tx, org.ID, email, "duplicate_email")
			return errRegistrationFailed
		}
		if err != nil && vdmserr.KindOf(err) != vdmserr.KindNotFound {
			return err
		}
		if err := s.users.Create(ctx, tx, user); err != nil {
			return err
		}
		return s.emitAuth(ctx, tx, org.ID, userID, "dms.auth.user_registered.v1", map[string]any{
			"user_id":   userID.String(),
			"tenant_id": org.ID.String(),
			"email":     email, // email is not secret; we store it on the user row anyway
		})
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}

// errRegistrationFailed is the generic error emitted for every case that
// shouldn't be distinguishable on the wire. Handlers translate it to a
// uniform 4xx message.
var errRegistrationFailed = vdmserr.Conflict("registration failed, please contact support")

// auditLoginFailure inserts a dms.auth.login_failed.v1 audit event.
// NOTE: safe to call with unknown-user emails — we do NOT include the
// password or the hash, ever.
func (s *Service) auditLoginFailure(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, email, reason string) {
	payload, _ := json.Marshal(map[string]any{
		"tenant_id": tenantID.String(),
		"email":     email,
		"reason":    reason,
		"at":        s.clock().Format(time.RFC3339),
	})
	evt := database.NewOutboxEvent(tenantID, "dms.auth.login_failed.v1", "auth", uuid.New(), payload)
	// Best-effort; do not fail the parent TX if audit insert fails.
	if err := s.outbox.Insert(ctx, tx, evt); err != nil {
		s.log.Warn().Err(err).Msg("audit login_failed insert")
	}
}

// emitAuth is the happy-path audit/event emitter. Returns the insert error
// so the caller can propagate it and roll back.
func (s *Service) emitAuth(ctx context.Context, tx pgx.Tx, tenantID, aggregateID uuid.UUID, eventType string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	evt := database.NewOutboxEvent(tenantID, eventType, "auth", aggregateID, body)
	return s.outbox.Insert(ctx, tx, evt)
}
