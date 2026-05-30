package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
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
			created_by, created_at, updated_by, updated_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10,
			$11, $12, $13, $14,
			$15, $16, $17, $18,
			$19, $20, $21, $22
		)`,
		d.ID, d.TenantID, d.WorkspaceID, d.FolderID, d.Title, d.Description,
		d.MimeType, d.TotalSizeBytes, d.SHA256Hash, nullableUUID(d.CurrentVersionID),
		0, string(d.LifecycleState), d.RegionPin, d.LifecycleState == model.StateLegalHold,
		d.Tags, meta, d.DocumentClass, d.ClassificationConfidence,
		d.CreatedBy, d.CreatedAt, d.UpdatedBy, d.UpdatedAt,
	)
	return mapPgError(err)
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
		       COALESCE(d.sha256_hash, '') AS sha256_hash,
		       d.total_size_bytes,
		       COALESCE(d.mime_type, '') AS mime_type,
		       d.created_by, COALESCE(u.display_name, '') AS created_by_name,
		       d.created_at, d.updated_by, d.updated_at, d.deleted_at,
		       wf.id, wf.definition_id, wf.definition_name, wf.status, wf.current_step_id, wf.started_at
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

// Update writes back fields that the service layer marks as changed. It uses
// a full-row UPDATE because the service layer has already loaded the row and
// only mutated the fields the caller requested; this keeps the repo simple.
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
		    custom_metadata = $7, tags = $8, document_class = $9,
		    classification_confidence = $10, mime_type = $11,
		    sha256_hash = $12, total_size_bytes = $13, region_pin = $14,
		    lifecycle_state = $15, under_legal_hold = $16,
		    current_version_id = $17, updated_by = $18, updated_at = $19
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`,
		d.TenantID, d.ID, d.Title, d.Description, d.FolderID, d.WorkspaceID,
		meta, d.Tags, d.DocumentClass, d.ClassificationConfidence,
		d.MimeType, d.SHA256Hash, d.TotalSizeBytes, d.RegionPin,
		string(d.LifecycleState), d.LifecycleState == model.StateLegalHold,
		nullableUUID(d.CurrentVersionID), d.UpdatedBy, d.UpdatedAt,
	)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// SoftDelete sets deleted_at. Retention and hard-delete live in Phase 6.
func (r *documentRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET deleted_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Restore clears deleted_at. Returns ErrNotFound if the row doesn't
// exist or is not currently soft-deleted.
func (r *documentRepo) Restore(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE documents SET deleted_at = NULL, updated_at = now()
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
// version of the document — bucket + key so the caller can issue
// S3 DeleteObject before dropping the rows. Returns blobs even for
// soft-deleted documents (the whole point of the purge path).
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

// DeleteVersionsAndBlobs removes every version + content_blob row
// for the document. Caller invokes this inside the same tx as
// HardDelete after MinIO objects are gone. Cascading FKs handle
// ocr_results, document_chunks, etc.
func (r *documentRepo) DeleteVersionsAndBlobs(ctx context.Context, tx pgx.Tx, tenantID, docID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `
		DELETE FROM content_blobs
		WHERE tenant_id = $1
		  AND id IN (SELECT content_blob_id FROM document_versions WHERE tenant_id = $1 AND document_id = $2)
	`, tenantID, docID); err != nil {
		return mapPgError(err)
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM document_versions WHERE tenant_id = $1 AND document_id = $2
	`, tenantID, docID); err != nil {
		return mapPgError(err)
	}
	return nil
}

// UpdateLifecycleState only touches the state column; callers that need
// finer updates use Update.
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
		       COALESCE(d.sha256_hash, '') AS sha256_hash,
		       d.total_size_bytes,
		       COALESCE(d.mime_type, '') AS mime_type,
		       d.created_by, COALESCE(u.display_name, '') AS created_by_name,
		       d.created_at, d.updated_by, d.updated_at, d.deleted_at,
		       wf.id, wf.definition_id, wf.definition_name, wf.status, wf.current_step_id, wf.started_at
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
		&d.SHA256Hash, &d.TotalSizeBytes, &d.MimeType,
		&d.CreatedBy, &d.CreatedByName,
		&d.CreatedAt, &d.UpdatedBy, &d.UpdatedAt, &deleted,
		&wfID, &wfDefinitionID, &wfDefName, &wfStatus, &wfCurrentStepID, &wfStartedAt,
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
