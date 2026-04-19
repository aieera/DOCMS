package model

import (
	"time"

	"github.com/google/uuid"
)

// AnnotationType enumerates the four annotation kinds in the
// annotations table CHECK constraint. Keep in sync with
// services/document/migrations/000001 CREATE TABLE annotations.
const (
	AnnotationTypeHighlight = "highlight"
	AnnotationTypeNote      = "note"
	AnnotationTypeStamp     = "stamp"
	AnnotationTypeDrawing   = "drawing"
)

// Annotation is one row in `annotations`. Data is the opaque
// per-type JSON payload (coords for highlight, text+coords for note,
// ink path for drawing, etc.) — kept as `map[string]any` here so the
// service layer doesn't have to care about the internal shape.
type Annotation struct {
	TenantID     uuid.UUID      `json:"-"`
	ID           uuid.UUID      `json:"id"`
	DocumentID   uuid.UUID      `json:"document_id"`
	VersionID    uuid.UUID      `json:"version_id"`
	PageNumber   int            `json:"page_number"`
	Type         string         `json:"type"`
	Data         map[string]any `json:"data"`
	CreatedBy    uuid.UUID      `json:"created_by"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	DeletedAt    *time.Time     `json:"deleted_at,omitempty"`
}

// AnnotationCreatedPayload / Updated / Deleted — outbox event
// payloads. Wire names kept snake_case so downstream consumers
// (collaboration WS, activity feed) read them directly.
type AnnotationCreatedPayload struct {
	AnnotationID string `json:"annotation_id"`
	DocumentID   string `json:"document_id"`
	VersionID    string `json:"version_id"`
	PageNumber   int    `json:"page_number"`
	Type         string `json:"type"`
	CreatedBy    string `json:"created_by"`
}

type AnnotationUpdatedPayload struct {
	AnnotationID string `json:"annotation_id"`
	DocumentID   string `json:"document_id"`
	VersionID    string `json:"version_id"`
	UpdatedBy    string `json:"updated_by"`
}

type AnnotationDeletedPayload struct {
	AnnotationID string `json:"annotation_id"`
	DocumentID   string `json:"document_id"`
	VersionID    string `json:"version_id"`
	DeletedBy    string `json:"deleted_by"`
}

// IsValidAnnotationType is used by the service layer's validator.
func IsValidAnnotationType(t string) bool {
	switch t {
	case AnnotationTypeHighlight, AnnotationTypeNote,
		AnnotationTypeStamp, AnnotationTypeDrawing:
		return true
	}
	return false
}
