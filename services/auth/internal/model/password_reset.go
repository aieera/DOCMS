package model

import (
	"time"

	"github.com/google/uuid"
)

// PasswordResetToken is a single-use, time-boxed credential for the
// forgot/reset-password flow (audit-2026-05 Track 2). Only the SHA-256 hash of
// the opaque token is stored; this struct is the lookup result.
type PasswordResetToken struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	ExpiresAt time.Time
	UsedAt    *time.Time
}
