package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/document/internal/model"
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
	if err := validateExternalID(in.ExternalID); err != nil {
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

	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	if in.DocType == "" {
		in.DocType = model.DocTypeFile
	}
	if !model.IsValidDocType(in.DocType) {
		return nil, errInvalidInput("doc_type", "must be one of file|note|wiki")
	}

	doc := &model.Document{
		TenantID:       tenantID,
		ID:             id,
		ExternalID:     in.ExternalID,
		WorkspaceID:    in.WorkspaceID,
		FolderID:       in.FolderID,
		Title:          in.Title,
		Description:    in.Description,
		LifecycleState: model.StateDraft,
		RegionPin:      in.RegionPin,
		CustomMetadata: in.CustomMetadata,
		Tags:           in.Tags,
		DocType:        in.DocType,
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
		// FIX-4: materialise readable_by now so the search indexer's
		// onDocCreatedOrUpdated lands the doc with the right ACL on
		// the first event. Failures here log-and-continue — search
		// would otherwise eventually catch up via permission.changed.
		readableBy, readableUsers, readableGroups, rerr := s.computeFolderReaders(ctx, tx, tenantID, doc.FolderID, doc.WorkspaceID)
		if rerr != nil {
			s.log.Warn().Err(rerr).Str("doc", doc.ID.String()).Msg("compute readable_by failed; doc indexed without ACL")
			readableBy, readableUsers, readableGroups = nil, nil, nil
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.created.v1", "document", doc.ID,
			model.DocumentCreatedPayload{
				DocumentID:       doc.ID.String(),
				WorkspaceID:      doc.WorkspaceID.String(),
				FolderID:         doc.FolderID.String(),
				Title:            doc.Title,
				RegionPin:        doc.RegionPin,
				CreatedBy:        userID.String(),
				ReadableBy:       readableBy,
				ReadableByUsers:  readableUsers,
				ReadableByGroups: readableGroups,
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

// CreateNote creates a note/wiki document — a normal document with DocType
// set — so it inherits versioning, ACL, search, and audit. Content is added
// later as a markdown version through the normal version path; the document
// exists immediately so the collaborative (Yjs) editor can open on it, and
// it emits dms.document.created.v1 like any other document.
func (s *DocumentService) CreateNote(ctx context.Context, in CreateNoteInput) (*model.Document, error) {
	docType := in.DocType
	if docType == "" {
		docType = model.DocTypeNote
	}
	if docType != model.DocTypeNote && docType != model.DocTypeWiki {
		return nil, errInvalidInput("doc_type", "must be note or wiki")
	}
	title := in.Title
	if title == "" {
		title = "Untitled note"
	}
	return s.CreateDocument(ctx, &CreateDocumentInput{
		WorkspaceID: in.WorkspaceID,
		FolderID:    in.FolderID,
		Title:       title,
		DocType:     docType,
	})
}

// GetDocument loads a document and computes the requesting user's 5-axis
// permission summary with a single BatchCheckPermission call.
// isHex64 reports whether s is a 64-character lowercase hex string (a
// sha256 digest).
func isHex64(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// FindDuplicates returns existing documents whose content matches the
// given sha256 — the pre-upload "possible duplicate" check. Scoped to a
// workspace when workspaceID is non-nil (the common upload case).
// Non-admin callers only see matches in folders they can access, so a
// duplicate sitting in a private folder isn't leaked through the prompt.
func (s *DocumentService) FindDuplicates(ctx context.Context, workspaceID uuid.UUID, sha256 string) ([]model.DuplicateMatch, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	sha256 = strings.ToLower(strings.TrimSpace(sha256))
	if !isHex64(sha256) {
		return nil, vdmserr.Validation("sha256", "must be a 64-char hex sha256")
	}
	var matches []model.DuplicateMatch
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		found, ferr := s.repos.Documents.FindByContentHash(ctx, tx, tenantID, workspaceID, sha256, 50)
		if ferr != nil {
			return ferr
		}
		if s.callerIsTenantAdmin(ctx) || len(found) == 0 {
			matches = found
			return nil
		}
		// Permission filter: keep only matches in folders the caller can
		// access, reusing the batch ACL check so private-folder titles
		// aren't exposed via the duplicate prompt.
		folderIDs := make([]uuid.UUID, 0, len(found))
		for i := range found {
			if found[i].FolderID != uuid.Nil {
				folderIDs = append(folderIDs, found[i].FolderID)
			}
		}
		accessible, aerr := s.repos.Folders.FilterAccessibleFolderIDs(ctx, tx, tenantID, folderIDs, userID, auth.GetUserGroups(ctx))
		if aerr != nil {
			return aerr
		}
		for i := range found {
			if found[i].FolderID == uuid.Nil || accessible[found[i].FolderID] {
				matches = append(matches, found[i])
			}
		}
		return nil
	})
	return matches, err
}

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
	// §8: ACL grants visibility; apply the classification/clearance gate and
	// surface an explainable 403 on a block.
	if err := s.enforceClassificationView(ctx, tenantID, userID, doc); err != nil {
		return nil, nil, err
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

		// Records immutability: a declared record's metadata is frozen until
		// disposition.
		if err := s.blockedByRecord(ctx, tenantID, cur.ID); err != nil {
			return err
		}

		changed := []string{}
		// changedVals mirrors `changed` with the NEW values — the search
		// indexer applies them as a partial update (a name-only diff
		// used to full-replace the index doc and wipe content + ACL).
		changedVals := map[string]any{}
		if in.Title != nil && *in.Title != cur.Title {
			if model.IsLegalHoldBlocked(cur.LifecycleState, "update_title") {
				return vdmserr.ErrLegalHold
			}
			cur.Title = *in.Title
			changed = append(changed, "title")
			changedVals["title"] = cur.Title
		}
		if in.Description != nil && *in.Description != cur.Description {
			cur.Description = *in.Description
			changed = append(changed, "description")
			changedVals["description"] = cur.Description
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
			changedVals["custom_metadata"] = cur.CustomMetadata
		}
		if in.ClearTags {
			cur.Tags = []string{}
			changed = append(changed, "tags")
			changedVals["tags"] = cur.Tags
		} else if in.Tags != nil {
			cur.Tags = in.Tags
			changed = append(changed, "tags")
			changedVals["tags"] = cur.Tags
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
				Changed:       changedVals,
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
		// Records immutability: a declared record is deletable only via
		// disposition (records.Dispose), never the document delete path.
		if err := s.blockedByRecord(ctx, tenantID, id); err != nil {
			return err
		}
		// WORM object-lock: cannot delete before the retention date.
		if err := s.blockedByWORM(ctx, tenantID, id); err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "delete", "document", cur.ID, map[string]any{
			"workspace_id":    cur.WorkspaceID.String(),
			"lifecycle_state": string(cur.LifecycleState),
		}); err != nil {
			return err
		}
		if err := s.repos.Documents.SoftDelete(ctx, tx, tenantID, id, userID); err != nil {
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

// TrashDocument is a soft-deleted document plus the deletion metadata
// the Trash UI renders (who deleted it — stamped by both the single
// delete and the folder-cascade path).
type TrashDocument struct {
	model.Document
	DeletedBy     *uuid.UUID
	DeletedByName string
	// UserCleared is true once the deleter has cleared the item from
	// their own Trash. Only ever true in the admin listing — the
	// per-user listing filters these out — and it drives the "cleared
	// by user" badge so an admin can tell that the person who deleted
	// it believes it is already gone.
	UserCleared bool
}

// ListTrash returns the tenant's soft-deleted documents. Admin/owner
// only — gated at the handler layer because the trash spans every
// workspace and the row-level OPA checks (Rule 4/5) would short-circuit
// the cross-workspace view.
func (s *DocumentService) ListTrash(ctx context.Context, pageSize int, pageToken string) (*model.Page[TrashDocument], error) {
	return s.listTrash(ctx, pageSize, pageToken, false)
}

// ListMyTrash returns only what THIS caller deleted and hasn't yet cleared
// from their own Trash. Needs no role gate and no per-row permission check:
// deleted_by = caller is itself the authorization, and a member cannot see
// a colleague's deletion even in a folder they share.
func (s *DocumentService) ListMyTrash(ctx context.Context, pageSize int, pageToken string) (*model.Page[TrashDocument], error) {
	return s.listTrash(ctx, pageSize, pageToken, true)
}

func (s *DocumentService) listTrash(ctx context.Context, pageSize int, pageToken string, mineOnly bool) (*model.Page[TrashDocument], error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	f := model.DocumentFilter{
		DeletedOnly: true,
		PageSize:    pageSize,
		PageToken:   pageToken,
		SortBy:      "updated_at",
		SortOrder:   "desc",
	}
	if mineOnly {
		f.DeletedBy = &userID
		f.NotUserCleared = true
	}
	var out *model.Page[TrashDocument]
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		page, lErr := s.repos.Documents.List(ctx, tx, tenantID, f)
		if lErr != nil {
			return lErr
		}
		out = &model.Page[TrashDocument]{
			Items:         make([]TrashDocument, 0, len(page.Items)),
			NextPageToken: page.NextPageToken,
			TotalCount:    page.TotalCount,
		}
		for i := range page.Items {
			out.Items = append(out.Items, TrashDocument{Document: page.Items[i]})
		}
		if len(out.Items) == 0 {
			return nil
		}
		// deleted_by isn't part of the shared List projection (hot
		// path) — enrich the one page here instead.
		ids := make([]uuid.UUID, 0, len(out.Items))
		for i := range out.Items {
			ids = append(ids, out.Items[i].ID)
		}
		rows, qErr := tx.Query(ctx, `
			SELECT d.id, d.deleted_by, COALESCE(u.display_name, ''),
			       d.user_cleared_at IS NOT NULL
			  FROM documents d
			  LEFT JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.deleted_by
			 WHERE d.tenant_id = $1 AND d.id = ANY($2)
		`, tenantID, ids)
		if qErr != nil {
			return qErr
		}
		defer rows.Close()
		type delMeta struct {
			by      *uuid.UUID
			name    string
			cleared bool
		}
		meta := make(map[uuid.UUID]delMeta, len(ids))
		for rows.Next() {
			var (
				id uuid.UUID
				m  delMeta
			)
			if sErr := rows.Scan(&id, &m.by, &m.name, &m.cleared); sErr != nil {
				return sErr
			}
			meta[id] = m
		}
		if rErr := rows.Err(); rErr != nil {
			return rErr
		}
		for i := range out.Items {
			if m, ok := meta[out.Items[i].ID]; ok {
				out.Items[i].DeletedBy = m.by
				out.Items[i].DeletedByName = m.name
				out.Items[i].UserCleared = m.cleared
			}
		}
		return nil
	})
	return out, err
}

// RestoreDocument clears the soft-delete flag so the document
// reappears in its original workspace. Legal hold blocks restore the
// same way it blocks delete — a doc that was held when deleted needs
// the hold released before the row is "alive" again.
func (s *DocumentService) RestoreDocument(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Documents.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if cur.DeletedAt == nil {
			return vdmserr.ErrNotFound
		}
		if s.holds != nil {
			held, herr := s.holds.AnyActiveHoldFor(ctx, tenantID, id)
			if herr != nil {
				return fmt.Errorf("hold check: %w", herr)
			}
			if held {
				return vdmserr.ErrLegalHold
			}
		}
		// Retention guard (handoff Track 1): once the retention window has
		// elapsed the document is eligible for disposal — restoring it would
		// resurrect something the retention pipeline owns. 409.
		if until, exempt, rerr := s.retentionInfo(ctx, tx, tenantID, id); rerr != nil {
			return rerr
		} else if until != nil && !exempt && until.Before(time.Now().UTC()) {
			return vdmserr.Conflict("retention period has elapsed; document is past its restoration window")
		}
		if err := s.repos.Documents.Restore(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.restored.v1", "document", id,
			map[string]any{
				"document_id": id.String(),
				"restored_by": userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// RestoreOwnDocument is the member-facing restore: it reuses RestoreDocument
// (and therefore every legal-hold and retention guard) but first proves the
// caller is the person who deleted the document and hasn't already cleared
// it. Without that ownership check this would be an unauthenticated
// tenant-wide restore, since RestoreDocument itself is role-gated at the
// handler and does no per-row authorization.
func (s *DocumentService) RestoreOwnDocument(ctx context.Context, id uuid.UUID) error {
	if err := s.assertOwnTrashItem(ctx, id); err != nil {
		return err
	}
	return s.RestoreDocument(ctx, id)
}

// ClearOwnDocument removes a document from the caller's own Trash. It is
// deliberately NOT a purge: the row and its bytes stay put, the admin Trash
// keeps listing it (flagged "cleared by user"), and an admin can still
// restore it. Members therefore have no route to destroy content.
func (s *DocumentService) ClearOwnDocument(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if err := s.assertOwnTrashItem(ctx, id); err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.repos.Documents.ClearFromUserTrash(ctx, tx, tenantID, id, userID); err != nil {
			return err
		}
		// Audited: from the member's point of view this is "delete
		// permanently", so the trail has to show who asked for it even
		// though nothing was destroyed.
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.trash_cleared.v1", "document", id,
			map[string]any{
				"document_id": id.String(),
				"cleared_by":  userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// assertOwnTrashItem returns ErrNotFound unless the document is soft-deleted,
// was deleted by the caller, and is still in the caller's Trash. NotFound
// rather than Forbidden so the member surface can't be used to probe for
// documents in workspaces the caller can't see.
func (s *DocumentService) assertOwnTrashItem(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var ok bool
		qErr := tx.QueryRow(ctx, `
			SELECT EXISTS (
			    SELECT 1 FROM documents
			     WHERE tenant_id = $1 AND id = $2
			       AND deleted_at IS NOT NULL
			       AND deleted_by = $3
			       AND user_cleared_at IS NULL
			)`, tenantID, id, userID).Scan(&ok)
		if qErr != nil {
			return qErr
		}
		if !ok {
			return vdmserr.ErrNotFound
		}
		return nil
	})
}

// retentionInfo reads the document's retention window (the retention pipeline's
// source-of-truth). Used by restore + purge so trash operations stay aligned
// with retention: under-retention docs can't be purged, retention-elapsed docs
// can't be restored. retention_exempt short-circuits both.
func (s *DocumentService) retentionInfo(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (until *time.Time, exempt bool, err error) {
	err = tx.QueryRow(ctx,
		`SELECT retention_until, retention_exempt FROM documents WHERE tenant_id = $1 AND id = $2`,
		tenantID, id).Scan(&until, &exempt)
	return until, exempt, err
}

// purgeGuard enforces the invariants that block a hard delete:
// active legal hold and an unelapsed retention window. Shared by the
// per-document purge, the folder-cohort purge, and Empty Trash so all
// destructive paths fail closed identically.
func (s *DocumentService) purgeGuard(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	if s.holds != nil {
		held, hErr := s.holds.AnyActiveHoldFor(ctx, tenantID, id)
		if hErr != nil {
			return fmt.Errorf("hold check: %w", hErr)
		}
		if held {
			return vdmserr.ErrLegalHold
		}
	}
	// Retention guard (handoff Track 1): never hard-delete a document still
	// inside its retention window — the retention pipeline owns disposal,
	// and the trash UI must not expose a way to bypass it. 409.
	if until, exempt, rErr := s.retentionInfo(ctx, tx, tenantID, id); rErr != nil {
		return rErr
	} else if until != nil && !exempt && until.After(time.Now().UTC()) {
		return vdmserr.Conflict("document is under active retention; the retention pipeline owns disposal")
	}
	return nil
}

// PurgeDocument hard-deletes a soft-deleted document. Removes the S3
// blob bytes first (best-effort — a missing object is not an error so
// re-runs of a partially-failed purge complete), then drops the DB
// rows in a single tx. Legal hold blocks the purge unconditionally;
// the doc must not be currently active (deleted_at IS NOT NULL).
// Requires admin/owner — gated at the handler layer.
func (s *DocumentService) PurgeDocument(ctx context.Context, id uuid.UUID) error {
	if s.s3 == nil {
		return fmt.Errorf("purge unavailable: s3 client not configured")
	}
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	// Step 1 — collect blob targets + verify state under a tx.
	var blobs []struct {
		BlobID uuid.UUID
		Bucket string
		Key    string
	}
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, gErr := s.repos.Documents.GetByID(ctx, tx, tenantID, id)
		if gErr != nil {
			return gErr
		}
		if cur.DeletedAt == nil {
			return vdmserr.Validation("document", "must be soft-deleted before purge")
		}
		if gErr := s.purgeGuard(ctx, tx, tenantID, id); gErr != nil {
			return gErr
		}
		bs, bErr := s.repos.Documents.BlobsForDocument(ctx, tx, tenantID, id)
		if bErr != nil {
			return bErr
		}
		blobs = bs
		return nil
	}); err != nil {
		return err
	}
	// Step 2 — delete object bytes. DeleteObject returns nil on
	// NoSuchKey so a re-run after a half-failed purge completes.
	for _, b := range blobs {
		if err := s.s3.DeleteObject(ctx, b.Bucket, b.Key); err != nil {
			return fmt.Errorf("s3 delete %s/%s: %w", b.Bucket, b.Key, err)
		}
	}
	// Step 3 — drop DB rows + emit audit in one tx. Cascading FKs
	// remove ocr_results, document_chunks, etc.
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.repos.Documents.DeleteVersionsAndBlobs(ctx, tx, tenantID, id); err != nil {
			return err
		}
		if err := s.repos.Documents.HardDelete(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, evtErr := model.NewOutboxEvent(tenantID, "dms.document.purged.v1", "document", id,
			map[string]any{
				"document_id": id.String(),
				"purged_by":   userID.String(),
				"blob_count":  len(blobs),
			})
		if evtErr != nil {
			return evtErr
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// EmptyTrashSkipped records one trash item Empty Trash could not purge
// and why (legal hold, active retention, live references, …).
type EmptyTrashSkipped struct {
	ID     uuid.UUID `json:"id"`
	Type   string    `json:"type"` // "folder" | "document"
	Name   string    `json:"name,omitempty"`
	Reason string    `json:"reason"`
}

// EmptyTrashResult summarizes an Empty Trash run.
type EmptyTrashResult struct {
	PurgedFolders   int                `json:"purged_folders"`
	PurgedDocuments int                `json:"purged_documents"`
	Skipped         []EmptyTrashSkipped `json:"skipped"`
}

// emptyTrashMaxDocs bounds one Empty Trash call; a tenant with more
// trashed docs than this finishes on the next click.
const emptyTrashMaxDocs = 10000

// EmptyTrash permanently deletes everything in the tenant's trash:
// every folder cohort via PurgeFolder, then every remaining trashed
// document via PurgeDocument. Items blocked by legal hold / retention
// / live references are skipped and reported, never fatal — an admin
// emptying the trash should not be stopped by the one held document.
// Requires admin/owner — gated at the handler layer.
func (s *DocumentService) EmptyTrash(ctx context.Context) (*EmptyTrashResult, error) {
	if s.s3 == nil {
		return nil, fmt.Errorf("purge unavailable: s3 client not configured")
	}
	res := &EmptyTrashResult{Skipped: []EmptyTrashSkipped{}}
	// skippable: expected per-item refusals (hold, retention, conflict)
	// become skip entries; anything else (S3 down, DB error) aborts.
	skippable := func(err error) (string, bool) {
		var typed *vdmserr.Error
		if errors.As(err, &typed) {
			return typed.Message, true
		}
		return "", false
	}

	folders, err := s.ListTrashFolders(ctx)
	if err != nil {
		return nil, err
	}
	for i := range folders {
		f := &folders[i]
		fr, pErr := s.PurgeFolder(ctx, f.ID)
		if pErr != nil {
			if errors.Is(pErr, vdmserr.ErrNotFound) {
				continue // already gone (nested cohort purged via an earlier root)
			}
			if reason, ok := skippable(pErr); ok {
				res.Skipped = append(res.Skipped, EmptyTrashSkipped{
					ID: f.ID, Type: "folder", Name: f.Name, Reason: reason,
				})
				continue
			}
			return nil, pErr
		}
		res.PurgedFolders++
		res.PurgedDocuments += fr.DocumentsDeleted
	}

	// Remaining trashed documents: deleted individually, or members of a
	// skipped cohort. Purging a skipped cohort's unblocked docs here is
	// intentional — Empty Trash removes everything removable; only the
	// blocked docs (and their folder rows) stay behind.
	docIDs := make([]uuid.UUID, 0, 256)
	titles := make(map[uuid.UUID]string)
	pageToken := ""
	for len(docIDs) < emptyTrashMaxDocs {
		page, lErr := s.ListTrash(ctx, 200, pageToken)
		if lErr != nil {
			return nil, lErr
		}
		for i := range page.Items {
			docIDs = append(docIDs, page.Items[i].ID)
			titles[page.Items[i].ID] = page.Items[i].Title
		}
		if page.NextPageToken == "" {
			break
		}
		pageToken = page.NextPageToken
	}
	for _, docID := range docIDs {
		if pErr := s.PurgeDocument(ctx, docID); pErr != nil {
			if errors.Is(pErr, vdmserr.ErrNotFound) {
				continue // purged with its cohort above
			}
			if reason, ok := skippable(pErr); ok {
				res.Skipped = append(res.Skipped, EmptyTrashSkipped{
					ID: docID, Type: "document", Name: titles[docID], Reason: reason,
				})
				continue
			}
			return nil, pErr
		}
		res.PurgedDocuments++
	}
	return res, nil
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
		if err := s.blockedByRecord(ctx, tenantID, cur.ID); err != nil {
			return err
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
		// Phase 2 — visibility / grants gate. The OPA workspace +
		// folder grant rules above don't know about per-folder
		// privacy. If the target is a private folder the caller
		// can't access, refuse before touching the row.
		isAdmin := s.callerIsTenantAdmin(ctx)
		if okSrc, cerr := s.repos.Folders.CanAccessFolder(ctx, tx, tenantID, cur.FolderID, userID, nil, isAdmin); cerr != nil {
			return cerr
		} else if !okSrc {
			return vdmserr.ErrNotFound
		}
		if okDst, cerr := s.repos.Folders.CanAccessFolder(ctx, tx, tenantID, in.TargetFolderID, userID, nil, isAdmin); cerr != nil {
			return cerr
		} else if !okDst {
			return vdmserr.ErrNotFound
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

// CopyDocument creates a NEW document row in the target folder that
// points at the same content_blob as the source document's current
// version. Shallow copy: only the current version is brought over,
// history / share-links / comments / annotations / OCR / chunks
// stay with the original.
//
// Region pin follows the target folder's workspace — the new row is
// a fresh creation, not a relocation, so the residency restriction
// that blocks moving across regions doesn't apply.
//
// Blocked by legal hold (same as Move). Requires edit permission on
// the target folder. The source is read-only here so we only need
// "view" on it; current_user is implicit (mustCaller).
func (s *DocumentService) CopyDocument(ctx context.Context, in *CopyDocumentInput) (*model.Document, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || in.DocumentID == uuid.Nil || in.TargetFolderID == uuid.Nil {
		return nil, errInvalidInput("document_id", "required")
	}

	var out *model.Document
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		src, err := s.repos.Documents.GetByID(ctx, tx, tenantID, in.DocumentID)
		if err != nil {
			return err
		}
		if src.DeletedAt != nil {
			return vdmserr.ErrNotFound
		}
		if model.IsLegalHoldBlocked(src.LifecycleState, "copy") {
			return vdmserr.ErrLegalHold
		}
		targetFolder, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.TargetFolderID)
		if err != nil {
			return err
		}
		targetWS := src.WorkspaceID
		if in.TargetWorkspaceID != nil {
			targetWS = *in.TargetWorkspaceID
		}
		if targetFolder.WorkspaceID != targetWS {
			return vdmserr.Validation("target_folder_id", "folder is not in the target workspace")
		}
		// Caller must be able to read the source + edit the target.
		if err := s.requirePermission(ctx, userID, "view", "document", src.ID, map[string]any{
			"workspace_id": src.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "folder", in.TargetFolderID, map[string]any{
			"workspace_id": targetWS.String(),
		}); err != nil {
			return err
		}
		// Phase 2 — visibility gate on the target folder. The source
		// is already accessible (we ran requirePermission view above)
		// so we only need to check the destination here.
		isAdmin := s.callerIsTenantAdmin(ctx)
		if okDst, cerr := s.repos.Folders.CanAccessFolder(ctx, tx, tenantID, in.TargetFolderID, userID, nil, isAdmin); cerr != nil {
			return cerr
		} else if !okDst {
			return vdmserr.ErrNotFound
		}
		now := time.Now().UTC()
		newID, _ := newExternalID()
		dst := &model.Document{
			ID:             newID,
			TenantID:       tenantID,
			WorkspaceID:    targetWS,
			FolderID:       in.TargetFolderID,
			Title:          src.Title,
			Description:    src.Description,
			LifecycleState: model.StateDraft,
			// Inherit region from the source. Cross-region copy would
			// require a residency migration; that's out of scope for
			// the user-facing Copy action.
			RegionPin:                src.RegionPin,
			CustomMetadata:           src.CustomMetadata,
			Tags:                     append([]string(nil), src.Tags...),
			CurrentVersionID:         src.CurrentVersionID,
			DocumentClass:            src.DocumentClass,
			ClassificationConfidence: src.ClassificationConfidence,
			SHA256Hash:               src.SHA256Hash,
			TotalSizeBytes:           src.TotalSizeBytes,
			MimeType:                 src.MimeType,
			CreatedBy:                in.CopiedBy,
			CreatedAt:                now,
			UpdatedBy:                in.CopiedBy,
			UpdatedAt:                now,
		}
		if err := s.repos.Documents.Create(ctx, tx, dst); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.document.copied.v1", "document", dst.ID,
			map[string]any{
				"document_id":         dst.ID.String(),
				"source_document_id":  src.ID.String(),
				"source_workspace_id": src.WorkspaceID.String(),
				"target_workspace_id": targetWS.String(),
				"target_folder_id":    in.TargetFolderID.String(),
				"copied_by":           in.CopiedBy.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		out = dst
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
	// Two authorization paths:
	//   A) Standard workspace permission via OPA (members + admins).
	//   B) Folder-grantee scoping — caller has no workspace permission
	//      but is querying a SPECIFIC folder they hold a grant on.
	//      Workspace-wide listing without folder_id stays gated; this
	//      keeps grantee-only callers from enumerating documents
	//      outside their granted folder.
	if err := s.requirePermission(ctx, userID, "view", "workspace", *f.WorkspaceID, nil); err != nil {
		if !errors.Is(err, vdmserr.ErrForbidden) {
			return nil, err
		}
		if f.FolderID == nil || *f.FolderID == uuid.Nil {
			return nil, err
		}
		hasGrant, gerr := s.callerHasDirectGrantOnFolder(ctx, tenantID, *f.FolderID, userID)
		if gerr != nil {
			return nil, gerr
		}
		if !hasGrant {
			return nil, err
		}
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
	if err != nil {
		return nil, err
	}
	// Per-row ACL (Workstream 7). The gate above authorizes the LISTING
	// (workspace view or a folder grant), but individual rows may sit in
	// private folders or carry document-level restrictions the caller can't
	// read. Batch-check "view" on every returned row in ONE round-trip and drop
	// the ones OPA denies, so a list never leaks a document the caller can't
	// open. Tenant admins/owners see everything, so skip the call for them.
	// NextPageToken is unaffected (it keys on the last raw row), so a filtered
	// page may be short — the client keeps paging until the token is empty.
	if page != nil && len(page.Items) > 0 && !s.callerIsTenantAdmin(ctx) {
		visible, ferr := s.filterViewableDocuments(ctx, userID, page.Items)
		if ferr != nil {
			return nil, ferr
		}
		page.Items = visible
	}
	return page, nil
}

// filterViewableDocuments returns the subset of docs the caller may "view",
// resolved with a single BatchCheckPermission (one check per document). Fails
// closed: a policy-transport error returns the error rather than risk leaking
// rows. Mirrors the OPA context built by requireDocPermission so workspace /
// lifecycle / region / hold rules evaluate correctly per row.
func (s *DocumentService) filterViewableDocuments(ctx context.Context, userID uuid.UUID, docs []model.Document) ([]model.Document, error) {
	// §8: load the classification gate once so per-row checks also drop
	// documents the caller lacks clearance for (no-op when gating is disabled).
	var gate *classGate
	if tid, terr := auth.GetTenantID(ctx); terr == nil && tid != uuid.Nil {
		g, gerr := s.loadClassGate(ctx, tid, userID)
		if gerr != nil {
			return nil, gerr
		}
		gate = g
	}
	checks := make([]*sedocv1.CheckPermissionRequest, 0, len(docs))
	for i := range docs {
		d := &docs[i]
		row := map[string]any{
			"workspace_id":     d.WorkspaceID.String(),
			"lifecycle_state":  string(d.LifecycleState),
			"region_pin":       d.RegionPin,
			"classification":   d.DocumentClass,
			"under_legal_hold": d.LifecycleState == model.StateLegalHold,
			"user_role":        auth.GetUserRole(ctx),
		}
		for k, v := range gate.contextFor(d, "view") {
			row[k] = v
		}
		ctxStruct, err := structpb.NewStruct(stringifyMap(row))
		if err != nil {
			return nil, fmt.Errorf("build context struct: %w", err)
		}
		checks = append(checks, &sedocv1.CheckPermissionRequest{
			SubjectType:  "user",
			SubjectId:    userID.String(),
			Action:       "view",
			ResourceType: "document",
			ResourceId:   d.ID.String(),
			Context:      ctxStruct,
		})
	}

	pairs := []string{"x-user-id", userID.String()}
	if tid, terr := auth.GetTenantID(ctx); terr == nil && tid != uuid.Nil {
		pairs = append(pairs, "x-tenant-id", tid.String())
	}
	if name := auth.GetUserName(ctx); name != "" {
		pairs = append(pairs, "x-user-name", name)
	}
	if role := auth.GetUserRole(ctx); role != "" {
		pairs = append(pairs, "x-user-role", role)
	}
	octx := metadata.AppendToOutgoingContext(ctx, pairs...)

	resp, err := s.policy.BatchCheckPermission(octx, &sedocv1.BatchCheckPermissionRequest{Checks: checks})
	if err != nil {
		// Fail closed: surface the error instead of returning unfiltered rows.
		return nil, fmt.Errorf("per-row permission check unavailable: %w", err)
	}
	results := resp.GetResults()
	out := docs[:0]
	for i := range docs {
		if i < len(results) && results[i].GetAllowed() {
			out = append(out, docs[i])
		}
	}
	return out, nil
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
		// Optimistic concurrency (§sync): if the caller declared the version it
		// based this edit on and the head has since moved, reject so the sync
		// client can raise a conflict the user resolves. Re-read the head under a
		// row lock (FOR UPDATE) so two writers on the same base can't both pass
		// the check — the loser blocks, then sees the moved head and gets 409
		// (rather than an opaque UNIQUE violation from the version insert).
		if in.BaseVersionID != nil {
			var cur *uuid.UUID
			if err := tx.QueryRow(ctx,
				`SELECT current_version_id FROM documents WHERE tenant_id=$1 AND id=$2 FOR UPDATE`,
				tenantID, doc.ID).Scan(&cur); err != nil {
				return err
			}
			if StaleBaseVersion(in.BaseVersionID, cur) {
				return vdmserr.Conflict("document was modified concurrently; base version is stale")
			}
		}
		if model.IsLegalHoldBlocked(doc.LifecycleState, "create_version") {
			return vdmserr.ErrLegalHold
		}
		// Records immutability: no new versions on a declared record.
		if err := s.blockedByRecord(ctx, tenantID, doc.ID); err != nil {
			return err
		}
		// WORM object-lock: cannot overwrite (new version) before retention.
		if err := s.blockedByWORM(ctx, tenantID, doc.ID); err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "document", doc.ID, map[string]any{
			"workspace_id":    doc.WorkspaceID.String(),
			"lifecycle_state": string(doc.LifecycleState),
		}); err != nil {
			return err
		}

		v, err := s.appendVersionLocked(ctx, tx, tenantID, userID, doc,
			in.ContentBlobID, in.SizeBytes, in.SHA256Hash, in.MimeType,
			in.CreatedByName, in.ChangeSummary)
		if err != nil {
			return err
		}
		out = v
		return nil
	})
	return out, err
}

// appendVersionLocked reserves the next version number for doc, inserts the
// immutable version row, repoints the document head to it, and emits the
// canonical dms.version.uploaded.v1 event (+ an uploader inbox notify) — all
// inside the caller's transaction. The caller owns the permission +
// legal-hold checks and must have resolved the blob's canonical size/sha/mime
// (the content_blobs row is the source of truth). Shared by CreateVersion and
// the upsert-by-external-key append path so both produce identical version
// rows + events. The UNIQUE(document_id, version_number) constraint serializes
// concurrent writers; the loser surfaces as ErrAlreadyExists.
func (s *DocumentService) appendVersionLocked(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, doc *model.Document, blobID uuid.UUID, size int64, sha, mime, createdByName, changeSummary string) (*model.Version, error) {
	n, err := s.repos.Versions.NextVersionNumber(ctx, tx, tenantID, doc.ID)
	if err != nil {
		return nil, err
	}
	vid, err := newExternalID()
	if err != nil {
		return nil, err
	}
	v := &model.Version{
		TenantID:      tenantID,
		ID:            vid,
		DocumentID:    doc.ID,
		VersionNumber: n,
		ContentBlobID: blobID,
		SizeBytes:     size,
		MimeType:      mime,
		SHA256Hash:    sha,
		CreatedBy:     userID,
		CreatedByName: createdByName,
		CreatedAt:     time.Now().UTC(),
		ChangeSummary: changeSummary,
	}
	if err := s.repos.Versions.Create(ctx, tx, v); err != nil {
		return nil, err
	}
	if err := s.repos.Documents.SetCurrentVersion(ctx, tx, tenantID, doc.ID, v.ID, v.SHA256Hash, v.MimeType, v.SizeBytes); err != nil {
		return nil, err
	}

	// ADR 0021: emit dms.version.uploaded.v1 (not the legacy
	// dms.version.created.v1) so the intelligence pipeline — OCR,
	// classify, embed, preview — has a single canonical trigger.
	// storage_uri is composed from the content_blobs row in the same tx.
	storageURI, err := s.lookupBlobURI(ctx, tx, tenantID, blobID)
	if err != nil {
		return nil, fmt.Errorf("lookup blob uri: %w", err)
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
			UploadedAt:       v.CreatedAt.Format(time.RFC3339),
		})
	if err != nil {
		return nil, err
	}
	if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
		return nil, err
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
		return nil, err
	}
	if err := s.repos.Outbox.Insert(ctx, tx, notifyEvt); err != nil {
		return nil, err
	}
	return v, nil
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
		// Records immutability: no version restore on a declared record.
		if err := s.blockedByRecord(ctx, tenantID, documentID); err != nil {
			return err
		}
		// WORM object-lock: cannot overwrite (restore) before retention.
		if err := s.blockedByWORM(ctx, tenantID, documentID); err != nil {
			return err
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
		vid, err := newExternalID()
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

		// Binding-table legal hold FREEZES every lifecycle transition (the
		// §lifecycle invariant), except hold management itself. ValidateTransition
		// only catches the in-band legal_hold STATE; a legal hold placed via the
		// compliance API (HoldsService) records into legal_hold_documents WITHOUT
		// changing lifecycle_state, so without this check a held document could be
		// archived / superseded / disposed — a spoliation event. Read of committed
		// state, before the action runs.
		if in.Action != model.ActionApplyHold && in.Action != model.ActionReleaseHold && s.holds != nil {
			held, herr := s.holds.AnyActiveHoldFor(ctx, tenantID, doc.ID)
			if herr != nil {
				return herr
			}
			if held {
				return vdmserr.ErrLegalHold
			}
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
			// A declared record must be disposed through the certified records
			// disposition ceremony (records.Service.Dispose), not this generic
			// path — otherwise disposition skips certification + cutoff. And a
			// WORM object-lock forbids destroy before its retention date.
			if err := s.blockedByRecord(ctx, tenantID, doc.ID); err != nil {
				return err
			}
			if err := s.blockedByWORM(ctx, tenantID, doc.ID); err != nil {
				return err
			}

		case model.ActionApplyHold:
			if in.HoldName == "" {
				return errInvalidInput("hold_name", "required for apply_hold")
			}
			if err := s.requirePermission(ctx, userID, "admin", "document", doc.ID, nil); err != nil {
				return err
			}
			holdID, err := newExternalID()
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
