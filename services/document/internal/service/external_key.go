package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// External-key surface (Workstream "Stable external key + upsert-by-external-
// key"). The ERP speaks in business keys (e.g. "INV-2024-00188"), not SeDoc
// UUIDs. These methods let it create-or-version a document keyed on that
// stable external_id, and resolve the internal ids back from the key. The
// blob bytes are uploaded out-of-band via the storage flow (which dedupes by
// sha256); the upsert re-links the existing content-addressed blob rather
// than re-uploading.

// validateExternalID enforces the business-key shape. Empty is allowed on the
// standard create path (optional); the upsert path requires it separately.
func validateExternalID(s string) error {
	if s == "" {
		return nil
	}
	if len(s) > 255 {
		return vdmserr.Validation("external_id", "max 255 characters")
	}
	if strings.TrimSpace(s) == "" {
		return vdmserr.Validation("external_id", "must not be blank")
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return vdmserr.Validation("external_id", "must not contain control characters")
		}
	}
	return nil
}

// UpsertByExternalKeyInput is the request for an ERP create-or-version call.
// WorkspaceID comes from the route; ExternalID + the blob descriptor are the
// load-bearing fields. Title defaults to ExternalID on create. DocType is
// folded into custom_metadata["doc_type"]; DocumentClass maps to the
// first-class column.
type UpsertByExternalKeyInput struct {
	ExternalID     string
	WorkspaceID    uuid.UUID
	FolderID       uuid.UUID
	Title          string
	DocumentClass  string
	DocType        string
	Tags           []string
	CustomMetadata map[string]any
	RegionPin      string

	// version descriptor — blob bytes already uploaded via storage.
	BlobChecksum  string // sha256 hex; also the no-op comparison key
	BlobRef       string // optional content_blob UUID; verified against checksum
	Mime          string // fallback only; the blob row is canonical
	Size          int64  // advisory; the blob row is canonical
	ChangeSummary string

	UpdatedBy uuid.UUID
}

// UpsertByExternalKeyResult reports what the upsert did. Created is true only
// when a brand-new document was minted; VersionCreated is false on the
// same-bytes no-op.
type UpsertByExternalKeyResult struct {
	DocumentID           uuid.UUID
	CurrentVersionID     uuid.UUID
	CurrentVersionNumber int
	Created              bool
	VersionCreated       bool
}

// UpsertDocumentByExternalKey is the transactional create-or-version entry
// point keyed on a tenant-unique business key.
//
//   - no existing doc            → create document + version 1
//   - existing + same checksum   → no-op, return the current version
//   - existing + new checksum    → append the next version, repoint head
//
// Concurrency: the create branch inserts ON CONFLICT DO NOTHING against the
// partial unique index, so two simultaneous first-creations converge on one
// document — the loser re-reads the winner's row (locked) and falls through
// to the append/no-op branch. Appends against the same key serialize on the
// FOR UPDATE row lock.
func (s *DocumentService) UpsertDocumentByExternalKey(ctx context.Context, in *UpsertByExternalKeyInput) (*UpsertByExternalKeyResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	// documents/versions carry a created_by FK to users — the ERP push
	// must act as a real principal (X-User-ID / API-key user), never the
	// anonymous internal-service identity.
	if userID == uuid.Nil {
		return nil, vdmserr.ErrUnauthorized
	}
	if strings.TrimSpace(in.ExternalID) == "" {
		return nil, errInvalidInput("external_id", "required")
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
	if err := validateRegion(in.RegionPin); err != nil {
		return nil, err
	}
	checksum := strings.ToLower(strings.TrimSpace(in.BlobChecksum))
	if checksum == "" {
		return nil, errInvalidInput("version.blob_checksum", "required")
	}

	meta := in.CustomMetadata
	if meta == nil {
		meta = map[string]any{}
	}
	if in.DocType != "" {
		meta["doc_type"] = in.DocType
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = in.ExternalID
	}
	if err := validateTitle(title); err != nil {
		return nil, err
	}
	region := in.RegionPin
	if region == "" {
		region = "us-east-1"
	}

	var res UpsertByExternalKeyResult
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		existing, gerr := s.repos.Documents.GetByExternalID(ctx, tx, tenantID, in.ExternalID, true)
		if gerr != nil && !errors.Is(gerr, vdmserr.ErrNotFound) {
			return gerr
		}

		// ---- create branch ------------------------------------------------
		if existing == nil {
			if perr := s.requirePermission(ctx, userID, "edit", "folder", in.FolderID, map[string]any{
				"workspace_id": in.WorkspaceID.String(),
			}); perr != nil {
				return perr
			}
			schemaJSON, serr := s.repos.MetadataSchema.Get(ctx, tx, tenantID)
			if serr != nil {
				return serr
			}
			if verr := validateMetadataAgainstSchema(schemaJSON, meta); verr != nil {
				return verr
			}
			folder, ferr := s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
			if ferr != nil {
				return ferr
			}
			if folder.WorkspaceID != in.WorkspaceID {
				return vdmserr.Validation("folder_id", "folder is not in the given workspace")
			}
			id, ierr := newExternalID()
			if ierr != nil {
				return ierr
			}
			now := time.Now().UTC()
			doc := &model.Document{
				TenantID:       tenantID,
				ID:             id,
				ExternalID:     in.ExternalID,
				WorkspaceID:    in.WorkspaceID,
				FolderID:       in.FolderID,
				Title:          title,
				LifecycleState: model.StateDraft,
				RegionPin:      region,
				CustomMetadata: meta,
				Tags:           in.Tags,
				DocumentClass:  in.DocumentClass,
				CreatedBy:      userID,
				CreatedAt:      now,
				UpdatedBy:      userID,
				UpdatedAt:      now,
			}
			created, cerr := s.repos.Documents.InsertUpsert(ctx, tx, doc)
			if cerr != nil {
				return cerr
			}
			if created {
				// Mirror CreateDocument's created event so the search
				// indexer lands the row with its folder ACL on event one.
				readableBy, readableUsers, readableGroups, rerr := s.computeFolderReaders(ctx, tx, tenantID, doc.FolderID, doc.WorkspaceID)
				if rerr != nil {
					s.log.Warn().Err(rerr).Str("doc", doc.ID.String()).Msg("compute readable_by failed; doc indexed without ACL")
					readableBy, readableUsers, readableGroups = nil, nil, nil
				}
				evt, eerr := model.NewOutboxEvent(tenantID, "dms.document.created.v1", "document", doc.ID,
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
				if eerr != nil {
					return eerr
				}
				if oerr := s.repos.Outbox.Insert(ctx, tx, evt); oerr != nil {
					return oerr
				}
				v, verr := s.appendBlobVersion(ctx, tx, tenantID, userID, doc, checksum, in.BlobRef, in.Mime, in.ChangeSummary)
				if verr != nil {
					return verr
				}
				res = UpsertByExternalKeyResult{
					DocumentID: doc.ID, CurrentVersionID: v.ID, CurrentVersionNumber: v.VersionNumber,
					Created: true, VersionCreated: true,
				}
				return nil
			}
			// Lost the create race — another upsert committed first.
			// Re-read (locked) and fall through to append/no-op.
			existing, gerr = s.repos.Documents.GetByExternalID(ctx, tx, tenantID, in.ExternalID, true)
			if gerr != nil {
				return gerr
			}
		}

		// ---- append / no-op branch ---------------------------------------
		if existing.DeletedAt != nil {
			return vdmserr.Conflict("external_id maps to a trashed document; restore or purge it first")
		}
		// Same bytes already the head → no-op.
		if existing.CurrentVersionID != nil && strings.EqualFold(existing.SHA256Hash, checksum) {
			cur, verr := s.repos.Versions.GetLatestByDocument(ctx, tx, tenantID, existing.ID)
			if verr != nil {
				return verr
			}
			res = UpsertByExternalKeyResult{
				DocumentID: existing.ID, CurrentVersionID: cur.ID, CurrentVersionNumber: cur.VersionNumber,
				Created: false, VersionCreated: false,
			}
			return nil
		}
		// Different bytes → append the next version.
		if model.IsLegalHoldBlocked(existing.LifecycleState, "create_version") {
			return vdmserr.ErrLegalHold
		}
		if perr := s.requirePermission(ctx, userID, "edit", "document", existing.ID, map[string]any{
			"workspace_id":    existing.WorkspaceID.String(),
			"lifecycle_state": string(existing.LifecycleState),
		}); perr != nil {
			return perr
		}
		v, verr := s.appendBlobVersion(ctx, tx, tenantID, userID, existing, checksum, in.BlobRef, in.Mime, in.ChangeSummary)
		if verr != nil {
			return verr
		}
		res = UpsertByExternalKeyResult{
			DocumentID: existing.ID, CurrentVersionID: v.ID, CurrentVersionNumber: v.VersionNumber,
			Created: false, VersionCreated: true,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &res, nil
}

// appendBlobVersion resolves the content-addressed blob for checksum/ref and
// appends it as the next version of doc, reusing the shared version-append
// path (so events + head-repointing match CreateVersion exactly).
func (s *DocumentService) appendBlobVersion(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, doc *model.Document, checksum, ref, mimeHint, changeSummary string) (*model.Version, error) {
	blobID, size, sha, mime, err := s.resolveContentBlobTx(ctx, tx, tenantID, checksum, ref)
	if err != nil {
		return nil, err
	}
	if mime == "" {
		mime = mimeHint
	}
	return s.appendVersionLocked(ctx, tx, tenantID, userID, doc, blobID, size, sha, mime, "", changeSummary)
}

// resolveContentBlobTx maps an ERP-supplied {checksum, ref} to an existing,
// content-addressed blob row and returns its canonical size/sha/mime. The
// document service never mints blobs (storage owns content_blobs + the
// envelope keys); the bytes must have been uploaded via the storage flow,
// which dedupes by sha256. Runs inside the caller's tenant tx so the
// content_blobs FORCE-RLS policy is satisfied by the SET LOCAL app.current_tenant.
func (s *DocumentService) resolveContentBlobTx(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, checksum, ref string) (blobID uuid.UUID, size int64, sha, mime string, err error) {
	// Optional explicit blob_ref (content_blob UUID): prefer it, but verify
	// its checksum so a stale ref can't relink the wrong bytes.
	if ref != "" {
		if rid, perr := uuid.Parse(ref); perr == nil {
			var (
				bSize int64
				bSHA  string
				bMime *string
			)
			qerr := tx.QueryRow(ctx,
				`SELECT size_bytes, sha256_hash, mime_type
				   FROM content_blobs
				  WHERE tenant_id = $1 AND id = $2 AND shredded_at IS NULL`,
				tenantID, rid).Scan(&bSize, &bSHA, &bMime)
			if qerr == nil {
				if !strings.EqualFold(bSHA, checksum) {
					return uuid.Nil, 0, "", "", errInvalidInput("version.blob_ref", "does not match version.blob_checksum")
				}
				return rid, bSize, bSHA, derefStr(bMime), nil
			}
			if !errors.Is(qerr, pgx.ErrNoRows) {
				return uuid.Nil, 0, "", "", qerr
			}
			// ref didn't resolve — fall back to the checksum lookup.
		}
	}
	var (
		bID   uuid.UUID
		bSize int64
		bSHA  string
		bMime *string
	)
	qerr := tx.QueryRow(ctx,
		`SELECT id, size_bytes, sha256_hash, mime_type
		   FROM content_blobs
		  WHERE tenant_id = $1 AND sha256_hash = $2 AND shredded_at IS NULL
		  ORDER BY created_at DESC
		  LIMIT 1`,
		tenantID, checksum).Scan(&bID, &bSize, &bSHA, &bMime)
	if errors.Is(qerr, pgx.ErrNoRows) {
		return uuid.Nil, 0, "", "", vdmserr.Validation("version.blob_checksum", "no uploaded content blob matches this checksum; upload the bytes via storage first")
	}
	if qerr != nil {
		return uuid.Nil, 0, "", "", qerr
	}
	return bID, bSize, bSHA, derefStr(bMime), nil
}

// GetDocumentByExternalKey resolves a business key back to the internal
// document + current-version ids, filtered by the caller's view permission.
// Returns ErrNotFound when the key is unknown OR the caller may not see it
// (existence is not leaked to unauthorized callers).
func (s *DocumentService) GetDocumentByExternalKey(ctx context.Context, externalID string) (documentID, currentVersionID uuid.UUID, err error) {
	tenantID, userID, merr := mustCaller(ctx)
	if merr != nil {
		return uuid.Nil, uuid.Nil, merr
	}
	if strings.TrimSpace(externalID) == "" {
		return uuid.Nil, uuid.Nil, errInvalidInput("external_id", "required")
	}
	var doc *model.Document
	if terr := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		d, gerr := s.repos.Documents.GetByExternalID(ctx, tx, tenantID, externalID, false)
		if gerr != nil {
			return gerr
		}
		doc = d
		return nil
	}); terr != nil {
		return uuid.Nil, uuid.Nil, terr
	}
	if doc.DeletedAt != nil {
		return uuid.Nil, uuid.Nil, vdmserr.ErrNotFound
	}
	perms, perr := s.summarizeDocumentPermissions(ctx, userID, doc.ID, map[string]any{
		"workspace_id":    doc.WorkspaceID.String(),
		"lifecycle_state": string(doc.LifecycleState),
		"region_pin":      doc.RegionPin,
	})
	if perr != nil {
		return uuid.Nil, uuid.Nil, perr
	}
	if !perms.CanView {
		return uuid.Nil, uuid.Nil, vdmserr.ErrNotFound
	}
	cur := uuid.Nil
	if doc.CurrentVersionID != nil {
		cur = *doc.CurrentVersionID
	}
	return doc.ID, cur, nil
}

func derefStr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
