package service

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/repository"
)

// ProcessingStatus is the per-document roll-up + per-stage detail the
// document-detail page reads (ADR 0115).
type ProcessingStatus struct {
	Status string                       `json:"status"` // documents.processing_status roll-up
	Stages []repository.ProcessingStage `json:"stages"`
}

// GetProcessingStages returns the roll-up status + per-stage rows for a
// document. View permission is required (members can see status; only
// the reprocess action is role-gated).
func (s *DocumentService) GetProcessingStages(ctx context.Context, docID uuid.UUID) (*ProcessingStatus, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	out := &ProcessingStatus{Status: "pending"}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		// Read the roll-up column directly — it isn't on the document
		// model (and threading it through every documents SELECT would be
		// invasive), but this single-column read inside the same tenant tx
		// is cheap and self-contained.
		if err := tx.QueryRow(ctx,
			"SELECT processing_status FROM documents WHERE tenant_id=$1 AND id=$2",
			tenantID, docID).Scan(&out.Status); err != nil {
			return err
		}
		stages, err := s.repos.Processing.ListStages(ctx, tx, tenantID, docID)
		if err != nil {
			return err
		}
		out.Stages = stages
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ReprocessDocument re-emits dms.version.uploaded.v1 for the document's
// current version, replaying the whole intelligence pipeline (OCR →
// classify → NER → embed → …). Idempotent at the event layer (fresh
// event_id; intel_dedupe handles concurrent re-fires). Role-gated to
// owner|admin|compliance_officer — matching the OCR re-run button.
//
// The event is the single canonical trigger, so this rebuilds exactly
// the same VersionUploadedPayload that CreateVersion emits, inside one
// tx via the transactional outbox.
func (s *DocumentService) ReprocessDocument(ctx context.Context, docID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, docID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if doc.CurrentVersionID == nil {
			return vdmserr.Validation("document", "no uploaded content to reprocess")
		}
		v, err := s.repos.Versions.GetByID(ctx, tx, tenantID, *doc.CurrentVersionID)
		if err != nil {
			return err
		}
		storageURI, err := s.lookupBlobURI(ctx, tx, tenantID, v.ContentBlobID)
		if err != nil {
			return fmt.Errorf("lookup blob uri: %w", err)
		}
		evtID, _ := newExternalID()
		evt, err := model.NewOutboxEvent(tenantID, "dms.version.uploaded.v1", "version", v.ID,
			model.VersionUploadedPayload{
				EventID:          evtID.String(),
				TenantID:         tenantID.String(),
				DocumentID:       doc.ID.String(),
				VersionID:        v.ID.String(),
				VersionNumber:    v.VersionNumber,
				ContentBlobID:    v.ContentBlobID.String(),
				StorageURI:       storageURI,
				MimeType:         v.MimeType,
				SizeBytes:        v.SizeBytes,
				SHA256:           v.SHA256Hash,
				UploadedByUserID: userID.String(),
				UploadedAt:       time.Now().UTC().Format(time.RFC3339),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		// Reset the roll-up so the UI immediately reflects "running"
		// rather than the stale failed state until the workers write
		// fresh stage rows.
		if _, err := tx.Exec(ctx,
			"UPDATE documents SET processing_status='running' WHERE tenant_id=$1 AND id=$2",
			tenantID, doc.ID); err != nil {
			return err
		}
		return nil
	})
}
