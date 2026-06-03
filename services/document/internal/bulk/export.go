package bulk

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

// ExportOptions narrows what BulkExport returns. WorkspaceID is
// only honored for document + folder kinds.
type ExportOptions struct {
	Resource    sedocv1.BulkResourceKind
	From        time.Time
	To          time.Time
	WorkspaceID uuid.UUID
	PageSize    int32
}

// Export pages through the requested resource in keyset order
// (created_at ASC, id ASC) and pushes batches to the supplied sink.
// Sink returns a non-nil error to stop early; the iterator
// propagates context cancellation transparently.
//
// Reads run inside a single tenant tx so the cursor stays
// consistent across pages — RLS enforces tenant isolation.
//
// User + Group exports are intentionally NOT implemented here:
// those rows live in auth's DB. A future iteration will dial
// auth's ListUsers / ListGroups RPCs and remap the rows.
func (s *Service) Export(ctx context.Context, tenantID uuid.UUID, opts ExportOptions, sink func(*sedocv1.BulkExportResponse) error) error {
	pageSize := opts.PageSize
	if pageSize <= 0 {
		pageSize = 500
	}
	if pageSize > 5000 {
		pageSize = 5000
	}

	switch opts.Resource {
	case sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_WORKSPACE:
		return s.exportWorkspaces(ctx, tenantID, opts, pageSize, sink)
	case sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_FOLDER:
		return s.exportFolders(ctx, tenantID, opts, pageSize, sink)
	case sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_DOCUMENT:
		return s.exportDocuments(ctx, tenantID, opts, pageSize, sink)
	case sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_USER,
		sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_GROUP:
		return fmt.Errorf("user / group export not yet implemented; use auth ListUsers / ListGroups directly")
	default:
		return fmt.Errorf("unsupported resource kind: %s", opts.Resource)
	}
}

// pageKeyset emits one batch per loop iteration via the supplied
// queryFn, passing in the (created_at, id) of the previous batch's
// last row as the cursor. queryFn returns the rows, the new cursor,
// and whether more pages remain.
//
// External-id is read from bulk_external_id_map at flatten time so
// exported rows include the originating tenant's natural key when
// the row was originally bulk-imported (otherwise external_id is
// empty — clients use internal_id as the fallback key).
func (s *Service) exportWorkspaces(ctx context.Context, tenantID uuid.UUID, opts ExportOptions, pageSize int32, sink func(*sedocv1.BulkExportResponse) error) error {
	cursorTime := opts.From
	cursorID := uuid.Nil
	pageIdx := int32(0)
	for {
		var batch []*sedocv1.BulkItem
		var lastTime time.Time
		var lastID uuid.UUID
		err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT w.id, w.name, COALESCE(w.description,''), w.region_pin, w.created_at,
				       COALESCE(m.external_id,'')
				FROM workspaces w
				LEFT JOIN bulk_external_id_map m
				  ON m.tenant_id = w.tenant_id AND m.resource_type = 'workspace' AND m.internal_id = w.id
				WHERE w.tenant_id = $1
				  AND ($2::timestamptz IS NULL OR w.created_at >= $2)
				  AND ($3::timestamptz IS NULL OR w.created_at <= $3)
				  AND (w.created_at, w.id) > ($4, $5)
				  AND w.deleted_at IS NULL
				ORDER BY w.created_at ASC, w.id ASC
				LIMIT $6
			`, tenantID, nullableTime(opts.From), nullableTime(opts.To),
				cursorTime, cursorID, pageSize)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id uuid.UUID
				var name, desc, region, ext string
				var createdAt time.Time
				if err := rows.Scan(&id, &name, &desc, &region, &createdAt, &ext); err != nil {
					return err
				}
				batch = append(batch, &sedocv1.BulkItem{
					Resource: &sedocv1.BulkItem_Workspace{
						Workspace: &sedocv1.BulkWorkspace{
							ExternalId: ext, Name: name, Description: desc, RegionPin: region,
						},
					},
				})
				lastTime = createdAt
				lastID = id
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		hasMore := int32(len(batch)) == pageSize
		if err := sink(&sedocv1.BulkExportResponse{
			Items: batch, HasMore: hasMore, PageIndex: pageIdx,
		}); err != nil {
			return err
		}
		if !hasMore {
			return nil
		}
		cursorTime, cursorID = lastTime, lastID
		pageIdx++
	}
}

func (s *Service) exportFolders(ctx context.Context, tenantID uuid.UUID, opts ExportOptions, pageSize int32, sink func(*sedocv1.BulkExportResponse) error) error {
	cursorTime := opts.From
	cursorID := uuid.Nil
	pageIdx := int32(0)
	for {
		var batch []*sedocv1.BulkItem
		var lastTime time.Time
		var lastID uuid.UUID
		err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT f.id, f.workspace_id, f.parent_folder_id, f.name, f.created_at,
				       COALESCE(mf.external_id,''), COALESCE(mw.external_id,''), COALESCE(mp.external_id,'')
				FROM folders f
				LEFT JOIN bulk_external_id_map mf
				  ON mf.tenant_id = f.tenant_id AND mf.resource_type = 'folder'    AND mf.internal_id = f.id
				LEFT JOIN bulk_external_id_map mw
				  ON mw.tenant_id = f.tenant_id AND mw.resource_type = 'workspace' AND mw.internal_id = f.workspace_id
				LEFT JOIN bulk_external_id_map mp
				  ON mp.tenant_id = f.tenant_id AND mp.resource_type = 'folder'    AND mp.internal_id = f.parent_folder_id
				WHERE f.tenant_id = $1
				  AND ($2::timestamptz IS NULL OR f.created_at >= $2)
				  AND ($3::timestamptz IS NULL OR f.created_at <= $3)
				  AND ($4::uuid IS NULL OR f.workspace_id = $4)
				  AND (f.created_at, f.id) > ($5, $6)
				  AND f.deleted_at IS NULL
				ORDER BY f.created_at ASC, f.id ASC
				LIMIT $7
			`, tenantID, nullableTime(opts.From), nullableTime(opts.To),
				nullableUUID(opts.WorkspaceID), cursorTime, cursorID, pageSize)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id, wsID uuid.UUID
				var parentID *uuid.UUID
				var name, fxt, wxt, pxt string
				var createdAt time.Time
				if err := rows.Scan(&id, &wsID, &parentID, &name, &createdAt, &fxt, &wxt, &pxt); err != nil {
					return err
				}
				_ = parentID
				batch = append(batch, &sedocv1.BulkItem{
					Resource: &sedocv1.BulkItem_Folder{
						Folder: &sedocv1.BulkFolder{
							ExternalId: fxt, WorkspaceExternalId: wxt, ParentExternalId: pxt, Name: name,
						},
					},
				})
				lastTime = createdAt
				lastID = id
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		hasMore := int32(len(batch)) == pageSize
		if err := sink(&sedocv1.BulkExportResponse{Items: batch, HasMore: hasMore, PageIndex: pageIdx}); err != nil {
			return err
		}
		if !hasMore {
			return nil
		}
		cursorTime, cursorID = lastTime, lastID
		pageIdx++
	}
}

func (s *Service) exportDocuments(ctx context.Context, tenantID uuid.UUID, opts ExportOptions, pageSize int32, sink func(*sedocv1.BulkExportResponse) error) error {
	cursorTime := opts.From
	cursorID := uuid.Nil
	pageIdx := int32(0)
	for {
		var batch []*sedocv1.BulkItem
		var lastTime time.Time
		var lastID uuid.UUID
		err := database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
			rows, err := tx.Query(ctx, `
				SELECT d.id, d.workspace_id, d.folder_id, d.title, COALESCE(d.description,''),
				       COALESCE(d.tags, '{}'), d.region_pin, d.created_at,
				       COALESCE(md.external_id,''), COALESCE(mw.external_id,''), COALESCE(mf.external_id,'')
				FROM documents d
				LEFT JOIN bulk_external_id_map md
				  ON md.tenant_id = d.tenant_id AND md.resource_type = 'document'  AND md.internal_id = d.id
				LEFT JOIN bulk_external_id_map mw
				  ON mw.tenant_id = d.tenant_id AND mw.resource_type = 'workspace' AND mw.internal_id = d.workspace_id
				LEFT JOIN bulk_external_id_map mf
				  ON mf.tenant_id = d.tenant_id AND mf.resource_type = 'folder'    AND mf.internal_id = d.folder_id
				WHERE d.tenant_id = $1
				  AND ($2::timestamptz IS NULL OR d.created_at >= $2)
				  AND ($3::timestamptz IS NULL OR d.created_at <= $3)
				  AND ($4::uuid IS NULL OR d.workspace_id = $4)
				  AND (d.created_at, d.id) > ($5, $6)
				  AND d.deleted_at IS NULL
				ORDER BY d.created_at ASC, d.id ASC
				LIMIT $7
			`, tenantID, nullableTime(opts.From), nullableTime(opts.To),
				nullableUUID(opts.WorkspaceID), cursorTime, cursorID, pageSize)
			if err != nil {
				return err
			}
			defer rows.Close()
			for rows.Next() {
				var id, wsID, folderID uuid.UUID
				var title, desc, region, dxt, wxt, fxt string
				var tags []string
				var createdAt time.Time
				if err := rows.Scan(&id, &wsID, &folderID, &title, &desc, &tags, &region, &createdAt, &dxt, &wxt, &fxt); err != nil {
					return err
				}
				batch = append(batch, &sedocv1.BulkItem{
					Resource: &sedocv1.BulkItem_Document{
						Document: &sedocv1.BulkDocument{
							ExternalId: dxt, WorkspaceExternalId: wxt, FolderExternalId: fxt,
							Title: title, Description: desc, Tags: tags, RegionPin: region,
						},
					},
				})
				lastTime = createdAt
				lastID = id
			}
			return rows.Err()
		})
		if err != nil {
			return err
		}
		hasMore := int32(len(batch)) == pageSize
		if err := sink(&sedocv1.BulkExportResponse{Items: batch, HasMore: hasMore, PageIndex: pageIdx}); err != nil {
			return err
		}
		if !hasMore {
			return nil
		}
		cursorTime, cursorID = lastTime, lastID
		pageIdx++
	}
}

// nullableTime returns nil for the zero time so the SQL "$2::timestamptz IS NULL"
// guard short-circuits the unbounded case.
func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

// nullableUUID returns nil for the zero UUID so the workspace_id
// filter is opt-in.
func nullableUUID(id uuid.UUID) any {
	if id == uuid.Nil {
		return nil
	}
	return id
}
