// ADR 0061 — WebAuthn data model.
//
// Mirrors the shape of webauthn_credentials + step_up_grants in
// services/document/migrations/000027_webauthn.up.sql.
package model

import (
	"time"

	"github.com/google/uuid"
)

// WebAuthnCredential is one registered authenticator.
type WebAuthnCredential struct {
	TenantID       uuid.UUID
	UserID         uuid.UUID
	CredentialID   []byte // BYTEA; the authenticator-chosen unique key
	PublicKey      []byte // BYTEA, COSE format
	SignCount      int64
	AAGUID         []byte // BYTEA; nullable in DB
	Transports     []string
	Name           string // user-supplied friendly name; required
	BackupEligible bool
	BackupState    bool
	CreatedAt      time.Time
	LastUsedAt     *time.Time // nil until first use
}

// StepUpGrant is one fresh-presence window. The middleware looks up
// (tenant, user) rows where expires_at > now() and (scope=” OR
// scope=requested_scope).
type StepUpGrant struct {
	TenantID     uuid.UUID
	UserID       uuid.UUID
	Scope        string // '' = all sensitive ops
	GrantedVia   string // 'webauthn' today; future 'totp_fresh', 'sso_recent'
	CredentialID []byte // FK back to webauthn_credentials when via=webauthn; nil otherwise
	GrantedAt    time.Time
	ExpiresAt    time.Time
}
