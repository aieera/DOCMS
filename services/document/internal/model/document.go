// Package model holds the document service's internal domain types.
// These are distinct from the wire types in vaultdmsv1 — mappers translate
// between them at the handler boundary.
package model

import (
	"time"

	"github.com/google/uuid"
)

// ---- Lifecycle enums -------------------------------------------------------

// LifecycleState enumerates the states a document can occupy.
type LifecycleState string

const (
	StateDraft      LifecycleState = "draft"
	StateInReview   LifecycleState = "in_review"
	StateActive     LifecycleState = "active"
	StateSuperseded LifecycleState = "superseded"
	StateRetained   LifecycleState = "retained"
	StateArchived   LifecycleState = "archived"
	StateDisposed   LifecycleState = "disposed"
	StateLegalHold  LifecycleState = "legal_hold"
)

// LifecycleAction enumerates the verbs that can trigger a transition.
type LifecycleAction string

const (
	ActionSubmitForReview LifecycleAction = "submit_for_review"
	ActionApprove         LifecycleAction = "approve"
	ActionReject          LifecycleAction = "reject"
	ActionSupersede       LifecycleAction = "supersede"
	ActionArchive         LifecycleAction = "archive"
	ActionDispose         LifecycleAction = "dispose"
	ActionApplyHold       LifecycleAction = "apply_hold"
	ActionReleaseHold     LifecycleAction = "release_hold"
	ActionRestore         LifecycleAction = "restore"
)

// ---- Domain types ----------------------------------------------------------

// Document is the top-level aggregate.
type Document struct {
	TenantID                 uuid.UUID
	ID                       uuid.UUID
	WorkspaceID              uuid.UUID
	FolderID                 uuid.UUID
	Title                    string
	Description              string
	LifecycleState           LifecycleState
	RegionPin                string
	CustomMetadata           map[string]any
	Tags                     []string
	CurrentVersionID         *uuid.UUID
	DocumentClass            string
	ClassificationConfidence float64
	SHA256Hash               string
	TotalSizeBytes           int64
	MimeType                 string
	CreatedBy                uuid.UUID
	// CreatedByName is denormalised at read time via LEFT JOIN users.
	// Empty when the uploader row is hard-deleted (rare — the schema
	// uses soft-delete) or missing. The handler-layer mapper passes
	// this through; the frontend renders "Deleted user" when empty +
	// CreatedBy non-zero. Not persisted, not write-mapped.
	CreatedByName            string
	CreatedAt                time.Time
	UpdatedBy                uuid.UUID
	UpdatedAt                time.Time
	DeletedAt                *time.Time
}

// Version is an immutable record of a document content revision.
// Label is an optional human-friendly name set after upload (e.g.
// "Q1 final", "Approved for legal review"). Empty string = unlabelled.
type Version struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	DocumentID    uuid.UUID
	VersionNumber int
	ContentBlobID uuid.UUID
	SizeBytes     int64
	MimeType      string
	SHA256Hash    string
	CreatedBy     uuid.UUID
	CreatedByName string
	CreatedAt     time.Time
	ChangeSummary string
	Label         string
}

// Workspace is the top-level tenant-scoped container. Aggregates
// folders and documents; doubles as a policy-check anchor.
type Workspace struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	Name          string
	Description   string
	RegionPin     string
	Settings      []byte // JSON; empty slice means default
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
	UpdatedAt     time.Time
	DeletedAt     *time.Time
	DocumentCount int64
	FolderCount   int64
}

// Folder is an ltree-pathed container for documents and sub-folders.
type Folder struct {
	TenantID         uuid.UUID
	ID               uuid.UUID
	WorkspaceID      uuid.UUID
	ParentFolderID   *uuid.UUID
	Name             string
	Path             string
	Depth            int
	DocumentCount    int64
	ChildFolderCount int64
	CreatedBy        uuid.UUID
	CreatedAt        time.Time
	UpdatedAt        time.Time
	DeletedAt        *time.Time
	// Ancestors is populated by GetFolder; the breadcrumb trail from root
	// to (but not including) this folder, ordered shallowest → deepest.
	// Empty for root folders. Not persisted.
	Ancestors []Folder `json:"ancestors,omitempty"`
}

// ShareLink is a tokenized anonymous access grant to a document.
type ShareLink struct {
	TenantID     uuid.UUID
	ID           uuid.UUID
	DocumentID   uuid.UUID
	CreatedBy    uuid.UUID
	Token        string
	PasswordHash string
	ExpiresAt    *time.Time
	MaxViews     int
	ViewCount    int
	Permissions  []string
	IsActive     bool
	CreatedAt    time.Time
	AccessedAt   *time.Time
}

// ShareLinkAdmin extends ShareLink with the document title for the
// tenant-wide admin view (Wave 10). Keeps ShareLink unchanged so
// the per-doc list/access paths don't carry a field they never use.
type ShareLinkAdmin struct {
	ShareLink
	DocumentTitle string
}

// Tag is a tenant-global label that can be applied to documents.
type Tag struct {
	TenantID      uuid.UUID
	ID            uuid.UUID
	Name          string
	Color         string
	DocumentCount int64
	CreatedBy     uuid.UUID
	CreatedAt     time.Time
}

// LegalHoldRecord tracks a document under hold and the state it should return
// to when released.
type LegalHoldRecord struct {
	TenantID               uuid.UUID
	ID                     uuid.UUID
	DocumentID             uuid.UUID
	HoldName               string
	MatterReference        string
	Reason                 string
	PreviousLifecycleState LifecycleState
	PlacedBy               uuid.UUID
	PlacedAt               time.Time
	ReleasedAt             *time.Time
	ReleasedBy             *uuid.UUID
}

// ---- Paging / filtering ----------------------------------------------------

// Page is a generic paginated result envelope.
type Page[T any] struct {
	Items         []T
	NextPageToken string
	TotalCount    int64
}

// DocumentFilter carries all list-document filter fields.
type DocumentFilter struct {
	WorkspaceID    *uuid.UUID
	FolderID       *uuid.UUID
	LifecycleState *LifecycleState
	DocumentClass  string
	Tags           []string
	CreatedAfter   *time.Time
	CreatedBefore  *time.Time
	Query          string
	SortBy         string // "created_at" | "updated_at" | "title" | "size_bytes"
	SortOrder      string // "asc" | "desc"
	PageSize       int
	PageToken      string
	IncludeDeleted bool
}

// VersionFilter is pagination scoped to a single document.
type VersionFilter struct {
	DocumentID uuid.UUID
	PageSize   int
	PageToken  string
}
