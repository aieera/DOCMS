// Package model holds the auth service's internal domain types.
// Serialization deliberately never exposes password_hash, mfa_secret_encrypted,
// or mfa_recovery_hashes — those fields never leave the service.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Role is the global role a user has within their tenant.
type Role string

const (
	RoleOwner  Role = "owner"
	RoleAdmin  Role = "admin"
	RoleMember Role = "member"
	RoleGuest  Role = "guest"
)

// Status is the user's lifecycle state.
type Status string

const (
	StatusActive      Status = "active"
	StatusSuspended   Status = "suspended"
	StatusDeactivated Status = "deactivated"
)

// User is the auth-service domain type. The repo reads/writes; handlers
// translate to wire types (registration/login response) that exclude the
// hash/secret fields.
type User struct {
	TenantID          uuid.UUID
	ID                uuid.UUID
	Email             string
	DisplayName       string
	PasswordHash      string   // never serialized
	AvatarURL         string
	Role              Role
	Status            Status
	MFAEnabled        bool
	MFASecretEnc      string   // AES-GCM ciphertext; never serialized
	MFARecoveryHashes []string // bcrypt-hashed; never serialized
	LastLoginAt       *time.Time
	Locale            string
	Timezone          string
	Settings          map[string]any
	CreatedAt         time.Time
	UpdatedAt         time.Time
	DeletedAt         *time.Time
}

// PublicView is the safe projection for API responses.
type PublicView struct {
	ID          uuid.UUID  `json:"id"`
	TenantID    uuid.UUID  `json:"tenant_id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        Role       `json:"role"`
	Status      Status     `json:"status"`
	MFAEnabled  bool       `json:"mfa_enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

// ToPublic returns a PublicView with secret fields stripped.
func (u *User) ToPublic() PublicView {
	return PublicView{
		ID:          u.ID,
		TenantID:    u.TenantID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        u.Role,
		Status:      u.Status,
		MFAEnabled:  u.MFAEnabled,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}

// Organization is the minimum tenant projection the auth service needs.
type Organization struct {
	ID            uuid.UUID
	Slug          string
	Name          string
	PrimaryRegion string
	DeletedAt     *time.Time
}
