package service

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// §17.3 / D10 — annotation service layer.
//
// Wiring:
//   - Every mutation emits a dms.annotation.* outbox event so the
//     collaboration WS service can fan out live updates (D10 real-
//     time slice, follow-up PR).
//   - Permission check is "edit" on the document for create/update/
//     delete, "view" for list. This matches the comment-on-doc
//     semantics from §17.3 — if you can read the doc, you can see
//     annotations; if you can write it, you can annotate.
//   - RLS + the tenant GUC in WithTenantTx enforce cross-tenant
//     isolation at the DB layer.

// CreateAnnotationInput is the service-layer shape for
// POST /api/v1/documents/:id/versions/:vid/annotations.
type CreateAnnotationInput struct {
	DocumentID uuid.UUID
	VersionID  uuid.UUID
	Page       int
	Type       string
	Data       map[string]any
}

// UpdateAnnotationInput — partial update; Data replaces wholesale
// because per-type coord schemas vary too much for a generic patch.
type UpdateAnnotationInput struct {
	ID   uuid.UUID
	Page int
	Data map[string]any
}

// CreateAnnotation persists a new annotation, checks the caller has
// edit permission on the document, and emits dms.annotation.created.v1.
func (s *DocumentService) CreateAnnotation(ctx context.Context, in *CreateAnnotationInput) (*model.Annotation, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.DocumentID == uuid.Nil || in.VersionID == uuid.Nil {
		return nil, errInvalidInput("document_id/version_id", "required")
	}
	if !model.IsValidAnnotationType(in.Type) {
		return nil, vdmserr.Validation("type", "must be one of highlight|note|stamp|drawing")
	}
	if in.Page <= 0 {
		in.Page = 1
	}
	if in.Data == nil {
		in.Data = map[string]any{}
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	ann := &model.Annotation{
		TenantID:   tenantID,
		ID:         id,
		DocumentID: in.DocumentID,
		VersionID:  in.VersionID,
		PageNumber: in.Page,
		Type:       in.Type,
		Data:       in.Data,
		CreatedBy:  userID,
		CreatedAt:  now,
		UpdatedAt:  now,
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Permission check against the document; legal-hold and
		// soft-delete are re-checked because annotations shouldn't
		// be creatable on a removed doc.
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}

		if s.repos.Annotations == nil {
			return vdmserr.Internal("annotations repository not wired")
		}
		if err := s.repos.Annotations.Create(ctx, tx, ann); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.annotation.created.v1", "annotation", ann.ID,
			model.AnnotationCreatedPayload{
				AnnotationID: ann.ID.String(),
				DocumentID:   ann.DocumentID.String(),
				VersionID:    ann.VersionID.String(),
				PageNumber:   ann.PageNumber,
				Type:         ann.Type,
				CreatedBy:    userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return ann, nil
}

// ListAnnotations returns every non-deleted annotation on a given
// (document, version). Page ordering matches drawing order on paper
// so a re-render of the overlay is stable.
func (s *DocumentService) ListAnnotations(ctx context.Context, documentID, versionID uuid.UUID) ([]model.Annotation, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Annotation
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
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
		if s.repos.Annotations == nil {
			return vdmserr.Internal("annotations repository not wired")
		}
		out, err = s.repos.Annotations.ListByDocumentVersion(ctx, tx, tenantID, documentID, versionID)
		return err
	})
	return out, err
}

// UpdateAnnotation replaces page + data on an existing annotation.
// Type is NOT mutable — a highlight stays a highlight — because the
// frontend overlay renderer picks the component off the type and a
// silent type swap would break every subscribed viewer.
func (s *DocumentService) UpdateAnnotation(ctx context.Context, in *UpdateAnnotationInput) (*model.Annotation, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == uuid.Nil {
		return nil, errInvalidInput("id", "required")
	}
	if in.Page <= 0 {
		in.Page = 1
	}
	if in.Data == nil {
		in.Data = map[string]any{}
	}

	var out *model.Annotation
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if s.repos.Annotations == nil {
			return vdmserr.Internal("annotations repository not wired")
		}
		cur, err := s.repos.Annotations.GetByID(ctx, tx, tenantID, in.ID)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, cur.DocumentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}
		if err := s.repos.Annotations.Update(ctx, tx, tenantID, in.ID, in.Page, in.Data); err != nil {
			return err
		}
		cur.PageNumber = in.Page
		cur.Data = in.Data
		cur.UpdatedAt = time.Now().UTC()
		evt, err := model.NewOutboxEvent(tenantID, "dms.annotation.updated.v1", "annotation", cur.ID,
			model.AnnotationUpdatedPayload{
				AnnotationID: cur.ID.String(),
				DocumentID:   cur.DocumentID.String(),
				VersionID:    cur.VersionID.String(),
				UpdatedBy:    userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		out = cur
		return nil
	})
	return out, err
}

// DeleteAnnotation soft-deletes. Hard delete is a GDPR erase job,
// not a user-facing endpoint.
func (s *DocumentService) DeleteAnnotation(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if id == uuid.Nil {
		return errInvalidInput("id", "required")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if s.repos.Annotations == nil {
			return vdmserr.Internal("annotations repository not wired")
		}
		cur, err := s.repos.Annotations.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, cur.DocumentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}
		if err := s.repos.Annotations.SoftDelete(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.annotation.deleted.v1", "annotation", id,
			model.AnnotationDeletedPayload{
				AnnotationID: id.String(),
				DocumentID:   cur.DocumentID.String(),
				VersionID:    cur.VersionID.String(),
				DeletedBy:    userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}
