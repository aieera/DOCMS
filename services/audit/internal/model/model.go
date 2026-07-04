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
	// Checkpoint verification (Ed25519). Populated when a signed
	// checkpoint exists for the tenant. CheckpointValid is true only when
	// the latest checkpoint's signature verifies AND the recomputed chain
	// head at its event_count matches its signed head_hash.
	CheckpointValid   bool   `json:"checkpoint_valid"`
	CheckpointAt      string `json:"checkpoint_at,omitempty"`
	CheckpointCount   int64  `json:"checkpoint_event_count,omitempty"`
	CheckpointMessage string `json:"checkpoint_message,omitempty"`
}

// MerkleProof is a range-scoped integrity proof over a set of audit events
// (e.g. all events for one document). The leaves are the per-event hashes; the
// root anchors the whole set, so any altered event changes the root. Valid is
// false (with BrokenAt) when a leaf's recomputed hash doesn't match the stored
// EventHash — i.e. the event's content was tampered.
type MerkleProof struct {
	TenantID     string     `json:"tenant_id"`
	ResourceType string     `json:"resource_type,omitempty"`
	ResourceID   string     `json:"resource_id,omitempty"`
	Count        int        `json:"count"`
	RootHash     string     `json:"root_hash"`
	FirstEventID string     `json:"first_event_id,omitempty"`
	LastEventID  string     `json:"last_event_id,omitempty"`
	FirstAt      *time.Time `json:"first_at,omitempty"`
	LastAt       *time.Time `json:"last_at,omitempty"`
	Valid        bool       `json:"valid"`
	BrokenAt     string     `json:"broken_at,omitempty"`
	BrokenHash   string     `json:"broken_hash,omitempty"`
	ExpectedHash string     `json:"expected_hash,omitempty"`
	// LeafHashes are the ordered per-event hashes (the Merkle leaves) so an
	// external verifier can recompute the root independently.
	LeafHashes []string `json:"leaf_hashes,omitempty"`
}

// AuditCheckpoint is one signed assertion that the first EventCount audit
// events hashed to HeadHash at CreatedAt. The signature is over a
// canonical message of (tenant_id, head_hash, event_count) — see
// service.CanonicalCheckpointMessage — so an external verifier with the
// published public key can confirm it independently.
type AuditCheckpoint struct {
	TenantID   string    `json:"tenant_id"`
	ID         string    `json:"id"`
	CreatedAt  time.Time `json:"created_at"`
	HeadHash   string    `json:"head_hash"`
	EventCount int64     `json:"event_count"`
	Algo       string    `json:"algo"`
	KeyID      string    `json:"key_id"`
	Signature  string    `json:"signature"` // base64
}

// DataSubjectExport is the GDPR Art.15 response.
type DataSubjectExport struct {
	SubjectID  string        `json:"subject_id"`
	Events     []*AuditEvent `json:"events"`
	ExportedAt time.Time     `json:"exported_at"`
}
