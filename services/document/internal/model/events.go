package model

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// OutboxEvent mirrors the `outbox` table. Services write these inside the
// same transaction as the business change; the outbox publisher ships them
// to NATS JetStream asynchronously.
type OutboxEvent struct {
	ID            uuid.UUID
	TenantID      uuid.UUID
	EventType     string // e.g. "dms.document.created.v1"
	AggregateType string // e.g. "document"
	AggregateID   uuid.UUID
	Subject       string // NATS subject, e.g. "dms.document.created"
	Payload       json.RawMessage
	CreatedAt     time.Time
}

// ---- Event payloads --------------------------------------------------------

type DocumentCreatedPayload struct {
	DocumentID  string `json:"document_id"`
	WorkspaceID string `json:"workspace_id"`
	FolderID    string `json:"folder_id"`
	Title       string `json:"title"`
	RegionPin   string `json:"region_pin"`
	CreatedBy   string `json:"created_by"`
	// FIX-4 (audit C3) — populated from publishFolderACLChange's
	// query so freshly-indexed documents land in OpenSearch with the
	// correct ACL from minute one. Without this, new docs match no
	// user's `readable_by` filter and are invisible to search until
	// the first folder-grant change retroactively rewrites them.
	ReadableBy       []string `json:"readable_by,omitempty"`
	ReadableByUsers  []string `json:"readable_by_users,omitempty"`
	ReadableByGroups []string `json:"readable_by_groups,omitempty"`
}

type DocumentUpdatedPayload struct {
	DocumentID    string   `json:"document_id"`
	ChangedFields []string `json:"changed_fields"`
	UpdatedBy     string   `json:"updated_by"`
	// FIX-4 — same projection on update. Folder moves (changed_fields
	// includes "folder_id") flip the doc into a new ACL scope; carry
	// readable_by so the indexer rewrites without a separate event.
	ReadableBy       []string `json:"readable_by,omitempty"`
	ReadableByUsers  []string `json:"readable_by_users,omitempty"`
	ReadableByGroups []string `json:"readable_by_groups,omitempty"`
}

type DocumentDeletedPayload struct {
	DocumentID string `json:"document_id"`
	DeletedBy  string `json:"deleted_by"`
	SoftDelete bool   `json:"soft_delete"`
}

type DocumentMovedPayload struct {
	DocumentID    string `json:"document_id"`
	FromFolderID  string `json:"from_folder_id"`
	ToFolderID    string `json:"to_folder_id"`
	FromWorkspace string `json:"from_workspace_id"`
	ToWorkspace   string `json:"to_workspace_id"`
	MovedBy       string `json:"moved_by"`
}

type DocumentStateChangedPayload struct {
	DocumentID string `json:"document_id"`
	FromState  string `json:"from_state"`
	ToState    string `json:"to_state"`
	Action     string `json:"action"`
	Reason     string `json:"reason,omitempty"`
	ChangedBy  string `json:"changed_by"`
}

type VersionCreatedPayload struct {
	VersionID     string `json:"version_id"`
	DocumentID    string `json:"document_id"`
	VersionNumber int    `json:"version_number"`
	ContentBlobID string `json:"content_blob_id"`
	SizeBytes     int64  `json:"size_bytes"`
	MimeType      string `json:"mime_type"`
	CreatedBy     string `json:"created_by"`
}

// VersionUploadedPayload is the JSON projection of proto
// `vaultdms.v1.VersionUploadedV1`. See ADR 0021 for the rationale
// on emitting this from the document service's CreateVersion rather
// than from storage's CompleteUpload.
type VersionUploadedPayload struct {
	EventID           string `json:"event_id"`
	TenantID          string `json:"tenant_id"`
	DocumentID        string `json:"document_id"`
	VersionID         string `json:"version_id"`
	VersionNumber     int    `json:"version_number"`
	ContentBlobID     string `json:"content_blob_id"`
	StorageURI        string `json:"storage_uri"`
	MimeType          string `json:"mime_type"`
	SizeBytes         int64  `json:"size_bytes"`
	SHA256            string `json:"sha256"`
	UploadedByUserID  string `json:"uploaded_by_user_id"`
	UploadedAt        string `json:"uploaded_at"` // RFC3339
}

type FolderCreatedPayload struct {
	FolderID    string `json:"folder_id"`
	WorkspaceID string `json:"workspace_id"`
	Path        string `json:"path"`
	Name        string `json:"name"`
	CreatedBy   string `json:"created_by"`
}

type FolderMovedPayload struct {
	FolderID string `json:"folder_id"`
	OldPath  string `json:"old_path"`
	NewPath  string `json:"new_path"`
	MovedBy  string `json:"moved_by"`
}

type ShareLinkCreatedPayload struct {
	LinkID     string `json:"link_id"`
	DocumentID string `json:"document_id"`
	CreatedBy  string `json:"created_by"`
	ExpiresAt  string `json:"expires_at,omitempty"`
}

type TagAppliedPayload struct {
	DocumentID string `json:"document_id"`
	Tag        string `json:"tag"`
	AppliedBy  string `json:"applied_by"`
}

// NewOutboxEvent marshals payload, attaches ids/timestamps, and returns a
// ready-to-insert OutboxEvent. Subject is derived from eventType by stripping
// the ".v1" suffix (e.g. "dms.document.created.v1" → "dms.document.created").
func NewOutboxEvent(
	tenantID uuid.UUID,
	eventType, aggregateType string,
	aggregateID uuid.UUID,
	payload any,
) (*OutboxEvent, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal outbox payload: %w", err)
	}
	id, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("gen outbox id: %w", err)
	}
	return &OutboxEvent{
		ID:            id,
		TenantID:      tenantID,
		EventType:     eventType,
		AggregateType: aggregateType,
		AggregateID:   aggregateID,
		Subject:       subjectFromEventType(eventType),
		Payload:       data,
		CreatedAt:     time.Now().UTC(),
	}, nil
}

func subjectFromEventType(eventType string) string {
	// "dms.document.created.v1" → "dms.document.created"
	for i := len(eventType) - 1; i >= 0; i-- {
		if eventType[i] == '.' {
			if i+1 < len(eventType) && eventType[i+1] == 'v' {
				return eventType[:i]
			}
			break
		}
	}
	return eventType
}
