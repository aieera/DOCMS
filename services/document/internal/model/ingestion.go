package model

import (
	"time"

	"github.com/google/uuid"
)

// Pre-commit ingestion pipeline (Workstream 3). An IngestionItem stages a
// blob that is already in S3 (uploaded via the storage initiate/PUT/complete
// flow) BEFORE any document or version exists. The intelligence worker OCRs +
// extracts a business key against the blob ref, then the IngestAndRoute
// Temporal workflow commits it as a new version of a matched document, a new
// document v1, or sends it to the review queue. This inverts the legacy order
// (OCR after dms.version.uploaded.v1), so a clear invoice can be auto-routed to
// the right document with no orphan staging entry.

// IngestionStatus is the lifecycle of a staged blob.
type IngestionStatus string

const (
	// IngestReceived — staged via POST /ingest; awaiting OCR.
	IngestReceived IngestionStatus = "received"
	// IngestOCRRunning — the intelligence worker picked it up.
	IngestOCRRunning IngestionStatus = "ocr_running"
	// IngestProcessed — OCR + key extraction done; awaiting routing.
	IngestProcessed IngestionStatus = "processed"
	// IngestRouted — the route workflow decided (terminal sub-states below).
	IngestRouted IngestionStatus = "routed"
	// IngestNeedsReview — low confidence / ambiguous; a review item exists.
	IngestNeedsReview IngestionStatus = "needs_review"
	// IngestCommitted — committed as a document/version (terminal).
	IngestCommitted IngestionStatus = "committed"
	// IngestRejected — discarded by a reviewer (terminal).
	IngestRejected IngestionStatus = "rejected"
)

// IsTerminal reports whether routing is done for this item. The route step and
// the processed-event consumer treat terminal items as no-ops (idempotency).
func (s IngestionStatus) IsTerminal() bool {
	return s == IngestCommitted || s == IngestRejected
}

// IngestionItem mirrors the ingestion_items row.
type IngestionItem struct {
	TenantID             uuid.UUID
	ID                   uuid.UUID
	WorkspaceID          uuid.UUID
	FolderID             *uuid.UUID
	TargetCustomerRef    string
	BlobRef              uuid.UUID // content_blobs.id
	BlobChecksum         string    // sha256 hex
	Status               IngestionStatus
	OCRResultRef         *uuid.UUID
	ExtractedExternalKey string
	MatchDocumentID      *uuid.UUID
	Confidence           float64
	DocumentClass        string
	StorageBucket        string
	StorageKey           string
	MimeType             string
	RegionPin            string
	CreatedBy            *uuid.UUID
	FailureReason        string
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

// ReviewReason classifies why a staged item was sent to review.
type ReviewReason string

const (
	ReasonBelowThreshold ReviewReason = "below_threshold"
	ReasonAmbiguousMatch ReviewReason = "ambiguous_match"
	ReasonNoExternalKey  ReviewReason = "no_external_key"
	ReasonLowOCRQuality  ReviewReason = "low_ocr_confidence"
)

// ReviewStatus is the lifecycle of a review queue item (WS4 vocabulary).
type ReviewStatus string

const (
	ReviewPending  ReviewStatus = "pending"
	ReviewResolved ReviewStatus = "resolved"
	ReviewRejected ReviewStatus = "rejected"
)

// ReviewQueueItem mirrors the review_queue_items row.
type ReviewQueueItem struct {
	TenantID                 uuid.UUID
	ID                       uuid.UUID
	IngestionItemID          uuid.UUID
	WorkspaceID              uuid.UUID
	TargetCustomerRef        string
	DocumentClass            string
	ExtractedExternalKey     string
	SuggestedMatchDocumentID *uuid.UUID
	Confidence               float64
	Reason                   ReviewReason
	Status                   ReviewStatus
	BlobRef                  uuid.UUID
	BlobChecksum             string
	OCRResultRef             *uuid.UUID
	Notes                    string
	Resolution               string
	ResultingDocumentID      *uuid.UUID
	ResultingVersionID       *uuid.UUID
	ResolvedBy               *uuid.UUID
	ResolvedAt               *time.Time
	CreatedAt                time.Time
}

// ---- Event payloads (JSON, published via the outbox) ----------------------

// IngestionReceivedPayload is the body of dms.ingestion.received.v1. The
// intelligence worker reads the storage coordinates from here to fetch + OCR
// the blob without a document-service round-trip.
type IngestionReceivedPayload struct {
	IngestionItemID   string `json:"ingestion_item_id"`
	TenantID          string `json:"tenant_id"`
	WorkspaceID       string `json:"workspace_id"`
	TargetCustomerRef string `json:"target_customer_ref"`
	ContentBlobID     string `json:"content_blob_id"`
	BlobChecksum      string `json:"blob_checksum"`
	StorageBucket     string `json:"storage_bucket"`
	StorageKey        string `json:"storage_key"`
	MimeType          string `json:"mime_type"`
	RegionPin         string `json:"region_pin"`
	DocumentClass     string `json:"document_class"`
}

// IngestionProcessedPayload is the body of dms.ingestion.processed.v1, emitted
// by the intelligence worker after OCR + key extraction. The document service
// consumer starts IngestAndRoute on it.
type IngestionProcessedPayload struct {
	IngestionItemID      string  `json:"ingestion_item_id"`
	TenantID             string  `json:"tenant_id"`
	ExtractedExternalKey string  `json:"extracted_external_key"`
	Confidence           float64 `json:"confidence"`
	DocumentClass        string  `json:"document_class"`
}

// IngestionRoutedPayload is the body of dms.ingestion.routed.v1, emitted when
// the route step commits a staged item to a document/version.
type IngestionRoutedPayload struct {
	IngestionItemID string `json:"ingestion_item_id"`
	TenantID        string `json:"tenant_id"`
	DocumentID      string `json:"document_id"`
	VersionID       string `json:"version_id"`
	ExternalKey     string `json:"external_key"`
	NewDocument     bool   `json:"new_document"`
}

// ReviewCreatedPayload is the body of dms.review.created.v1, emitted when a
// low-confidence / ambiguous staged read is parked in the review queue (WS4).
type ReviewCreatedPayload struct {
	ReviewID                 string  `json:"review_id"`
	TenantID                 string  `json:"tenant_id"`
	IngestionItemID          string  `json:"ingestion_item_id"`
	Reason                   string  `json:"reason"`
	ExternalKey              string  `json:"external_key"`
	SuggestedMatchDocumentID string  `json:"suggested_match_document_id,omitempty"`
	Confidence               float64 `json:"confidence"`
}

// ReviewResolvedPayload is the body of dms.review.resolved.v1, emitted when a
// reviewer resolves a queued item (WS4). Decision is new_version | new_document
// | reject; DocumentID/VersionID are set only on a commit.
type ReviewResolvedPayload struct {
	ReviewID        string `json:"review_id"`
	TenantID        string `json:"tenant_id"`
	IngestionItemID string `json:"ingestion_item_id"`
	Decision        string `json:"decision"`
	DocumentID      string `json:"document_id,omitempty"`
	VersionID       string `json:"version_id,omitempty"`
	ResolvedBy      string `json:"resolved_by"`
}
