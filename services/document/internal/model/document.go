// Package model holds the document service's internal domain types.
// These are distinct from the wire types in sedocv1 — mappers translate
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

// Document types. A note/wiki is a normal document distinguished by
// DocType so it inherits versioning, ACL, search, and audit while the
// tree/editor treat it specially. Empty is treated as DocTypeFile.
const (
	DocTypeFile = "file"
	DocTypeNote = "note"
	DocTypeWiki = "wiki"
)

// IsValidDocType reports whether t is an accepted document type. The empty
// string is NOT accepted here — callers default it to DocTypeFile first.
func IsValidDocType(t string) bool {
	switch t {
	case DocTypeFile, DocTypeNote, DocTypeWiki:
		return true
	}
	return false
}

// Document is the top-level aggregate.
type Document struct {
	TenantID uuid.UUID
	ID       uuid.UUID
	// ExternalID is the caller's stable business key (e.g.
	// "INV-2024-00188") used by the ERP upsert + byExternalKey paths.
	// Empty when the document has no external mapping. Tenant-unique
	// (partial unique index, migration 000072) when set; persisted as
	// SQL NULL when empty so the partial index ignores it.
	ExternalID               string
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
	// SecurityClassification is the sensitivity level gating access (§8):
	// unclassified < internal < confidential < restricted. Empty = unset.
	// HasPHI/HasPII are denormalised from the intelligence compliance scan
	// (ADR 0054); ClassificationSource records who set the level
	// ("scan" | "manual" | "records"). Migration 000087.
	SecurityClassification string
	HasPHI                 bool
	HasPII                 bool
	ClassificationSource   string
	SHA256Hash             string
	TotalSizeBytes         int64
	// VersionCount mirrors the denormalised documents.version_count
	// counter (incremented by the upload-complete path). Read-only on
	// this model — never written back.
	VersionCount           int32
	MimeType               string
	// DocType marks notes/wikis as a first-class document type. A note is
	// a normal document (versioning/ACL/search/audit) whose content is a
	// markdown version; the tree, search, and the collaborative editor key
	// off this. Defaults to DocTypeFile (migration 000078).
	DocType   string
	CreatedBy uuid.UUID
	// CreatedByName is denormalised at read time via LEFT JOIN users.
	// Empty when the uploader row is hard-deleted (rare — the schema
	// uses soft-delete) or missing. The handler-layer mapper passes
	// this through; the frontend renders "Deleted user" when empty +
	// CreatedBy non-zero. Not persisted, not write-mapped.
	CreatedByName string
	CreatedAt     time.Time
	UpdatedBy     uuid.UUID
	UpdatedAt     time.Time
	DeletedAt     *time.Time
	// WorkflowInstance is the document's currently-active workflow
	// summary, joined at read time from workflow_instances. nil when
	// no active (non-terminal) workflow exists. Used by the frontend's
	// DocumentCard + document header to render a status pill without
	// a per-row useQuery.
	WorkflowInstance *WorkflowInstanceSummary
}

// WorkflowInstanceSummary is the minimum the FE needs to render a
// workflow status badge / chip on a document surface. Pulled directly
// from workflow_instances + workflow_definitions; FK constraints
// guarantee the join lands.
type WorkflowInstanceSummary struct {
	ID             uuid.UUID
	DefinitionID   uuid.UUID
	DefinitionName string
	Status         string // pending | running | completed | failed | cancelled
	CurrentStep    int
	StartedAt      time.Time
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
	MemberCount   int64
	// SharedWithCount is how many DISTINCT principals other than the creator
	// hold access — workspace members plus active workspace-scoped grants.
	// Zero means private: nobody but the creator (and tenant admins, whose
	// access is implicit and deliberately not counted) can reach it.
	//
	// MemberCount alone can't answer this: the creator is auto-enrolled as a
	// member, so every workspace reads "1 member" whether or not it has been
	// shared, and a pure permissions-table grant adds no member row at all.
	SharedWithCount int64
}

// WorkspaceMember is one row of the workspace ACL plus enough user
// metadata to render the row without a follow-up fetch.
type WorkspaceMember struct {
	UserID      uuid.UUID
	Email       string
	DisplayName string
	Role        string // 'admin' | 'member' | 'viewer'
	AddedBy     *uuid.UUID
	AddedAt     time.Time
}

// FolderVisibility classifies who can see + act on a folder.
//   - FolderShared: inherits workspace membership / permissions
//   - FolderPrivate: visible only to the OwnerID + folder_grants
//     rows + workspace admins
type FolderVisibility string

const (
	FolderShared  FolderVisibility = "shared"
	FolderPrivate FolderVisibility = "private"
)

// DuplicateMatch is a lightweight projection of a document that already
// holds the same content (by version sha256) as a file about to be
// uploaded. Powers the pre-upload "possible duplicate" prompt.
type DuplicateMatch struct {
	DocumentID  uuid.UUID
	Title       string
	WorkspaceID uuid.UUID
	FolderID    uuid.UUID
	CreatedAt   time.Time
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
	// Phase 2: visibility + owner. Visibility defaults to shared on
	// every row (DB default). OwnerID is populated for private
	// folders; NULL for shared (the column is nullable in the schema).
	Visibility FolderVisibility
	OwnerID    *uuid.UUID
	// Ancestors is populated by GetFolder; the breadcrumb trail from root
	// to (but not including) this folder, ordered shallowest → deepest.
	// Empty for root folders. Not persisted.
	Ancestors []Folder `json:"ancestors,omitempty"`
}

// FolderGrant is one row of the folder-scoped ACL. (GranteeType,
// GranteeID) uniquely identifies the principal granted access; the
// FK constraint to (tenant_id, folder_id) cascades on folder delete.
type FolderGrant struct {
	TenantID    uuid.UUID
	ID          uuid.UUID
	FolderID    uuid.UUID
	GranteeType string // 'user' | 'group'
	GranteeID   uuid.UUID
	GrantedBy   *uuid.UUID
	CreatedAt   time.Time
}

// SharedFolder is one row of the cross-workspace "shared with me"
// list. Carries enough folder + workspace metadata for the UI to
// render the row and deep-link to /workspaces/{wsId}?folder={folderId},
// plus the grant provenance (direct vs via group).
type SharedFolder struct {
	Folder        Folder
	WorkspaceName string
	GrantedVia    string // 'user' | 'group'
	GroupID       *uuid.UUID
	GrantedAt     time.Time
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
	// DeletedOnly flips the filter to soft-deleted rows only. Used by
	// the admin Trash list, which doesn't scope by workspace.
	DeletedOnly bool
	// DeletedBy scopes DeletedOnly to one deleter — the per-user Trash.
	// Ignored unless DeletedOnly is set.
	DeletedBy *uuid.UUID
	// NotUserCleared drops rows the deleter has already cleared from
	// their own Trash. The admin Trash leaves this false so it keeps
	// showing (and can still restore) what a user cleared.
	NotUserCleared bool
}

// VersionFilter is pagination scoped to a single document.
type VersionFilter struct {
	DocumentID uuid.UUID
	PageSize   int
	PageToken  string
}
