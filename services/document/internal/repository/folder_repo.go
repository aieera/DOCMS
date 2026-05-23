package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

type folderRepo struct{}

func (r *folderRepo) Create(ctx context.Context, tx pgx.Tx, f *model.Folder) error {
	var parent any
	if f.ParentFolderID != nil {
		parent = *f.ParentFolderID
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO folders (
			id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
			created_by, created_at, updated_by, updated_at
		) VALUES ($1, $2, $3, $4, $5::ltree, $6, $7, $8, $9, $8, $9)
	`, f.ID, f.TenantID, f.WorkspaceID, parent, f.Path, f.Name, f.Depth,
		f.CreatedBy, f.CreatedAt)
	return mapPgError(err)
}

// GetByID loads a single folder and computes children counts inline.
func (r *folderRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Folder, error) {
	row := tx.QueryRow(ctx, `
		SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
		       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
		       COALESCE((SELECT count(*) FROM documents d
		                 WHERE d.tenant_id = f.tenant_id AND d.folder_id = f.id
		                   AND d.deleted_at IS NULL), 0) AS doc_count,
		       COALESCE((SELECT count(*) FROM folders c
		                 WHERE c.tenant_id = f.tenant_id AND c.parent_folder_id = f.id
		                   AND c.deleted_at IS NULL), 0) AS child_count
		FROM folders f
		WHERE f.tenant_id = $1 AND f.id = $2
	`, tenantID, id)
	return scanFolderWithCounts(row)
}

// Ancestors returns rows whose path is a strict ancestor of ltreePath, in
// shallow-to-deep order. Uses the @> ltree operator with a self-exclusion.
func (r *folderRepo) Ancestors(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ltreePath string) ([]model.Folder, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, workspace_id, parent_folder_id, path::text,
		       name, depth, created_by, created_at, updated_at, deleted_at,
		       0::bigint, 0::bigint
		FROM folders
		WHERE tenant_id = $1
		  AND path @> $2::ltree
		  AND path::text <> $2
		  AND deleted_at IS NULL
		ORDER BY nlevel(path)
	`, tenantID, ltreePath)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Folder
	for rows.Next() {
		f, err := scanFolderWithCounts(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, mapPgError(rows.Err())
}

func (r *folderRepo) ListByParent(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, parent *uuid.UUID) ([]model.Folder, error) {
	var (
		rows pgx.Rows
		err  error
	)
	if parent == nil {
		rows, err = tx.Query(ctx, `
			SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
			       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
			       0::bigint, 0::bigint
			FROM folders f
			WHERE f.tenant_id = $1 AND f.workspace_id = $2
			  AND f.parent_folder_id IS NULL AND f.deleted_at IS NULL
			ORDER BY f.name ASC
		`, tenantID, workspaceID)
	} else {
		rows, err = tx.Query(ctx, `
			SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
			       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
			       0::bigint, 0::bigint
			FROM folders f
			WHERE f.tenant_id = $1 AND f.workspace_id = $2
			  AND f.parent_folder_id = $3 AND f.deleted_at IS NULL
			ORDER BY f.name ASC
		`, tenantID, workspaceID, *parent)
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()

	var out []model.Folder
	for rows.Next() {
		f, err := scanFolderWithCounts(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *f)
	}
	return out, mapPgError(rows.Err())
}

func (r *folderRepo) UpdateName(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name string) error {
	ct, err := tx.Exec(ctx, `
		UPDATE folders SET name = $3, updated_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, name)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// Move relocates a folder (and its entire subtree) by rewriting ltree paths
// for every descendant. oldPath and newParentPath are ltree strings.
func (r *folderRepo) Move(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, oldPath, newParentPath string, newDepth int) error {
	// 1. Update all descendants (including self) by replacing the old prefix
	//    with the new parent path. ltree subpath semantics: subpath(path, 0, nlevel(@old_path) - 1)
	//    gives us the prefix length; we replace exactly that many labels.
	_, err := tx.Exec(ctx, `
		UPDATE folders
		SET path = (text2ltree($3) || subpath(path, nlevel($2::ltree) - 1))::ltree,
		    depth = $4 + (depth - nlevel($2::ltree) + 1),
		    updated_at = now()
		WHERE tenant_id = $1 AND path <@ $2::ltree AND deleted_at IS NULL
	`, tenantID, oldPath, newParentPath, newDepth)
	if err != nil {
		return mapPgError(err)
	}
	// 2. Separately set parent_folder_id on the moved folder itself — parent FK is
	//    redundant with path but kept for relational queries.
	_, err = tx.Exec(ctx, `
		UPDATE folders
		SET parent_folder_id = (SELECT id FROM folders
		                 WHERE tenant_id = $1 AND path::text = $3 AND deleted_at IS NULL)
		WHERE tenant_id = $1 AND id = $2
	`, tenantID, id, newParentPath)
	return mapPgError(err)
}

// SoftDeleteAllInWorkspace bulk-soft-deletes every folder in a workspace.
// Used during DeleteWorkspace when the workspace is "user-visibly empty"
// (no documents and ≤1 folder — typically the auto-created Root) so the
// workspace + its lone folder land in the same tx. No-op if zero rows.
func (r *folderRepo) SoftDeleteAllInWorkspace(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		UPDATE folders SET deleted_at = now(), updated_at = now()
		WHERE tenant_id = $1 AND workspace_id = $2 AND deleted_at IS NULL
	`, tenantID, workspaceID)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *folderRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		UPDATE folders SET deleted_at = now(), updated_at = now()
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

func (r *folderRepo) HasChildren(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (bool, error) {
	var has bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM folders   WHERE tenant_id = $1 AND parent_folder_id = $2 AND deleted_at IS NULL
			UNION ALL
			SELECT 1 FROM documents WHERE tenant_id = $1 AND folder_id = $2 AND deleted_at IS NULL
		)
	`, tenantID, id).Scan(&has)
	return has, mapPgError(err)
}

func scanFolderWithCounts(r rowScanner) (*model.Folder, error) {
	var (
		f       model.Folder
		parent  *uuid.UUID
		deleted *time.Time
	)
	if err := r.Scan(
		&f.ID, &f.TenantID, &f.WorkspaceID, &parent, &f.Path,
		&f.Name, &f.Depth, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, &deleted,
		&f.DocumentCount, &f.ChildFolderCount,
	); err != nil {
		return nil, mapPgError(err)
	}
	f.ParentFolderID = parent
	f.DeletedAt = deleted
	return &f, nil
}
