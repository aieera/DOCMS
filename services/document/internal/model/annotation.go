package model

import (
	"time"

	"github.com/google/uuid"
)

// AnnotationType enumerates legal values for the annotations
// table's CHECK constraint. Keep in sync with the DDL in
// services/document/migrations/000001 + migration 000032 (ADR 0067).
//
// Two generations of values coexist:
//
//   - The legacy primitives (highlight/note/stamp/drawing) shipped
//     with the initial schema. Existing rows still use these
//     values; the service treats them as `pdf_markup` at the
//     rendering layer.
//   - The §10.5 categories (pdf_markup, image_shape, video_timestamp)
//     widen the surface to images and video. New writes use these.
//
// The per-primitive shape (highlight vs underline vs strikethrough
// vs note vs drawing) lives inside `annotation_data.kind` rather
// than the table-level type, so adding a new pdf primitive is a
// frontend change with no DDL.
const (
	// Legacy.
	AnnotationTypeHighlight = "highlight"
	AnnotationTypeNote      = "note"
	AnnotationTypeStamp     = "stamp"
	AnnotationTypeDrawing   = "drawing"

	// ADR 0067 categories.
	AnnotationTypePDFMarkup      = "pdf_markup"
	AnnotationTypeImageShape     = "image_shape"
	AnnotationTypeVideoTimestamp = "video_timestamp"
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
// Accepts both legacy primitives (for back-compat with existing
// rows + the original PDF-only frontend) and the §10.5 categories.
func IsValidAnnotationType(t string) bool {
	switch t {
	case AnnotationTypeHighlight, AnnotationTypeNote,
		AnnotationTypeStamp, AnnotationTypeDrawing,
		AnnotationTypePDFMarkup, AnnotationTypeImageShape,
		AnnotationTypeVideoTimestamp:
		return true
	}
	return false
}

// AnnotationLayer maps an annotation's table-level type to the
// front-end overlay layer that renders it. Used by the rendering
// pipeline; does not appear in the wire format.
func AnnotationLayer(t string) string {
	switch t {
	case AnnotationTypeImageShape:
		return "image"
	case AnnotationTypeVideoTimestamp:
		return "video"
	default:
		// Every legacy primitive + pdf_markup renders on the PDF
		// overlay layer.
		return "pdf"
	}
}
