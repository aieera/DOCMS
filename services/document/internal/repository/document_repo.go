package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

type documentRepo struct{}

// Create inserts a new document row. tenant_id is required in WHERE clauses
// on every subsequent query for defense-in-depth alongside RLS.
func (r *documentRepo) Create(ctx context.Context, tx pgx.Tx, d *model.Document) error {
	meta, err := json.Marshal(d.CustomMetadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO documents (
			id, tenant_id, workspace_id, folder_id, title, description,
			mime_type, total_size_bytes, sha256_hash, current_version_id,
			version_count, lifecycle_state, region_pin, under_legal_hold,
			tags, custom_metadata, document_class, classification_confidence,
			created_by, created_at, updated_by, updated_at, external_id, doc_type
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14,
			$15, $16, $17, $18,
			$19, $20, $21, $22, $23,
			COALESCE(NULLIF($24, ''), 'file')
		)`,
		d.ID, d.TenantID, d.WorkspaceID, d.FolderID, d.Title, d.Description,
		d.MimeType, d.TotalSizeBytes, d.SHA256Hash, nullableUUID(d.CurrentVersionID),
		0, string(d.LifecycleState), d.RegionPin, d.LifecycleState == model.StateLegalHold,
		d.Tags, meta, d.DocumentClass, d.ClassificationConfidence,
		d.CreatedBy, d.CreatedAt, d.UpdatedBy, d.UpdatedAt, nullableText(d.ExternalID), d.DocType,
	)
	return mapPgError(err)
}

// InsertUpsert inserts a new document but treats a (tenant_id, external_id)
// collision as a no-op, returning created=false. Callers use this for the
// create branch of an upsert: the partial unique index serializes concurrent
// first-creations so exactly one wins, and the loser re-reads the existing row
// and falls through to the append/no-op path. external_id MUST be non-empty —
// the ON CONFLICT target is the partial index WHERE external_id IS NOT NULL.
func (r *documentRepo) InsertUpsert(ctx context.Context, tx pgx.Tx, d *model.Document) (bool, error) {
	meta, err := json.Marshal(d.CustomMetadata)
	if err != nil {
		return false, fmt.Errorf("marshal metadata: %w", err)
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO documents (
			id, tenant_id, workspace_id, folder_id, title, description,
			mime_type, total_size_bytes, sha256_hash, current_version_id,
			version_count, lifecycle_state, region_pin, under_legal_hold,
			tags, custom_metadata, document_class, classification_confidence,
			created_by, created_at, updated_by, updated_at, external_id
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14,
			$15, $16, $17, $18,
			$19, $20, $21, $22, $23
		)
		ON CONFLICT (tenant_id, external_id) WHERE external_id IS NOT NULL DO NOTHING`,
		d.ID, d.TenantID, d.WorkspaceID, d.FolderID, d.Title, d.Description,
		d.MimeType, d.TotalSizeBytes, d.SHA256Hash, nullableUUID(d.CurrentVersionID),
		0, string(d.LifecycleState), d.RegionPin, d.LifecycleState == model.StateLegalHold,
		d.Tags, meta, d.DocumentClass, d.ClassificationConfidence,
		d.CreatedBy, d.CreatedAt, d.UpdatedBy, d.UpdatedAt, nullableText(d.ExternalID),
	)
	if err != nil {
		return false, mapPgError(err)
	}
	return tag.RowsAffected() == 1, nil
}

// GetByExternalID loads the document carrying the given tenant-scoped business
// key, or ErrNotFound when none exists. Unlike GetByID this is a lean,
// join-free projection (no users / workflow LATERAL) so it can take a
// FOR UPDATE row lock — the upsert path locks the row to serialize concurrent
// version appends against the same key. Only the fields the upsert decision
// needs are populated.
func (r *documentRepo) GetByExternalID(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, externalID string, forUpdate bool) (*model.Document, error) {
	if externalID == "" {
		return nil, vdmserr.ErrNotFound
	}
	q := `
		SELECT id, tenant_id, workspace_id, folder_id, title,
		       COALESCE(description, ''), lifecycle_state,
		       COALESCE(region_pin, ''), custom_metadata, tags,
		       current_version_id, COALESCE(document_class, ''),
		       COALESCE(sha256_hash, ''), COALESCE(mime_type, ''),
		       total_size_bytes, deleted_at, COALESCE(external_id, '')
		FROM documents
		WHERE tenant_id = $1 AND external_id = $2`
	if forUpdate {
		q += " FOR UPDATE"
	}
	var (
		d            model.Document
		curVersion   *uuid.UUID
		metaBytes    []byte
		deleted      *time.Time
		lifecycleRaw string
	)
	if err := tx.QueryRow(ctx, q, tenantID, externalID).Scan(
		&d.ID, &d.TenantID, &d.WorkspaceID, &d.FolderID, &d.Title,
		&d.Description, &lifecycleRaw, &d.RegionPin, &metaBytes, &d.Tags,
		&curVersion, &d.DocumentClass, &d.SHA256Hash, &d.MimeType,
		&d.TotalSizeBytes, &deleted, &d.ExternalID,
	); err != nil {
		return nil, mapPgError(err)
	}
	d.LifecycleState = model.LifecycleState(lifecycleRaw)
	d.CurrentVersionID = curVersion
	d.DeletedAt = deleted
	if len(metaBytes) > 0 {
		_ = json.Unmarshal(metaBytes, &d.CustomMetadata)
	}
	if d.CustomMetadata == nil {
		d.CustomMetadata = map[string]any{}
	}
	return &d, nil
}

// GetByID returns one document by id, enforcing tenant isolation. Includes
// soft-deleted rows; callers filter via DocumentFilter.IncludeDeleted.
func (r *documentRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Document, error) {
	// Same COALESCE story as List — scanDocument's plain-string scan
	// destinations (description / region_pin / document_class /
	// sha256_hash / mime_type) panic on NULL. Pre-Wave-16 rows + docs
	// without a completed first version both have NULLs in these
	// columns, so every GET on those rows would 500 without this.
	// LEFT JOIN users so the response carries the uploader's
	// display_name. The join is correlated on (tenant_id, created_by)
	// so RLS on users keeps tenant isolation intact even when the
	// document row is read by a tenant whose GUC matches both sides.
	// Soft-deleted users (deleted_at IS NOT NULL) still return their
	// display_name — preserving uploader attribution after deletion is
	// the explicit product requirement.
	// LEFT JOIN LATERAL pulls the most-recent non-terminal workflow
	// instance for this doc (at most one row by ORDER BY...LIMIT 1)
	// plus its template name. Adds 6 trailing nullable columns to the
	// projection; scanDocument scans them as pointers and stitches a
	// model.WorkflowInstanceSummary when present. No active workflow
	// → all NULLs → WorkflowInstance stays nil on the model.
	row := tx.QueryRow(ctx, `
		SELECT d.id, d.tenant_id, d.workspace_id, d.folder_id, d.title,
		       COALESCE(d.description, '') AS description,
		       d.lifecycle_state,
		       COALESCE(d.region_pin, '') AS region_pin,
		       d.custom_metadata, d.tags,
		       d.current_version_id,
		       COALESCE(d.document_class, '') AS document_class,
		       d.classification_confidence,
		       COALESCE(d.security_classification, '') AS security_classification,
		       d.has_phi, d.has_pii,
		       COALESCE(d.classification_source, '') AS classification_source,
		       COALESCE(d.sha256_hash, '') AS sha256_hash,
		       d.total_size_bytes, d.version_count,
		       COALESCE(d.mime_type, '') AS mime_type,
		       d.created_by, COALESCE(u.display_name, '') AS created_by_name,
		       d.created_at, d.updated_by, d.updated_at, d.deleted_at,
		       COALESCE(d.external_id, '') AS external_id,
		       wf.id, wf.definition_id, wf.definition_name, wf.status, wf.current_step_id, wf.started_at,
		       d.doc_type
		FROM documents d
		LEFT JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.created_by
		LEFT JOIN LATERAL (
		    SELECT i.id, i.definition_id, def.name AS definition_name,
		           i.status, i.current_step_id, i.started_at
		      FROM workflow_instances i
		      LEFT JOIN workflow_definitions def
		        ON def.tenant_id = i.tenant_id AND def.id = i.definition_id
		     WHERE i.tenant_id = d.tenant_id
		       AND i.document_id = d.id
		       AND i.status NOT IN ('completed','failed','cancelled')
		  ORDER BY i.started_at DESC
		     LIMIT 1
		) wf ON true
		WHERE d.tenant_id = $1 AND d.id = $2
	`, tenantID, id)
	return scanDocument(row)
}

// FindByContentHash returns live documents whose any version carries the
// given sha256. DISTINCT ON (d.id) collapses multiple matching versions
// of the same document to one row.
func (r *documentRepo) FindByContentHash(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, sha256 string, limit int) ([]model.DuplicateMatch, error) {
	if limit <= 0 || limit > 50 {
		limit = 50
	}
	q := `
		SELECT DISTINCT ON (d.id) d.id, d.title, d.workspace_id, d.folder_id, d.created_at
		FROM documents d
		JOIN versions v ON v.tenant_id = d.tenant_id AND v.document_id = d.id
		WHERE d.tenant_id = $1 AND d.deleted_at IS NULL AND v.sha256_hash = $2`
	args := []any{tenantID, sha256}
	if workspaceID != uuid.Nil {
		q += fmt.Sprintf(` AND d.workspace_id = $%d`, len(args)+1)
		args = append(args, workspaceID)
	}
	q += fmt.Sprintf(` ORDER BY d.id, d.created_at ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.DuplicateMatch
	for rows.Next() {
		var m model.DuplicateMatch
		if err := rows.Scan(&m.DocumentID, &m.Title, &m.WorkspaceID, &m.FolderID, &m.CreatedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, m)
	}
	return out, mapPgError(rows.Err())
}

// Update writes back the caller-editable columns of a document the service
// has loaded and mutated: title, description, folder/workspace placement,
// custom_metadata and tags (plus updated_by/at). It deliberately does NOT
// write the columns other paths own — current_version_id and the head
// sha256 / mime / size (version append), lifecycle_state + under_legal_hold
// (lifecycle transitions), region_pin, document_class /
// classification_confidence (classification) — so a stale in-memory copy
// can never revert a concurrent change to them (M-4). Callers lock the row
// (LockDocument) before reading it so the editable columns themselves are
// not lost either.
func (r *documentRepo) Update(ctx context.Context, tx pgx.Tx, d *model.Document) error {
	meta, err := json.Marshal(d.CustomMetadata)
	if err != nil {
		return fmt.Errorf("marshal metadata: %w", err)
	}
	if d.Tags == nil {
		d.Tags = []string{}
	}
	ct, err := tx.Exec(ctx, `
		UPDATE documents
		SET title = $3, description = $4, folder_id = $5, workspace_id = $6,
		    custom_metadata = $7, tags = $8, updated_by = $9, updated_at = $10
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`,
		d.TenantID, d.ID, d.Title, d.Description, d.FolderID, d.WorkspaceID,
		meta, d.Tags, d.UpdatedBy, d.UpdatedAt,
	)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// SoftDelete sets deleted_at and records who deleted the row so the
// Trash listing can show it (the cascade path stamps deleted_by the
// same way in SoftDeleteSubtree).
func (r *documentRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id, deletedBy uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET deleted_at = now(), deleted_by = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, deletedBy)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Restore clears deleted_at. Returns ErrNotFound if the row doesn't
// exist or is not currently soft-deleted. Also clears the cascade
// markers (deleted_cohort_id / deleted_by): a doc restored out of a
// folder cohort must not be swept up by a later purge or restore of
// that cohort.
func (r *documentRepo) Restore(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET deleted_at = NULL, deleted_cohort_id = NULL,
		                     deleted_by = NULL, user_cleared_at = NULL,
		                     user_cleared_by = NULL, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NOT NULL
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ClearFromUserTrash hides a soft-deleted document from the deleter's own
// Trash WITHOUT touching the bytes or the row. The admin Trash still lists
// it (flagged) and Restore still brings it back — that asymmetry is the
// whole point: a member's "delete permanently" must not be able to destroy
// something an admin may need back.
//
// Scoped to `deleted_by = clearedBy` so one member can never clear another
// member's item out of their view.
func (r *documentRepo) ClearFromUserTrash(ctx context.Context, tx pgx.Tx, tenantID, id, clearedBy uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET user_cleared_at = now(), user_cleared_by = $3
		WHERE tenant_id = $1 AND id = $2
		  AND deleted_at IS NOT NULL AND user_cleared_at IS NULL
		  AND deleted_by = $3
	`, tenantID, id, clearedBy)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// HardDelete removes the documents row outright. The caller is
// responsible for deleting downstream rows (versions, blobs) and
// the blob bytes from object storage; this only drops the parent.
func (r *documentRepo) HardDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		DELETE FROM documents WHERE tenant_id = $1 AND id = $2
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// BlobsForDocument returns one row per content blob backing any
// version of the document (bucket + key). Purge no longer uses it to
// pick objects to delete — a blob listed here may be shared with
// another document; ReleaseDocumentBlobs returns the only objects that
// are safe to remove.
func (r *documentRepo) BlobsForDocument(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]struct {
	BlobID uuid.UUID
	Bucket string
	Key    string
}, error,
) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT b.id, b.storage_bucket, b.storage_key
		FROM document_versions v
		JOIN content_blobs b
		  ON b.tenant_id = v.tenant_id AND b.id = v.content_blob_id
		WHERE v.tenant_id = $1 AND v.document_id = $2
	`, tenantID, docID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []struct {
		BlobID uuid.UUID
		Bucket string
		Key    string
	}{}
	for rows.Next() {
		var b struct {
			BlobID uuid.UUID
			Bucket string
			Key    string
		}
		if err := rows.Scan(&b.BlobID, &b.Bucket, &b.Key); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// DeleteVersionsAndBlobs drops the document's version rows and releases
// its blob references with the same shared-blob rules as
// ReleaseDocumentBlobs (it never touches a blob another document's version
// still points at). Unreferenced blobs are left at reference_count 0 for
// the storage blob reaper.
func (r *documentRepo) DeleteVersionsAndBlobs(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) error {
	_, err := ReleaseDocumentBlobs(ctx, tx, tenantID, docID)
	return err
}

// ---- Row locks + blob reference accounting --------------------------------
//
// Package-level functions rather than DocumentRepository methods: the
// interface lives in repository.go. They take the caller's tx like every
// other repo call and can be folded into the interface later.

// BlobObject names one content blob's object-store coordinates.
type BlobObject struct {
	BlobID uuid.UUID
	Bucket string
	Key    string
}

// LockDocument takes a row lock (SELECT … FOR UPDATE) on the document and
// returns its deleted_at. The lock lasts until the caller's transaction
// ends, so the read-modify-write that follows cannot interleave with a
// concurrent writer on the same row. ErrNotFound when the row is absent.
func LockDocument(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*time.Time, error) {
	var deletedAt *time.Time
	if err := tx.QueryRow(ctx, `
		SELECT deleted_at FROM documents
		 WHERE tenant_id = $1 AND id = $2
		   FOR UPDATE
	`, tenantID, id).Scan(&deletedAt); err != nil {
		return nil, mapPgError(err)
	}
	return deletedAt, nil
}

// LockTrashedDocuments row-locks the given documents (in id order, so two
// purges never deadlock) and returns the ids that are still soft-deleted
// once the locks are held. A document restored concurrently drops out.
func LockTrashedDocuments(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) ([]uuid.UUID, error) {
	out := []uuid.UUID{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id FROM documents
		 WHERE tenant_id = $1 AND id = ANY($2) AND deleted_at IS NOT NULL
		 ORDER BY id
		   FOR UPDATE
	`, tenantID, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, id)
	}
	return out, mapPgError(rows.Err())
}

// ReleaseDocumentBlobs drops every version row of the document and
// releases its references on the backing content blobs (DEC-8):
//
//   - each distinct blob the document's versions reference loses one
//     reference_count;
//   - a blob row is deleted only when its count reaches 0 AND no version of
//     any other document still references it; a blob that is still
//     referenced keeps a count of at least 1, so the storage reaper never
//     treats it as garbage even when an earlier relink skipped the
//     increment;
//   - a blob whose count reaches 0 and that no version of any document
//     still references is returned as released. Its row is NOT deleted
//     here: a content_blobs row at reference_count 0 is the storage
//     service's blob-reaper work queue, so committing the purge durably
//     records that the bytes must go. The caller may delete them right
//     after commit (LockReleasedBlob + DeleteBlobRow); whatever it does not
//     finish, the reaper does. Bytes of a surviving blob are never
//     returned.
//
// The document row stays (the caller hard-deletes it in the same tx); its
// current_version_id is cleared first because that pointer has no ON
// DELETE action.
func ReleaseDocumentBlobs(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) ([]BlobObject, error) {
	blobIDs := []uuid.UUID{}
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT content_blob_id FROM document_versions
		 WHERE tenant_id = $1 AND document_id = $2
	`, tenantID, docID)
	if err != nil {
		return nil, mapPgError(err)
	}
	for rows.Next() {
		var id uuid.UUID
		if scanErr := rows.Scan(&id); scanErr != nil {
			rows.Close()
			return nil, mapPgError(scanErr)
		}
		blobIDs = append(blobIDs, id)
	}
	rows.Close()
	if errErr := rows.Err(); errErr != nil {
		return nil, mapPgError(errErr)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE documents SET current_version_id = NULL
		 WHERE tenant_id = $1 AND id = $2
	`, tenantID, docID); err != nil {
		return nil, mapPgError(err)
	}
	// extracted_fields → document_versions has no ON DELETE action (the
	// table post-dates the 000064 cascade sweep); the values are derived
	// per version, so they go with it.
	if _, execErr := tx.Exec(ctx, `
		DELETE FROM extracted_fields
		 WHERE tenant_id = $1
		   AND version_id IN (SELECT id FROM document_versions
		                       WHERE tenant_id = $1 AND document_id = $2)
	`, tenantID, docID); execErr != nil {
		return nil, mapPgError(execErr)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM document_versions WHERE tenant_id = $1 AND document_id = $2
	`, tenantID, docID); err != nil {
		return nil, mapPgError(err)
	}
	if len(blobIDs) == 0 {
		return []BlobObject{}, nil
	}

	if _, execErr := tx.Exec(ctx, `
		UPDATE content_blobs b
		   SET reference_count = CASE
		         WHEN EXISTS (SELECT 1 FROM document_versions v
		                       WHERE v.tenant_id = b.tenant_id AND v.content_blob_id = b.id)
		         THEN GREATEST(b.reference_count - 1, 1)
		         ELSE GREATEST(b.reference_count - 1, 0)
		       END
		 WHERE b.tenant_id = $1 AND b.id = ANY($2)
	`, tenantID, blobIDs); execErr != nil {
		return nil, mapPgError(execErr)
	}
	rel, err := tx.Query(ctx, `
		SELECT b.id, b.storage_bucket, b.storage_key
		  FROM content_blobs b
		 WHERE b.tenant_id = $1 AND b.id = ANY($2)
		   AND b.reference_count = 0
		   AND NOT EXISTS (SELECT 1 FROM document_versions v
		                    WHERE v.tenant_id = b.tenant_id AND v.content_blob_id = b.id)
		 ORDER BY b.id
	`, tenantID, blobIDs)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rel.Close()
	out := []BlobObject{}
	for rel.Next() {
		var o BlobObject
		if err := rel.Scan(&o.BlobID, &o.Bucket, &o.Key); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, o)
	}
	return out, mapPgError(rel.Err())
}

// LockReleasedBlob row-locks a blob a purge released and reports whether it
// may still be deleted: reference_count 0 and no version of any document
// referencing it, re-checked AFTER the lock is held (a fresh statement, so
// a link committed while we waited is seen). While the lock is held a
// concurrent version link (its FK check) or a storage dedup increment
// waits. Returns the blob's object coordinates and region; ok=false (no
// error) when the blob is gone or referenced again.
func LockReleasedBlob(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID) (obj BlobObject, region string, ok bool, err error) {
	obj.BlobID = blobID
	if err := tx.QueryRow(ctx, `
		SELECT storage_bucket, storage_key, storage_region FROM content_blobs
		 WHERE tenant_id = $1 AND id = $2
		   FOR UPDATE
	`, tenantID, blobID).Scan(&obj.Bucket, &obj.Key, &region); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return obj, "", false, nil
		}
		return obj, "", false, mapPgError(err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT b.reference_count = 0
		       AND NOT EXISTS (SELECT 1 FROM document_versions v
		                        WHERE v.tenant_id = b.tenant_id AND v.content_blob_id = b.id)
		  FROM content_blobs b
		 WHERE b.tenant_id = $1 AND b.id = $2
	`, tenantID, blobID).Scan(&ok); err != nil {
		return obj, "", false, mapPgError(err)
	}
	return obj, region, ok, nil
}

// DeleteBlobRow drops a blob row the caller holds via LockReleasedBlob. The
// caller deletes the bytes only after this succeeds and commits only after
// the bytes are gone, so a failure at any step before commit leaves row and
// bytes together.
func DeleteBlobRow(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID) error {
	_, err := tx.Exec(ctx, `DELETE FROM content_blobs WHERE tenant_id = $1 AND id = $2`, tenantID, blobID)
	return mapPgError(err)
}

// ContentBlob is the subset of a content_blobs row (a table owned by the
// storage service's schema, readable inside the tenant GUC) the document
// service needs to link, region-check and delete a blob.
type ContentBlob struct {
	ID            uuid.UUID
	SHA256Hash    string
	SizeBytes     int64
	MimeType      string
	StorageRegion string
	StorageBucket string
	StorageKey    string
}

// GetContentBlob reads one live (not crypto-shredded) blob inside the
// caller's tenant tx. content_blobs is FORCE-RLS: a read outside the tenant
// GUC sees no rows under the production NOBYPASSRLS role. ErrNotFound when
// the blob is absent, shredded, or another tenant's.
func GetContentBlob(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*ContentBlob, error) {
	var (
		b    ContentBlob
		mime *string
	)
	if err := tx.QueryRow(ctx, `
		SELECT id, sha256_hash, size_bytes, mime_type,
		       storage_region, storage_bucket, storage_key
		  FROM content_blobs
		 WHERE tenant_id = $1 AND id = $2 AND shredded_at IS NULL
	`, tenantID, id).Scan(&b.ID, &b.SHA256Hash, &b.SizeBytes, &mime,
		&b.StorageRegion, &b.StorageBucket, &b.StorageKey); err != nil {
		return nil, mapPgError(err)
	}
	if mime != nil {
		b.MimeType = *mime
	}
	return &b, nil
}

// GetContentBlobBySHA reads the newest live blob carrying sha256 inside the
// caller's tenant tx. ErrNotFound when none.
func GetContentBlobBySHA(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, sha string) (*ContentBlob, error) {
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT id FROM content_blobs
		 WHERE tenant_id = $1 AND sha256_hash = $2 AND shredded_at IS NULL
		 ORDER BY created_at DESC
		 LIMIT 1
	`, tenantID, sha).Scan(&id); err != nil {
		return nil, mapPgError(err)
	}
	return GetContentBlob(ctx, tx, tenantID, id)
}

// BlobUploadedBy reports whether userID completed an upload session that
// produced blobID (storage writes upload_sessions.content_blob_id at
// completion — on a late-dedup hit it names the pre-existing blob, so a
// user who re-uploads identical bytes holds a claim on it too).
func BlobUploadedBy(ctx context.Context, tx pgx.Tx, tenantID, blobID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
		    SELECT 1 FROM upload_sessions
		     WHERE tenant_id = $1 AND content_blob_id = $2
		       AND created_by = $3 AND status = 'completed'
		)`, tenantID, blobID, userID).Scan(&ok)
	return ok, mapPgError(err)
}

// DocumentsReferencingBlob returns up to limit live (not soft-deleted)
// documents that have at least one version on blobID, in id order.
func DocumentsReferencingBlob(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID, limit int) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT v.document_id
		  FROM document_versions v
		  JOIN documents d ON d.tenant_id = v.tenant_id AND d.id = v.document_id
		 WHERE v.tenant_id = $1 AND v.content_blob_id = $2 AND d.deleted_at IS NULL
		 ORDER BY v.document_id
		 LIMIT $3
	`, tenantID, blobID, limit)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, id)
	}
	return out, mapPgError(rows.Err())
}

// IncrementBlobRef takes one more reference on a live blob that a new
// version row shares without a fresh upload (copy), so purging either
// document's last reference leaves the blob to the other. ErrNotFound when
// the blob is absent or shredded.
func IncrementBlobRef(ctx context.Context, tx pgx.Tx, tenantID, blobID uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE content_blobs SET reference_count = reference_count + 1
		 WHERE tenant_id = $1 AND id = $2 AND shredded_at IS NULL
	`, tenantID, blobID)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// WorkspaceRegion returns the region a new document in workspaceID is
// pinned to (DEC-11): the workspace's region_pin, else the tenant's
// primary_region, else "" (the caller falls back to the deployment region).
func WorkspaceRegion(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) (string, error) {
	var region string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(NULLIF(w.region_pin, ''), NULLIF(o.primary_region, ''), '')
		  FROM organizations o
		  LEFT JOIN workspaces w ON w.tenant_id = o.id AND w.id = $2
		 WHERE o.id = $1
	`, tenantID, workspaceID).Scan(&region)
	if err != nil {
		return "", mapPgError(err)
	}
	return region, nil
}

// UpdateLifecycleState only touches the state column, unconditionally. The
// service's lifecycle transitions use TransitionLifecycleState (a
// compare-and-set on the expected state); this remains for test fixtures
// and callers that set a state outright.
func (r *documentRepo) UpdateLifecycleState(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, s model.LifecycleState) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET lifecycle_state = $3,
		                     under_legal_hold = ($3 = 'legal_hold'),
		                     updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, string(s))
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// TransitionLifecycleState moves the document from → to only if it is still
// in from: a compare-and-set, so a transition decided on a state another
// transaction has since changed cannot overwrite it (M-5 — "apply hold"
// racing "archive" used to end archived with under_legal_hold=false).
// Zero rows → Conflict; the caller's tx rolls back its side effects.
func TransitionLifecycleState(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, from, to model.LifecycleState) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET lifecycle_state = $4,
		                     under_legal_hold = ($4 = 'legal_hold'),
		                     updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
		  AND lifecycle_state = $3
	`, tenantID, id, string(from), string(to))
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.Conflict("document lifecycle state changed concurrently; reload and retry")
	}
	return nil
}

// SetCurrentVersion records the new head version alongside the hash / size /
// mime summary derived from that version.
func (r *documentRepo) SetCurrentVersion(ctx context.Context, tx pgx.Tx, tenantID, id, versionID uuid.UUID, sha, mime string, size int64) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents
		SET current_version_id = $3, sha256_hash = $4, mime_type = $5,
		    total_size_bytes = $6, version_count = version_count + 1,
		    updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, versionID, sha, mime, size)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

func (r *documentRepo) CountByFolder(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) (int64, error) {
	var n int64
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM documents
		WHERE tenant_id = $1 AND folder_id = $2 AND deleted_at IS NULL
	`, tenantID, folderID).Scan(&n)
	return n, mapPgError(err)
}

// List returns a page of documents matching f. Pagination is keyset on
// (sort_column, id) — no OFFSET anywhere.
func (r *documentRepo) List(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, f model.DocumentFilter) (*model.Page[model.Document], error) {
	col, err := sortColumn(f.SortBy)
	if err != nil {
		return nil, vdmserr.Validation("sort_by", err.Error())
	}
	desc := !strings.EqualFold(f.SortOrder, "asc")
	pageSize := clampPageSize(f.PageSize)

	var (
		args  []any
		where []string
	)
	add := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	// All predicates qualified with `d.` — the LEFT JOIN with users
	// below brings ambiguous columns (tenant_id, id, deleted_at) into
	// scope and Postgres would error on unqualified references.
	where = append(where, "d.tenant_id = "+add(tenantID))
	switch {
	case f.DeletedOnly:
		where = append(where, "d.deleted_at IS NOT NULL")
		// Per-user Trash: only what this caller deleted, and only while
		// they haven't cleared it. The admin Trash sets neither, so it
		// still sees user-cleared rows and can restore them.
		if f.DeletedBy != nil {
			where = append(where, "d.deleted_by = "+add(*f.DeletedBy))
		}
		if f.NotUserCleared {
			where = append(where, "d.user_cleared_at IS NULL")
		}
	case !f.IncludeDeleted:
		where = append(where, "d.deleted_at IS NULL")
	}
	if f.WorkspaceID != nil {
		where = append(where, "d.workspace_id = "+add(*f.WorkspaceID))
	}
	if f.FolderID != nil {
		where = append(where, "d.folder_id = "+add(*f.FolderID))
	}
	if f.LifecycleState != nil {
		where = append(where, "d.lifecycle_state = "+add(string(*f.LifecycleState)))
	}
	if f.DocumentClass != "" {
		where = append(where, "d.document_class = "+add(f.DocumentClass))
	}
	if len(f.Tags) > 0 {
		where = append(where, "d.tags && "+add(f.Tags)) // ARRAY overlap
	}
	if f.CreatedAfter != nil {
		where = append(where, "d.created_at >= "+add(*f.CreatedAfter))
	}
	if f.CreatedBefore != nil {
		where = append(where, "d.created_at <= "+add(*f.CreatedBefore))
	}
	if f.Query != "" {
		where = append(where, "d.title ILIKE "+add("%"+f.Query+"%"))
	}

	// Cursor predicate
	if c, ok := decodeCursor(f.PageToken); ok && c.Sort == col {
		cmp := "<"
		if !desc {
			cmp = ">"
		}
		switch col {
		case "created_at", "updated_at":
			where = append(where,
				fmt.Sprintf("(d.%s, d.id) %s (%s, %s)", col, cmp, add(c.Time), add(c.ID)))
		case "title":
			where = append(where,
				fmt.Sprintf("(d.title, d.id) %s (%s, %s)", cmp, add(c.Text), add(c.ID)))
		case "total_size_bytes":
			where = append(where,
				fmt.Sprintf("(d.total_size_bytes, d.id) %s (%s, %s)", cmp, add(c.Size), add(c.ID)))
		}
	}

	order := "DESC"
	if !desc {
		order = "ASC"
	}

	// COALESCE every nullable string column to '' so the plain-string
	// scan destinations in scanDocument don't panic with
	// "cannot scan NULL into *string". Per CLAUDE.md scanner discipline
	// — every column is either pointer-scanned or COALESCE'd. Nullables:
	// description (free text), region_pin (legacy rows pre-Wave 16),
	// document_class (set by intelligence service after OCR),
	// sha256_hash + mime_type (set after the first upload completes,
	// NULL on docs that have no version yet).
	// LEFT JOIN users for created_by_name — see Get() for rationale.
	// Column references are qualified with `d.` because the join
	// brings ambiguous column names (tenant_id, id, deleted_at) into
	// scope. The ORDER BY columns are listed without qualification
	// in the format string but the only candidates (created_at,
	// updated_at, title, total_size_bytes, id) all live on documents
	// — no ambiguity for Postgres.
	// Same LATERAL join as GetByID — see comment there. The list
	// payload now carries the workflow summary inline, so the
	// frontend DocumentCard can render a status pill without an
	// N+1 useQuery per card.
	q := fmt.Sprintf(`
		SELECT d.id, d.tenant_id, d.workspace_id, d.folder_id, d.title,
		       COALESCE(d.description, '') AS description,
		       d.lifecycle_state,
		       COALESCE(d.region_pin, '') AS region_pin,
		       d.custom_metadata, d.tags,
		       d.current_version_id,
		       COALESCE(d.document_class, '') AS document_class,
		       d.classification_confidence,
		       COALESCE(d.security_classification, '') AS security_classification,
		       d.has_phi, d.has_pii,
		       COALESCE(d.classification_source, '') AS classification_source,
		       COALESCE(d.sha256_hash, '') AS sha256_hash,
		       d.total_size_bytes, d.version_count,
		       COALESCE(d.mime_type, '') AS mime_type,
		       d.created_by, COALESCE(u.display_name, '') AS created_by_name,
		       d.created_at, d.updated_by, d.updated_at, d.deleted_at,
		       COALESCE(d.external_id, '') AS external_id,
		       wf.id, wf.definition_id, wf.definition_name, wf.status, wf.current_step_id, wf.started_at,
		       d.doc_type
		FROM documents d
		LEFT JOIN users u ON u.tenant_id = d.tenant_id AND u.id = d.created_by
		LEFT JOIN LATERAL (
		    SELECT i.id, i.definition_id, def.name AS definition_name,
		           i.status, i.current_step_id, i.started_at
		      FROM workflow_instances i
		      LEFT JOIN workflow_definitions def
		        ON def.tenant_id = i.tenant_id AND def.id = i.definition_id
		     WHERE i.tenant_id = d.tenant_id
		       AND i.document_id = d.id
		       AND i.status NOT IN ('completed','failed','cancelled')
		  ORDER BY i.started_at DESC
		     LIMIT 1
		) wf ON true
		WHERE %s
		ORDER BY d.%s %s, d.id %s
		LIMIT %d`,
		strings.Join(where, " AND "), col, order, order, pageSize+1)

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	items := make([]model.Document, 0, pageSize)
	for rows.Next() {
		d, err := scanDocument(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, *d)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}

	page := &model.Page[model.Document]{Items: items, TotalCount: -1}
	if len(items) > pageSize {
		last := items[pageSize-1]
		page.Items = items[:pageSize]
		next := cursor{Sort: col, ID: last.ID}
		switch col {
		case "created_at":
			next.Time = last.CreatedAt
		case "updated_at":
			next.Time = last.UpdatedAt
		case "title":
			next.Text = last.Title
		case "total_size_bytes":
			next.Size = last.TotalSizeBytes
		}
		page.NextPageToken = encodeCursor(next)
	}
	return page, nil
}

// ---- helpers ---------------------------------------------------------------

// rowScanner is satisfied by both pgx.Row and pgx.Rows.
type rowScanner interface {
	Scan(dest ...any) error
}

func scanDocument(r rowScanner) (*model.Document, error) {
	var (
		d            model.Document
		curVersion   *uuid.UUID
		metaBytes    []byte
		deleted      *time.Time
		lifecycleRaw string
		// Workflow LEFT JOIN trailers — every column nullable so a
		// document without an active workflow scans cleanly.
		wfID            *uuid.UUID
		wfDefinitionID  *uuid.UUID
		wfDefName       *string
		wfStatus        *string
		wfCurrentStepID *string
		wfStartedAt     *time.Time
	)
	if err := r.Scan(
		&d.ID, &d.TenantID, &d.WorkspaceID, &d.FolderID, &d.Title, &d.Description,
		&lifecycleRaw, &d.RegionPin, &metaBytes, &d.Tags,
		&curVersion, &d.DocumentClass, &d.ClassificationConfidence,
		&d.SecurityClassification, &d.HasPHI, &d.HasPII, &d.ClassificationSource,
		&d.SHA256Hash, &d.TotalSizeBytes, &d.VersionCount, &d.MimeType,
		&d.CreatedBy, &d.CreatedByName,
		&d.CreatedAt, &d.UpdatedBy, &d.UpdatedAt, &deleted, &d.ExternalID,
		&wfID, &wfDefinitionID, &wfDefName, &wfStatus, &wfCurrentStepID, &wfStartedAt,
		&d.DocType,
	); err != nil {
		return nil, mapPgError(err)
	}
	d.LifecycleState = model.LifecycleState(lifecycleRaw)
	d.CurrentVersionID = curVersion
	d.DeletedAt = deleted
	if len(metaBytes) > 0 {
		_ = json.Unmarshal(metaBytes, &d.CustomMetadata)
	}
	if d.CustomMetadata == nil {
		d.CustomMetadata = map[string]any{}
	}
	if wfID != nil && wfStatus != nil {
		wf := &model.WorkflowInstanceSummary{
			ID:     *wfID,
			Status: *wfStatus,
		}
		if wfDefinitionID != nil {
			wf.DefinitionID = *wfDefinitionID
		}
		if wfDefName != nil {
			wf.DefinitionName = *wfDefName
		}
		if wfStartedAt != nil {
			wf.StartedAt = *wfStartedAt
		}
		if wfCurrentStepID != nil {
			if n, perr := strconv.Atoi(*wfCurrentStepID); perr == nil {
				wf.CurrentStep = n
			}
		}
		d.WorkflowInstance = wf
	}
	return &d, nil
}

func nullableUUID(u *uuid.UUID) any {
	if u == nil {
		return nil
	}
	return *u
}

// nullableText maps "" → SQL NULL so an empty external_id stays out of the
// partial unique index (which is WHERE external_id IS NOT NULL). Non-empty
// values pass through unchanged.
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}
