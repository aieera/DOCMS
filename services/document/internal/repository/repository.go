// Package repository is the Postgres data-access layer for the document
// service. Every method takes a pgx.Tx (never the pool directly) so that
// the service layer can bundle writes + outbox inserts into one TX.
// Callers must invoke these inside database.WithTenant / WithTenantTx so
// that the `app.current_tenant` GUC is set and RLS policies fire.
package repository

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// ---- Interfaces (consumed by the service layer; enable mocking) -----------

type DocumentRepository interface {
	Create(ctx context.Context, tx pgx.Tx, d *model.Document) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Document, error)
	Update(ctx context.Context, tx pgx.Tx, d *model.Document) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f model.DocumentFilter) (*model.Page[model.Document], error)
	UpdateLifecycleState(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.LifecycleState) error
	SetCurrentVersion(ctx context.Context, tx pgx.Tx, tenantID, id, versionID uuid.UUID, sha, mime string, size int64) error
	CountByFolder(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) (int64, error)
}

type WorkspaceRepository interface {
	Create(ctx context.Context, tx pgx.Tx, w *model.Workspace) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Workspace, error)
	// List returns workspaces visible to the caller. tenant owner/admin
	// sees all; other roles see only ones they created or are members
	// of. Role string is the caller's tenant-level role (owner/admin/
	// member/viewer); empty role is treated as the most-restrictive.
	List(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, role string) ([]model.Workspace, error)
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
}

type FolderRepository interface {
	Create(ctx context.Context, tx pgx.Tx, f *model.Folder) error
	GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Folder, error)
	// Ancestors returns the breadcrumb (root → parent of f, exclusive of f
	// itself) ordered shallowest first. Empty for root folders.
	Ancestors(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ltreePath string) ([]model.Folder, error)
	ListByParent(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, parent *uuid.UUID) ([]model.Folder, error)
	UpdateName(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name string) error
	Move(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, oldPath, newParentPath string, newDepth int) error
	SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error
	// SoftDeleteAllInWorkspace bulk-soft-deletes every folder in a
	// workspace. Used by DeleteWorkspace to clean up the auto-Root.
	SoftDeleteAllInWorkspace(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) error
	HasChildren(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error)
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
	IncrementViewCount(ctx context.Context, tx pgx.Tx, id uuid.UUID) error
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

type MetadataSchemaRepository interface {
	Get(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID) ([]byte, error) // returns JSON-encoded JSON-Schema
	Upsert(ctx context.Context, tx pgx.Tx, tenantID, updatedBy uuid.UUID, jsonSchema []byte) error
}

type OutboxRepository interface {
	Insert(ctx context.Context, tx pgx.Tx, e *model.OutboxEvent) error
}

// ---- Bundle (convenient wiring) -------------------------------------------

// Repositories groups all repos for a document-service instance.
type Repositories struct {
	Pool          *pgxpool.Pool
	Workspaces    WorkspaceRepository
	Documents     DocumentRepository
	Folders       FolderRepository
	Versions      VersionRepository
	ShareLinks    ShareLinkRepository
	Tags          TagRepository
	LegalHolds    LegalHoldRepository
	MetadataSchema MetadataSchemaRepository
	Outbox             OutboxRepository
	Annotations        AnnotationRepository
	TagSuggestions     TagSuggestionRepository
	Routing            RoutingRepository
	Compliance         ComplianceRepository
	OCRQuality         OCRQualityRepository
	Anomaly            AnomalyRepository
	ClassifyCorrections ClassifyCorrectionRepository
	ActiveLearning      ActiveLearningRepository
	NER                 NERRepository
	Redaction           RedactionRepository
	// ADR 0066 — threaded comments + reactions.
	Comments            CommentRepository
	// ADR 0068 — lightweight tasks (separate from workflow_tasks).
	Tasks               TaskRepository
}

// New wires concrete repo implementations against a single pool.
func New(pool *pgxpool.Pool) *Repositories {
	return &Repositories{
		Pool:           pool,
		Workspaces:     &workspaceRepo{},
		Documents:      &documentRepo{},
		Folders:        &folderRepo{},
		Versions:       &versionRepo{},
		ShareLinks:     &shareLinkRepo{},
		Tags:           &tagRepo{},
		LegalHolds:     &legalHoldRepo{},
		MetadataSchema: &metadataSchemaRepo{},
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
	}
}
