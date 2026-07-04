// Package repository is the Postgres data-access layer for the document
// service. Every method takes a pgx.Tx (never the pool directly) so that
// the service layer can bundle writes + outbox inserts into one TX.
// Callers must invoke these inside database.WithTenant / WithTenantTx so
// that the `app.current_tenant` GUC is set and RLS policies fire.
package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// ---- Interfaces (consumed by the service layer; enable mocking) -----------

type DocumentRepository interface {
	Create(ctx context.Context, tx pgx.Tx, d *model.Document) error
	// InsertUpsert inserts a document, treating a (tenant_id, external_id)
	// collision as a no-op (created=false). Used by the upsert create
	// branch so concurrent first-creations converge on one row.
	InsertUpsert(ctx context.Context, tx pgx.Tx, d *model.Document) (bool, error)
	// GetByExternalID loads the document for a tenant-scoped business key
	// (ErrNotFound when absent). forUpdate takes a row lock to serialize
	// concurrent version appends against the same key.
	GetByExternalID(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, externalID string, forUpdate bool) (*model.Document, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Document, error)
	// FindByContentHash returns live documents whose any version carries
	// the given sha256 (versions.sha256_hash is denormalized, so no join
	// to content_blobs is needed). workspaceID == uuid.Nil searches the
	// whole tenant. Powers the pre-upload duplicate prompt.
	FindByContentHash(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, sha256 string, limit int) ([]model.DuplicateMatch, error)
	Update(ctx context.Context, tx pgx.Tx, d *model.Document) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	Restore(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	BlobsForDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]struct {
		BlobID uuid.UUID
		Bucket string
		Key    string
	}, error)
	DeleteVersionsAndBlobs(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) error
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f model.DocumentFilter) (*model.Page[model.Document], error)
	UpdateLifecycleState(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.LifecycleState) error
	SetCurrentVersion(ctx context.Context, tx pgx.Tx, tenantID, id, versionID uuid.UUID, sha, mime string, size int64) error
	CountByFolder(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) (int64, error)
}

type WorkspaceRepository interface {
	Create(ctx context.Context, tx pgx.Tx, w *model.Workspace) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Workspace, error)
	// List returns workspaces visible to the caller. tenant owner/admin
	// sees all; other roles see only ones they created, are members
	// of, OR hold a folder grant in (direct user OR via a group on the
	// caller's userGroups slice). Role string is the caller's
	// tenant-level role; empty role is treated as the most-restrictive.
	List(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, userGroups []uuid.UUID, role string) ([]model.Workspace, error)
	Update(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name, description string) error
	// UpdateCreatedBy reassigns the workspace's creator (single-owner
	// model). Callers gate on tenant role / current creator before
	// invoking.
	UpdateCreatedBy(ctx context.Context, tx pgx.Tx, tenantID, id, newCreatedBy uuid.UUID) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// AddMember enrolls a user as a workspace member with the given
	// role (admin / member / viewer). Idempotent — re-runs are no-ops.
	AddMember(ctx context.Context, tx pgx.Tx, tenantID, workspaceID, userID, addedBy uuid.UUID, role string) error
	// IsMember reports whether the user has any active membership row
	// for the workspace.
	IsMember(ctx context.Context, tx pgx.Tx, tenantID, workspaceID, userID uuid.UUID) (bool, error)
	// ListMembers + UpdateMemberRole + RemoveMember power the
	// per-workspace Settings → Members panel.
	ListMembers(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) ([]model.WorkspaceMember, error)
	UpdateMemberRole(ctx context.Context, tx pgx.Tx, tenantID, workspaceID, userID uuid.UUID, role string) error
	RemoveMember(ctx context.Context, tx pgx.Tx, tenantID, workspaceID, userID uuid.UUID) error
}

type FolderRepository interface {
	Create(ctx context.Context, tx pgx.Tx, f *model.Folder) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Folder, error)
	// Ancestors returns the breadcrumb (root → parent of f, exclusive of f
	// itself) ordered shallowest first. Empty for root folders.
	Ancestors(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ltreePath string) ([]model.Folder, error)
	// ListByParent returns one keyset page of direct children ordered by
	// (name, id), resuming after (cursorName, cursorID) when cursorID is set.
	// limit is clamped to [1, FolderPageMaxLimit]. Child/document counts are
	// filled with O(1) aggregates, not per-row subqueries (Workstream 6).
	ListByParent(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, parent *uuid.UUID, cursorName string, cursorID uuid.UUID, limit int) ([]model.Folder, error)
	UpdateName(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name string) error
	Move(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, oldPath, newParentPath string, newDepth int) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// SoftDeleteAllInWorkspace bulk-soft-deletes every folder in a
	// workspace. Used by DeleteWorkspace to clean up the auto-Root.
	SoftDeleteAllInWorkspace(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) error
	// SoftDeleteSubtree / RestoreSubtree power folder cascade delete +
	// recycle-bin restore (FIX-5). SoftDeleteSubtree returns the
	// cohort id + the affected folder and document ids so the caller
	// can publish a folder.deleted.v1 event that lets the search
	// consumer DeleteByQuery without re-walking the tree.
	SoftDeleteSubtree(ctx context.Context, tx pgx.Tx, tenantID, rootID, deletedBy uuid.UUID) (*SubtreeDeleteResult, error)
	RestoreSubtree(ctx context.Context, tx pgx.Tx, tenantID, rootID uuid.UUID) error
	HasChildren(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
	// ListEmptyFolders returns live leaf folders with no live child
	// folders and no live documents — the orphaned empties that
	// accumulate from aborted ingests. workspaceID == uuid.Nil scopes to
	// the whole tenant; a zero olderThan disables the age filter.
	// Ordered deepest-first so deleting within a single tx collapses
	// nested empties on the next pass.
	ListEmptyFolders(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, olderThan time.Time, limit int) ([]model.Folder, error)
	// Phase 2 — visibility + folder-scoped ACL.
	UpdateVisibility(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, visibility model.FolderVisibility, owner *uuid.UUID) error
	ListGrants(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) ([]model.FolderGrant, error)
	AddGrant(ctx context.Context, tx pgx.Tx, g *model.FolderGrant) error
	RemoveGrant(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID, granteeType string, granteeID uuid.UUID) error
	CanAccessFolder(ctx context.Context, tx pgx.Tx, tenantID, folderID, userID uuid.UUID, userGroups []uuid.UUID, isAdmin bool) (bool, error)
	// FilterAccessibleFolderIDs returns the subset of `folderIDs` the
	// caller can access. Batch version of CanAccessFolder — single
	// round-trip instead of N. Folder rows assumed live (deleted_at IS
	// NULL). isAdmin short-circuits at the call site.
	FilterAccessibleFolderIDs(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, folderIDs []uuid.UUID, userID uuid.UUID, userGroups []uuid.UUID) (map[uuid.UUID]bool, error)
	// ListSharedWithUser returns folders the caller has been granted
	// access to (directly or via a group) but does NOT own. Used by
	// the cross-workspace "Shared with me" view. Owner exclusion is
	// the only criterion that makes the result a discovery surface
	// rather than a re-list of "private folders I can see anyway".
	ListSharedWithUser(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, userGroups []uuid.UUID) ([]model.SharedFolder, error)
}

type VersionRepository interface {
	Create(ctx context.Context, tx pgx.Tx, v *model.Version) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Version, error)
	ListByDocument(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f model.VersionFilter) (*model.Page[model.Version], error)
	GetLatestByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*model.Version, error)
	CountByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int, error)
	NextVersionNumber(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int, error)
	UpdateLabel(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, label string) error
}

type ShareLinkRepository interface {
	Create(ctx context.Context, tx pgx.Tx, l *model.ShareLink) error
	GetByTokenHash(ctx context.Context, tx pgx.Tx, tokenHash string) (*model.ShareLink, error)
	ListByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) ([]model.ShareLink, error)
	ListByTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, onlyActive bool) ([]model.ShareLinkAdmin, error)
	RevokeAllForDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (int64, error)
	Deactivate(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// IncrementViewCount atomically bumps the view counter. Returns
	// false when max_views would be exceeded — caller should reject
	// the access. FIX-10 closed the TOCTOU race the previous unconditional
	// bump invited.
	IncrementViewCount(ctx context.Context, tx pgx.Tx, id uuid.UUID) (bool, error)
}

type TagRepository interface {
	Create(ctx context.Context, tx pgx.Tx, t *model.Tag) error
	ListByTenant(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.Tag, error)
	Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	RemoveTagFromAllDocuments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, name string) error
	GetByName(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, name string) (*model.Tag, error)
}

type LegalHoldRepository interface {
	Place(ctx context.Context, tx pgx.Tx, r *model.LegalHoldRecord) error
	Release(ctx context.Context, tx pgx.Tx, tenantID, documentID, releasedBy uuid.UUID) (*model.LegalHoldRecord, error)
	GetActiveByDocument(ctx context.Context, tx pgx.Tx, tenantID, documentID uuid.UUID) (*model.LegalHoldRecord, error)
}

type ReportRepository interface {
	Create(ctx context.Context, tx pgx.Tx, s *model.SavedReport) error
	Update(ctx context.Context, tx pgx.Tx, s *model.SavedReport) (bool, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SavedReport, error)
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.SavedReport, error)
	Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
	TouchLastRun(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
}

type TemplateRepository interface {
	Create(ctx context.Context, tx pgx.Tx, t *model.WorkspaceTemplate) error
	Update(ctx context.Context, tx pgx.Tx, t *model.WorkspaceTemplate) (bool, error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.WorkspaceTemplate, error)
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]model.WorkspaceTemplate, error)
	Delete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
}

type MetadataSchemaRepository interface {
	Get(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]byte, error) // returns JSON-encoded JSON-Schema
	Upsert(ctx context.Context, tx pgx.Tx, tenantID, updatedBy uuid.UUID, jsonSchema []byte) error
}

type OutboxRepository interface {
	Insert(ctx context.Context, tx pgx.Tx, e *model.OutboxEvent) error
}

// IngestionRepository is the data-access layer for WS3's pre-commit staging
// table. Create is idempotent on the (tenant, checksum, target) partial unique
// index so a re-POST converges on one active row.
type IngestionRepository interface {
	// Create inserts a staged item, treating a collision on the active-row
	// dedup index as a no-op. created=false returns the existing active row.
	Create(ctx context.Context, tx pgx.Tx, it *model.IngestionItem) (created bool, existing *model.IngestionItem, err error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, forUpdate bool) (*model.IngestionItem, error)
	// UpdateRouting sets status + match_document_id (nil clears) after the
	// route step decides.
	UpdateRouting(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.IngestionStatus, matchDocumentID *uuid.UUID) error
	// SetStatus is a generic status (+ optional failure reason) update.
	SetStatus(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.IngestionStatus, failureReason string) error
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, limit int) ([]model.IngestionItem, error)
}

// ReviewQueueRepository is the data-access layer for WS3's human review queue.
type ReviewQueueRepository interface {
	// Create inserts a review item, treating a collision on (tenant,
	// ingestion_item_id) as a no-op so the route step is idempotent.
	Create(ctx context.Context, tx pgx.Tx, it *model.ReviewQueueItem) (created bool, err error)
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.ReviewQueueItem, error)
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, limit int) ([]model.ReviewQueueItem, error)
	// ListKeyset is the keyset-paginated read behind GET /api/v1/review-queue.
	ListKeyset(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, status string, cursorTime time.Time, cursorID uuid.UUID, limit int) ([]model.ReviewQueueItem, error)
	// Resolve marks the review terminal with the reviewer's decision.
	Resolve(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, status model.ReviewStatus, resolution string, resultingDoc, resultingVer *uuid.UUID, resolvedBy uuid.UUID, notes string) error
}

// ---- Bundle (convenient wiring) -------------------------------------------

// Repositories groups all repos for a document-service instance.
type Repositories struct {
	Pool                *pgxpool.Pool
	Workspaces          WorkspaceRepository
	Documents           DocumentRepository
	Folders             FolderRepository
	Versions            VersionRepository
	ShareLinks          ShareLinkRepository
	Tags                TagRepository
	LegalHolds          LegalHoldRepository
	Templates           TemplateRepository
	Reports             ReportRepository
	MetadataSchema      MetadataSchemaRepository
	Outbox              OutboxRepository
	Annotations         AnnotationRepository
	TagSuggestions      TagSuggestionRepository
	Routing             RoutingRepository
	Compliance          ComplianceRepository
	OCRQuality          OCRQualityRepository
	Anomaly             AnomalyRepository
	ClassifyCorrections ClassifyCorrectionRepository
	ActiveLearning      ActiveLearningRepository
	NER                 NERRepository
	Redaction           RedactionRepository
	// ADR 0066 — threaded comments + reactions.
	Comments CommentRepository
	// ADR 0068 — lightweight tasks (separate from workflow_tasks).
	Tasks TaskRepository
	// ADR 0115 — per-stage processing status surface.
	Processing ProcessingRepository
	// WS3 — pre-commit ingestion pipeline.
	Ingestion   IngestionRepository
	ReviewQueue ReviewQueueRepository
	// §8 — classification-based access control.
	Classification ClassificationRepository
	// §5 — dynamic viewer watermark config.
	Watermark WatermarkRepository
	// §5/§8 — IRM protected-container export.
	IRM IRMRepository
	// §3/§5 — sync delta + per-device state.
	Sync SyncRepository
}

// New wires concrete repo implementations against a single pool.
func New(pool *pgxpool.Pool) *Repositories {
	return &Repositories{
		Pool:                pool,
		Workspaces:          &workspaceRepo{},
		Documents:           &documentRepo{},
		Folders:             &folderRepo{},
		Versions:            &versionRepo{},
		ShareLinks:          &shareLinkRepo{},
		Tags:                &tagRepo{},
		LegalHolds:          &legalHoldRepo{},
		Templates:           &templateRepo{},
		Reports:             &reportRepo{},
		MetadataSchema:      &metadataSchemaRepo{},
		Outbox:              &outboxRepo{},
		Annotations:         NewAnnotationRepo(),
		TagSuggestions:      NewTagSuggestionRepo(),
		Routing:             NewRoutingRepo(),
		Compliance:          NewComplianceRepo(),
		OCRQuality:          NewOCRQualityRepo(),
		Anomaly:             NewAnomalyRepo(),
		ClassifyCorrections: NewClassifyCorrectionRepo(),
		ActiveLearning:      NewActiveLearningRepo(),
		NER:                 NewNERRepo(),
		Redaction:           NewRedactionRepo(),
		Comments:            NewCommentRepo(),
		Tasks:               NewTaskRepo(),
		Processing:          NewProcessingRepo(),
		Ingestion:           &ingestionRepo{},
		ReviewQueue:         &reviewQueueRepo{},
		Classification:      NewClassificationRepo(),
		Watermark:           NewWatermarkRepo(),
		IRM:                 NewIRMRepo(),
		Sync:                NewSyncRepo(),
	}
}
