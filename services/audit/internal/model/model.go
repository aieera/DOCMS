// Package model holds internal domain types for the audit service.
package model

import "time"

// AuditEvent is one row in audit_events. Append-only, hash-chained.
type AuditEvent struct {
	ID            string    `json:"id"`
	TenantID      string    `json:"tenant_id"`
	EventHash     string    `json:"event_hash"`
	PreviousHash  string    `json:"previous_hash"`
	Actor         string    `json:"actor"`
	ActorName     string    `json:"actor_name,omitempty"`
	Action        string    `json:"action"`
	ResourceType  string    `json:"resource_type"`
	ResourceID    string    `json:"resource_id"`
	ResourceTitle string    `json:"resource_title,omitempty"`
	Details       []byte    `json:"details,omitempty"`
	IPAddress     string    `json:"ip_address,omitempty"`
	UserAgent     string    `json:"user_agent,omitempty"`
	SourceEvent   string    `json:"source_event,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ListFilter specifies query parameters for listing audit events.
type ListFilter struct {
	TenantID     string
	Actor        string
	Action       string
	ResourceType string
	ResourceID   string
	DateFrom     *time.Time
	DateTo       *time.Time
	PageSize     int
	PageToken    string // base64-encoded (created_at, id) cursor
}

// IntegrityResult is returned by VerifyIntegrity.
type IntegrityResult struct {
	TenantID     string `json:"tenant_id"`
	TotalEvents  int64  `json:"total_events"`
	Verified     int64  `json:"verified"`
	BrokenAt     string `json:"broken_at,omitempty"`
	BrokenHash   string `json:"broken_hash,omitempty"`
	ExpectedHash string `json:"expected_hash,omitempty"`
	Valid        bool   `json:"valid"`
}

// DataSubjectExport is the GDPR Art.15 response.
type DataSubjectExport struct {
	SubjectID string        `json:"subject_id"`
	Events    []*AuditEvent `json:"events"`
	ExportedAt time.Time    `json:"exported_at"`
}
