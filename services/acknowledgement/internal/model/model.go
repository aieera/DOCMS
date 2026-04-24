// Package model holds the acknowledgement service's internal domain types.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Status is the campaign lifecycle.
type Status string

const (
	StatusDraft    Status = "draft"
	StatusActive   Status = "active"
	StatusClosed   Status = "closed"
	StatusArchived Status = "archived"
)

// RecipientPolicy declares who receives assignments when a campaign
// transitions draft → active. Resolution (groups/roles → user ids)
// happens in the service layer on activation, not lazily at send
// time — so the assignment list is stable even if group membership
// shifts mid-campaign.
type RecipientPolicy struct {
	Groups []uuid.UUID `json:"groups,omitempty"`
	Users  []uuid.UUID `json:"users,omitempty"`
	Roles  []string    `json:"roles,omitempty"`
}

// Campaign mirrors acknowledgement_campaigns.
type Campaign struct {
	TenantID        uuid.UUID
	ID              uuid.UUID
	DocumentID      uuid.UUID
	VersionID       *uuid.UUID
	Title           string
	BodyMD          string
	DueAt           time.Time
	CreatedByUserID uuid.UUID
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
	Status          Status
	RecipientPolicy RecipientPolicy
}

// Assignment mirrors acknowledgement_assignments.
type Assignment struct {
	TenantID        uuid.UUID
	ID              uuid.UUID
	CampaignID      uuid.UUID
	AssigneeUserID  uuid.UUID
	AssignedAt      time.Time
	RemindedCount   int
	RemindedAt      *time.Time
	AcknowledgedAt  *time.Time
	IPAddress       string // inet; empty when not yet acknowledged
	UserAgent       string
	Comment         string
	AttestationHash []byte // HMAC-SHA256
	EscalatedAt     *time.Time
}

// Event mirrors acknowledgement_events — append-only hash chain per
// campaign so an exported bundle is offline-verifiable.
type Event struct {
	TenantID     uuid.UUID
	ID           uuid.UUID
	CampaignID   uuid.UUID
	AssignmentID *uuid.UUID
	ActorUserID  *uuid.UUID
	EventType    string
	Payload      []byte
	PrevHash     []byte
	SelfHash     []byte
	CreatedAt    time.Time
}

// Report is the per-campaign rollup surfaced by GET /campaigns/{id}/report.
type Report struct {
	CampaignID     uuid.UUID `json:"campaign_id"`
	Total          int       `json:"total"`
	Acknowledged   int       `json:"acknowledged"`
	Overdue        int       `json:"overdue"`
	Escalated      int       `json:"escalated"`
	AckRate        float64   `json:"acknowledgement_rate"`
	DueAt          time.Time `json:"due_at"`
	SignedExportURL string   `json:"signed_export_url,omitempty"`
}

// ChainHead is the tuple returned by the repo so the service can
// link the next event. An empty chain has PrevHash=nil.
type ChainHead struct {
	SelfHash []byte
}
