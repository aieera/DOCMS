// Package model holds the policy service's domain types.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Capability is one of the five verbs in the hierarchy
// admin > delete > edit > share > view.
type Capability string

const (
	CapView   Capability = "view"
	CapShare  Capability = "share"
	CapEdit   Capability = "edit"
	CapDelete Capability = "delete"
	CapAdmin  Capability = "admin"
)

// ResourceType — what the permission is ON.
type ResourceType string

const (
	ResDocument  ResourceType = "document"
	ResFolder    ResourceType = "folder"
	ResWorkspace ResourceType = "workspace"
)

// PrincipalType — who the permission is FOR.
type PrincipalType string

const (
	PrincUser  PrincipalType = "user"
	PrincGroup PrincipalType = "group"
)

// Effect — allow / deny. Today we only persist "allow" rows; deny is
// policy-level (legal hold, disposed state, deactivated user) evaluated
// in Rego.
type Effect string

const EffectAllow Effect = "allow"

// Permission mirrors one row in the permissions table.
type Permission struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	ResourceType  ResourceType
	ResourceID    uuid.UUID
	PrincipalType PrincipalType
	PrincipalID   uuid.UUID
	Capability    Capability
	Effect        Effect
	GrantedBy     uuid.UUID
	GrantedAt     time.Time
	ValidFrom     time.Time
	ValidTo       *time.Time
	ExpiresAt     *time.Time
}

func (p *Permission) IsActive(at time.Time) bool {
	if p.ValidTo != nil && !at.Before(*p.ValidTo) {
		return false
	}
	if p.ExpiresAt != nil && !at.Before(*p.ExpiresAt) {
		return false
	}
	if at.Before(p.ValidFrom) {
		return false
	}
	return true
}

// WorkspaceMember is the minimum shape needed for the workspace-admin rule.
type WorkspaceMember struct {
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	UserID      uuid.UUID
	Role        string // "admin" | "member" | "viewer"
}

// CheckInput is the ABAC input fed into Rego.
type CheckInput struct {
	SubjectType  string            `json:"subject_type"`
	SubjectID    string            `json:"subject_id"`
	Action       string            `json:"action"`
	ResourceType string            `json:"resource_type"`
	ResourceID   string            `json:"resource_id"`
	Context      map[string]string `json:"context"`
}

// CheckResult is what Rego evaluation returns.
type CheckResult struct {
	Allowed bool
	Reason  string
}
