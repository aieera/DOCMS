package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// CreateDocument creates an empty document (no content yet) in state DRAFT.
// Content attaches via CreateVersion.
func (s *DocumentService) CreateDocument(ctx context.Context, in *CreateDocumentInput) (*model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateTitle(in.Title); err != nil {
		return nil, err
	}
	if err := validateDescription(in.Description); err != nil {
		return nil, err
	}
	if err := validateRegion(in.RegionPin); err != nil {
		return nil, err
	}
	if in.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if in.FolderID == uuid.Nil {
		return nil, errInvalidInput("folder_id", "required")
	}
	if in.RegionPin == "" {
		in.RegionPin = "us-east-1"
	}

	if err := s.requirePermission(ctx, userID, "edit", "folder", in.FolderID, map[string]any{
		"workspace_id": in.WorkspaceID.String(),
	}); err != nil {
		return nil, err
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	doc := &model.Document{
		TenantID:       tenantID,
		ID:             id,
		WorkspaceID:    in.WorkspaceID,
		FolderID:       in.FolderID,
		Title:          in.Title,
		Description:    in.Description,
		LifecycleState: model.StateDraft,
		RegionPin:      in.RegionPin,
		CustomMetadata: in.CustomMetadata,
		Tags:           in.Tags,
		CreatedBy:      userID,
		CreatedAt:      time.Now().UTC(),
		UpdatedBy:      in.UpdatedBy,
		UpdatedAt:      time.Now().UTC(),
	}
	if doc.CustomMetadata == nil {
		doc.CustomMetadata = map[string]any{}
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Validate metadata against tenant schema.
		schemaJSON, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
		if err != nil {
			return err
		}
		if err := validateMetadataAgainstSchema(schemaJSON, doc.CustomMetadata); err != nil {
			return err
		}
		// Verify the folder exists and belongs to the given workspace.
		folder, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
		if err != nil {
			return err
		}
		if folder.WorkspaceID != in.WorkspaceID {
			return vdmserr.Validation("folder_id", "folder is not in the given workspace")
		}

		if err := s.repos.Documents.Create(ctx, tx, doc); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.created.v1", "document", doc.ID,
			model.DocumentCreatedPayload{
				DocumentID:  doc.ID.String(),
				WorkspaceID: doc.WorkspaceID.String(),
				FolderID:    doc.FolderID.String(),
				Title:       doc.Title,
				RegionPin:   doc.RegionPin,
				CreatedBy:   userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return doc, nil
}

// GetDocument loads a document and computes the requesting user's 5-axis
// permission summary with a single BatchCheckPermission call.
func (s *DocumentService) GetDocument(ctx context.Context, id uuid.UUID) (*model.Document, *DocumentPermissions, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, nil, err
	}
	var doc *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err = s.repos.Documents.GetByID(ctx, tx, tenantID, id)
		return err
	})
	if err != nil {
		return nil, nil, err
	}
	if doc.DeletedAt != nil {
		return nil, nil, vdmserr.ErrNotFound
	}
	perms, err := s.summarizeDocumentPermissions(ctx, userID, id, map[string]any{
		"workspace_id":     doc.WorkspaceID.String(),
		"lifecycle_state":  string(doc.LifecycleState),
		"region_pin":       doc.RegionPin,
		"classification":   doc.DocumentClass,
		"under_legal_hold": doc.LifecycleState == model.StateLegalHold,
	})
	if err != nil {
		return nil, nil, err
	}
	if !perms.CanView {
		return nil, nil, vdmserr.ErrForbidden
	}
	return doc, perms, nil
}

// UpdateDocument mutates only the fields the caller explicitly set. It
// refuses changes that are blocked by legal hold (using the per-operation
// allow-list in model.IsLegalHoldBlocked).
func (s *DocumentService) UpdateDocument(ctx context.Context, in *UpdateDocumentInput) (*model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.Title != nil {
		if err := validateTitle(*in.Title); err != nil {
			return nil, err
		}
	}
	if in.Description != nil {
		if err := validateDescription(*in.Description); err != nil {
			return nil, err
		}
	}

	var out *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", cur.ID, map[string]any{
			"workspace_id":     cur.WorkspaceID.String(),
			"lifecycle_state":  string(cur.LifecycleState),
			"under_legal_hold": cur.LifecycleState == model.StateLegalHold,
		}); err != nil {
			return err
		}

		changed := []string{}
		if in.Title != nil && *in.Title != cur.Title {
			if model.IsLegalHoldBlocked(cur.LifecycleState, "update_title") {
				return vdmserr.ErrLegalHold
			}
			cur.Title = *in.Title
			changed = append(changed, "title")
		}
		if in.Description != nil && *in.Description != cur.Description {
			cur.Description = *in.Description
			changed = append(changed, "description")
		}
		if in.CustomMetadata != nil {
			if model.IsLegalHoldBlocked(cur.LifecycleState, "update_metadata") {
				return vdmserr.ErrLegalHold
			}
			schemaJSON, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
			if err != nil {
				return err
			}
			if err := validateMetadataAgainstSchema(schemaJSON, in.CustomMetadata); err != nil {
				return err
			}
			cur.CustomMetadata = in.CustomMetadata
			changed = append(changed, "custom_metadata")
		}
		if in.ClearTags {
			cur.Tags = []string{}
			changed = append(changed, "tags")
		} else if in.Tags != nil {
			cur.Tags = in.Tags
			changed = append(changed, "tags")
		}

		if len(changed) == 0 {
			out = cur
			return nil
		}
		cur.UpdatedBy = in.UpdatedBy
		cur.UpdatedAt = time.Now().UTC()
		if err := s.repos.Documents.Update(ctx, tx, cur); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.updated.v1", "document", cur.ID,
			model.DocumentUpdatedPayload{
				DocumentID:    cur.ID.String(),
				ChangedFields: changed,
				UpdatedBy:     userID.String(),
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

// DeleteDocument soft-deletes a document. Blocked by legal hold.
func (s *DocumentService) DeleteDocument(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Documents.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if model.IsLegalHoldBlocked(cur.LifecycleState, "delete") {
			return vdmserr.ErrLegalHold
		}
		// Wave 8.2: binding-table gate. A document can have an active
		// hold without its lifecycle_state being legal_hold (the hold
		// was applied while the doc was draft/active). Both gates are
		// checked so neither representation of "held" leaks through.
		if s.holds != nil {
			held, err := s.holds.AnyActiveHoldFor(ctx, tenantID, id)
			if err != nil {
				return fmt.Errorf("hold check: %w", err)
			}
			if held {
				return vdmserr.ErrLegalHold
			}
		}
		if err := s.requirePermission(ctx, userID, "delete", "document", cur.ID, map[string]any{
			"workspace_id":    cur.WorkspaceID.String(),
			"lifecycle_state": string(cur.LifecycleState),
		}); err != nil {
			return err
		}
		if err := s.repos.Documents.SoftDelete(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.deleted.v1", "document", id,
			model.DocumentDeletedPayload{
				DocumentID: id.String(),
				DeletedBy:  userID.String(),
				SoftDelete: true,
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// MoveDocument re-parents a document. Blocked by legal hold. Refuses a move
// that would change the document's region_pin — region is immutable after
// creation and is enforced via data-residency policy.
func (s *DocumentService) MoveDocument(ctx context.Context, in *MoveDocumentInput) (*model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var out *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if cur.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if model.IsLegalHoldBlocked(cur.LifecycleState, "move") {
			return vdmserr.ErrLegalHold
		}

		targetFolder, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.TargetFolderID)
		if err != nil {
			return err
		}
		targetWS := cur.WorkspaceID
		if in.TargetWorkspaceID != nil {
			targetWS = *in.TargetWorkspaceID
		}
		if targetFolder.WorkspaceID != targetWS {
			return vdmserr.Validation("target_folder_id", "folder is not in the target workspace")
		}

		// Permission check on BOTH ends.
		if err := s.requirePermission(ctx, userID, "edit", "folder", cur.FolderID, map[string]any{
			"workspace_id": cur.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "folder", in.TargetFolderID, map[string]any{
			"workspace_id": targetWS.String(),
		}); err != nil {
			return err
		}

		fromFolder := cur.FolderID
		fromWorkspace := cur.WorkspaceID
		cur.FolderID = in.TargetFolderID
		cur.WorkspaceID = targetWS
		cur.UpdatedBy = in.UpdatedBy
		cur.UpdatedAt = time.Now().UTC()
		if err := s.repos.Documents.Update(ctx, tx, cur); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.moved.v1", "document", cur.ID,
			model.DocumentMovedPayload{
				DocumentID:    cur.ID.String(),
				FromFolderID:  fromFolder.String(),
				ToFolderID:    in.TargetFolderID.String(),
				FromWorkspace: fromWorkspace.String(),
				ToWorkspace:   targetWS.String(),
				MovedBy:       userID.String(),
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

// ListDocuments returns a page of documents matching filter. Filter validation
// (region, lifecycle_state values) is deferred to the repository's SQL +
// sort_column allowlist.
func (s *DocumentService) ListDocuments(ctx context.Context, f model.DocumentFilter) (*model.Page[model.Document], error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if f.WorkspaceID == nil || *f.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if err := s.requirePermission(ctx, userID, "view", "workspace", *f.WorkspaceID, nil); err != nil {
		return nil, err
	}
	// Only admins may request soft-deleted rows.
	if f.IncludeDeleted {
		if err := s.requirePermission(ctx, userID, "admin", "workspace", *f.WorkspaceID, nil); err != nil {
			return nil, err
		}
	}
	var page *model.Page[model.Document]
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		page, err = s.repos.Documents.List(ctx, tx, tenantID, f)
		return err
	})
	return page, err
}

// CreateVersion creates the next immutable version. Reserves the next
// version number with a MAX()+1 read under the same transaction; concurrent
// writers are serialized by the UNIQUE(document_id, version_number)
// constraint, and the loser surfaces as ErrAlreadyExists (caller may retry).
func (s *DocumentService) CreateVersion(ctx context.Context, in *CreateVersionInput) (*model.Version, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.DocumentID == uuid.Nil {
		return nil, errInvalidInput("document_id", "required")
	}
	if in.ContentBlobID == uuid.Nil {
		return nil, errInvalidInput("content_blob_id", "required")
	}
	// SizeBytes / MimeType / SHA256Hash are stored on content_blobs as
	// the canonical source of truth (set during storage CompleteUpload).
	// The proto's CreateVersionRequest doesn't carry them — the
	// frontend only knows the blob_id. Look them up from the blob row
	// when caller didn't pass them. Caller-supplied values still win
	// (e.g. a future migration tool that writes versions directly).
	if in.SizeBytes <= 0 || in.SHA256Hash == "" || in.MimeType == "" {
		var (
			bSize int64
			bSHA  string
			bMime *string
		)
		// Direct SQL — content_blobs lives in the shared Postgres but
		// is owned by services/storage; the document service has no
		// repository for it. Read-only one-shot lookup is fine here.
		err := s.pool.QueryRow(ctx,
			`SELECT size_bytes, sha256_hash, mime_type
			 FROM content_blobs
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, in.ContentBlobID,
		).Scan(&bSize, &bSHA, &bMime)
		if err != nil {
			return nil, fmt.Errorf("lookup content_blob %s: %w", in.ContentBlobID, err)
		}
		if in.SizeBytes <= 0 {
			in.SizeBytes = bSize
		}
		if in.SHA256Hash == "" {
			in.SHA256Hash = bSHA
		}
		if in.MimeType == "" && bMime != nil {
			in.MimeType = *bMime
		}
	}
	if in.SizeBytes <= 0 {
		return nil, errInvalidInput("size_bytes", "must be > 0")
	}

	var out *model.Version
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if model.IsLegalHoldBlocked(doc.LifecycleState, "create_version") {
			return vdmserr.ErrLegalHold
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}

		n, err := s.repos.Versions.NextVersionNumber(ctx, tx, tenantID, doc.ID)
		if err != nil {
			return err
		}
		vid, err := uuid.NewV7()
		if err != nil {
			return err
		}
		v := &model.Version{
			TenantID:      tenantID,
			ID:            vid,
			DocumentID:    doc.ID,
			VersionNumber: n,
			ContentBlobID: in.ContentBlobID,
			SizeBytes:     in.SizeBytes,
			MimeType:      in.MimeType,
			SHA256Hash:    in.SHA256Hash,
			CreatedBy:     userID,
			CreatedByName: in.CreatedByName,
			CreatedAt:     time.Now().UTC(),
			ChangeSummary: in.ChangeSummary,
		}
		if err := s.repos.Versions.Create(ctx, tx, v); err != nil {
			return err
		}
		if err := s.repos.Documents.SetCurrentVersion(ctx, tx, tenantID, doc.ID, v.ID, v.SHA256Hash, v.MimeType, v.SizeBytes); err != nil {
			return err
		}

		// ADR 0021: emit dms.version.uploaded.v1 (not the legacy
		// dms.version.created.v1) so the intelligence pipeline — OCR,
		// classify, embed, preview — has a single canonical trigger.
		// storage_uri is composed from the content_blobs row in the same tx.
		storageURI, err := s.lookupBlobURI(ctx, tx, tenantID, in.ContentBlobID)
		if err != nil {
			return fmt.Errorf("lookup blob uri: %w", err)
		}
		evtID, _ := uuid.NewV7()
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
				UploadedAt:       v.CreatedAt.Format(time.RFC3339),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		versionUploadedEmitted.WithLabelValues("dms.version.uploaded.v1").Inc()

		// Dual-publish a notify event so the uploader gets an inbox row.
		// The notification consumer subscribes to dms.notify.> and reads
		// `data` as a DeliveryPayload (tenant_id + user_ids required).
		notifyEvt, err := model.NewOutboxEvent(tenantID, "dms.notify.document.uploaded.v1", "document", doc.ID,
			map[string]any{
				"tenant_id":     tenantID.String(),
				"user_ids":      []string{userID.String()},
				"type":          "document.uploaded",
				"title":         "Document uploaded",
				"body":          fmt.Sprintf("%q v%d is ready.", doc.Title, v.VersionNumber),
				"resource_type": "document",
				"resource_id":   doc.ID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, notifyEvt); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// lookupBlobURI fetches storage_bucket + storage_key for a content blob
// from the same transaction as the version insert. Returns an
// s3-scheme URI. The content_blobs table is owned by the storage
// service's migrations but is readable from any service inside the
// tenant GUC.
func (s *DocumentService) lookupBlobURI(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID) (string, error) {
	var bucket, key string
	err := tx.QueryRow(ctx, `
		SELECT storage_bucket, storage_key
		FROM content_blobs
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, blobID).Scan(&bucket, &key)
	if err != nil {
		return "", err
	}
	return "s3://" + bucket + "/" + key, nil
}

// RestoreVersion copies the content of an older version forward as a new
// current version. Not destructive: the original version row is preserved
// and the new row carries its content_blob_id + hash, plus a note referencing
// the restored version number.
func (s *DocumentService) RestoreVersion(ctx context.Context, documentID, versionID uuid.UUID, note string) (*model.Version, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if documentID == uuid.Nil || versionID == uuid.Nil {
		return nil, errInvalidInput("document_id/version_id", "required")
	}

	var out *model.Version
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if model.IsLegalHoldBlocked(doc.LifecycleState, "create_version") {
			return vdmserr.ErrLegalHold
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}

		src, err := s.repos.Versions.GetByID(ctx, tx, tenantID, versionID)
		if err != nil {
			return err
		}
		if src.DocumentID != doc.ID {
			return vdmserr.ErrNotFound
		}

		n, err := s.repos.Versions.NextVersionNumber(ctx, tx, tenantID, doc.ID)
		if err != nil {
			return err
		}
		vid, err := uuid.NewV7()
		if err != nil {
			return err
		}
		summary := note
		if summary == "" {
			summary = fmt.Sprintf("restored from v%d", src.VersionNumber)
		}
		v := &model.Version{
			TenantID:      tenantID,
			ID:            vid,
			DocumentID:    doc.ID,
			VersionNumber: n,
			ContentBlobID: src.ContentBlobID,
			SizeBytes:     src.SizeBytes,
			MimeType:      src.MimeType,
			SHA256Hash:    src.SHA256Hash,
			CreatedBy:     userID,
			CreatedAt:     time.Now().UTC(),
			ChangeSummary: summary,
		}
		if err := s.repos.Versions.Create(ctx, tx, v); err != nil {
			return err
		}
		if err := s.repos.Documents.SetCurrentVersion(ctx, tx, tenantID, doc.ID, v.ID, v.SHA256Hash, v.MimeType, v.SizeBytes); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.version.restored.v1", "version", v.ID,
			model.VersionCreatedPayload{
				VersionID:     v.ID.String(),
				DocumentID:    doc.ID.String(),
				VersionNumber: v.VersionNumber,
				ContentBlobID: v.ContentBlobID.String(),
				SizeBytes:     v.SizeBytes,
				MimeType:      v.MimeType,
				CreatedBy:     userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

func (s *DocumentService) ListVersions(ctx context.Context, documentID uuid.UUID, pageSize int, pageToken string) (*model.Page[model.Version], error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var page *model.Page[model.Version]
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "view", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		page, err = s.repos.Versions.ListByDocument(ctx, tx, tenantID, model.VersionFilter{
			DocumentID: documentID,
			PageSize:   pageSize,
			PageToken:  pageToken,
		})
		return err
	})
	return page, err
}

// SetVersionLabel updates the optional human-friendly label on a version.
// Pass an empty string to clear an existing label. Requires the same "edit"
// capability on the document as renaming the document title — both are
// metadata writes that don't touch the binary content.
//
// Legal hold permits update_metadata (see IsLegalHoldBlocked), so labels can
// be edited while the document is held. This matches the title-update rule.
func (s *DocumentService) SetVersionLabel(ctx context.Context, documentID, versionID uuid.UUID, label string) (*model.Version, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	// Clamp the label so we don't get blob-sized metadata sneaking in.
	// 200 chars is enough for a descriptive name.
	const maxLabelLen = 200
	label = strings.TrimSpace(label)
	if len(label) > maxLabelLen {
		return nil, vdmserr.Validation("label", "label must be 200 characters or fewer")
	}

	var out *model.Version
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Resolve document → permission check anchor + workspace.
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, documentID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id": doc.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		// Confirm the version belongs to this document — guards against
		// a caller passing a stranger version-id under their document's
		// path.
		v, err := s.repos.Versions.GetByID(ctx, tx, tenantID, versionID)
		if err != nil {
			return err
		}
		if v.DocumentID != documentID {
			return vdmserr.ErrNotFound
		}
		if err := s.repos.Versions.UpdateLabel(ctx, tx, tenantID, versionID, label); err != nil {
			return err
		}
		// Audit emit — labels are metadata writes worth surfacing in the
		// activity log so compliance reviewers can see who anchored which
		// version. Subject mirrors the retention_exempt pattern.
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.version_labeled.v1", "version", versionID, map[string]any{
			"document_id":    documentID.String(),
			"version_id":     versionID.String(),
			"version_number": v.VersionNumber,
			"label":          label,
			"actor_id":       userID.String(),
		})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		v.Label = label
		out = v
		return nil
	})
	return out, err
}

// UpdateLifecycle runs the state machine: validate transition, apply action
// side effects (legal hold placement/release, required fields), write state,
// emit event.
func (s *DocumentService) UpdateLifecycle(ctx context.Context, in *UpdateLifecycleInput) (*model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}

	var out *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		doc, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if doc.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}

		target, err := model.ValidateTransition(doc.LifecycleState, in.Action)
		if err != nil {
			return vdmserr.Validation("action", err.Error())
		}

		// Action-specific preconditions + required fields.
		switch in.Action {
		case model.ActionSubmitForReview:
			n, err := s.repos.Versions.CountByDocument(ctx, tx, tenantID, doc.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return vdmserr.Conflict("cannot submit for review: document has no versions")
			}
			if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, nil); err != nil {
				return err
			}

		case model.ActionApprove, model.ActionReject:
			if in.Action == model.ActionReject && in.Reason == "" {
				return errInvalidInput("reason", "required for reject")
			}
			if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, nil); err != nil {
				return err
			}
			// Enforce required-fields when promoting to active (RFC: the
			// in_review → active boundary is the contract for "complete
			// metadata" — drafts may be incomplete).
			if in.Action == model.ActionApprove {
				schemaJSON, err := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
				if err != nil {
					return err
				}
				if err := validateMetadataAgainstFullSchema(schemaJSON, doc.CustomMetadata, true); err != nil {
					return err
				}
			}

		case model.ActionArchive, model.ActionSupersede, model.ActionRestore:
			if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, nil); err != nil {
				return err
			}

		case model.ActionDispose:
			if in.Reason == "" {
				return errInvalidInput("reason", "required for dispose")
			}
			if err := s.requirePermission(ctx, userID, "admin", "document", doc.ID, nil); err != nil {
				return err
			}

		case model.ActionApplyHold:
			if in.HoldName == "" {
				return errInvalidInput("hold_name", "required for apply_hold")
			}
			if err := s.requirePermission(ctx, userID, "admin", "document", doc.ID, nil); err != nil {
				return err
			}
			holdID, err := uuid.NewV7()
			if err != nil {
				return err
			}
			if err := s.repos.LegalHolds.Place(ctx, tx, &model.LegalHoldRecord{
				TenantID:               tenantID,
				ID:                     holdID,
				DocumentID:             doc.ID,
				HoldName:               in.HoldName,
				MatterReference:        in.HoldMatterReference,
				Reason:                 in.Reason,
				PreviousLifecycleState: doc.LifecycleState,
				PlacedBy:               userID,
				PlacedAt:               time.Now().UTC(),
			}); err != nil {
				return err
			}

		case model.ActionReleaseHold:
			if err := s.requirePermission(ctx, userID, "admin", "document", doc.ID, nil); err != nil {
				return err
			}
			rec, err := s.repos.LegalHolds.Release(ctx, tx, tenantID, doc.ID, userID)
			if err != nil {
				if errors.Is(err, vdmserr.ErrNotFound) {
					return vdmserr.Conflict("no active legal hold to release")
				}
				return err
			}
			// Override sentinel target with the real previous state.
			target = rec.PreviousLifecycleState
		}

		from := doc.LifecycleState
		if err := s.repos.Documents.UpdateLifecycleState(ctx, tx, tenantID, doc.ID, target); err != nil {
			return err
		}
		doc.LifecycleState = target
		doc.UpdatedAt = time.Now().UTC()

		evt, err := model.NewOutboxEvent(tenantID, "dms.document.state_changed.v1", "document", doc.ID,
			model.DocumentStateChangedPayload{
				DocumentID: doc.ID.String(),
				FromState:  string(from),
				ToState:    string(target),
				Action:     string(in.Action),
				Reason:     in.Reason,
				ChangedBy:  userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		out = doc
		return nil
	})
	return out, err
}
