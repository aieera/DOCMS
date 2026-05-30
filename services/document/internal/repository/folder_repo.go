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
	var owner any
	if f.OwnerID != nil {
		owner = *f.OwnerID
	}
	visibility := string(f.Visibility)
	if visibility == "" {
		visibility = string(model.FolderShared)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO folders (
			id, tenant_id, workspace_id, parent_folder_id, path, name, depth,
			created_by, created_at, updated_by, updated_at,
			visibility, owner_id
		) VALUES ($1, $2, $3, $4, $5::ltree, $6, $7, $8, $9, $8, $9, $10, $11)
	`, f.ID, f.TenantID, f.WorkspaceID, parent, f.Path, f.Name, f.Depth,
		f.CreatedBy, f.CreatedAt, visibility, owner)
	return mapPgError(err)
}

// GetByID loads a single folder and computes children counts inline.
func (r *folderRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Folder, error) {
	row := tx.QueryRow(ctx, `
		SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
		       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
		       f.visibility, f.owner_id,
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
		       visibility, owner_id,
		       0::bigint, 0::bigint
		FROM folders
		WHERE tenant_id = $1
		  AND path @> $2::ltree
		  -- path != $2::ltree (NOT path::text <> $2): pgx's
		  -- prepared-statement inference pins $2 as ltree from
		  -- the @> clause above. Mixing that with text comparison
		  -- triggers "operator does not exist: text <> ltree" and
		  -- aborts the surrounding tx. Comparing ltree to ltree
		  -- avoids the cross-type problem entirely.
		  AND path != $2::ltree
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
		       f.visibility, f.owner_id,
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
		       f.visibility, f.owner_id,
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

// UpdateVisibility flips visibility (and optionally owner_id when
// promoting shared → private). Owner stays unset for shared folders
// — passing a nil owner with visibility='shared' clears the column.
func (r *folderRepo) UpdateVisibility(
	ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID,
	visibility model.FolderVisibility, owner *uuid.UUID,
) error {
	var ownerArg any
	if owner != nil {
		ownerArg = *owner
	}
	ct, err := tx.Exec(ctx, `
		UPDATE folders
		   SET visibility = $3, owner_id = $4, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, string(visibility), ownerArg)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// ListGrants returns every grant on a folder, oldest-first.
func (r *folderRepo) ListGrants(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID) ([]model.FolderGrant, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, tenant_id, folder_id, grantee_type, grantee_id, granted_by, created_at
		FROM folder_grants
		WHERE tenant_id = $1 AND folder_id = $2
		ORDER BY created_at ASC
	`, tenantID, folderID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]model.FolderGrant, 0)
	for rows.Next() {
		var g model.FolderGrant
		if err := rows.Scan(&g.ID, &g.TenantID, &g.FolderID, &g.GranteeType,
			&g.GranteeID, &g.GrantedBy, &g.CreatedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, g)
	}
	return out, mapPgError(rows.Err())
}

// AddGrant upserts a (folder, grantee_type, grantee_id) row. UNIQUE
// constraint on those columns makes a re-grant a no-op.
func (r *folderRepo) AddGrant(ctx context.Context, tx pgx.Tx, g *model.FolderGrant) error {
	var grantedBy any
	if g.GrantedBy != nil {
		grantedBy = *g.GrantedBy
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO folder_grants (id, tenant_id, folder_id, grantee_type, grantee_id, granted_by, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (tenant_id, folder_id, grantee_type, grantee_id) DO NOTHING
	`, g.ID, g.TenantID, g.FolderID, g.GranteeType, g.GranteeID, grantedBy, g.CreatedAt)
	return mapPgError(err)
}

// RemoveGrant deletes by (folder, grantee_type, grantee_id) so the
// caller doesn't need the row id.
func (r *folderRepo) RemoveGrant(ctx context.Context, tx pgx.Tx, tenantID, folderID uuid.UUID, granteeType string, granteeID uuid.UUID) error {
	ct, err := tx.Exec(ctx, `
		DELETE FROM folder_grants
		 WHERE tenant_id = $1 AND folder_id = $2
		   AND grantee_type = $3 AND grantee_id = $4
	`, tenantID, folderID, granteeType, granteeID)
	if err != nil {
		return mapPgError(err)
	}
	if ct.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// CanAccessFolder is the single source of truth for "can user U see
// folder F?". Returns true when ANY of these is satisfied:
//   - the folder is shared (workspace membership is the gate, enforced
//     elsewhere)
//   - the caller is the folder's owner
//   - a folder_grants row exists for (user, folder)
//   - a folder_grants row exists for any group the user is in
//   - the caller is a tenant admin/owner (caller passes isAdmin=true)
//
// Used by ListFolders / GetFolder / Move / Copy. Caller-passed
// userGroups is the precomputed slice of group UUIDs the user belongs
// to; computing it inside this hot-path query would N+1.
func (r *folderRepo) CanAccessFolder(
	ctx context.Context, tx pgx.Tx,
	tenantID, folderID, userID uuid.UUID,
	userGroups []uuid.UUID,
	isAdmin bool,
) (bool, error) {
	if isAdmin {
		return true, nil
	}
	var visibility string
	var owner *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT visibility, owner_id FROM folders
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, folderID).Scan(&visibility, &owner); err != nil {
		if err == pgx.ErrNoRows {
			return false, vdmserr.ErrNotFound
		}
		return false, mapPgError(err)
	}
	if visibility == string(model.FolderShared) {
		return true, nil
	}
	if owner != nil && *owner == userID {
		return true, nil
	}
	var exists bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM folder_grants
			 WHERE tenant_id = $1 AND folder_id = $2
			   AND ((grantee_type = 'user' AND grantee_id = $3)
			     OR (grantee_type = 'group' AND grantee_id = ANY($4::uuid[])))
		)
	`, tenantID, folderID, userID, uuidSlice(userGroups)).Scan(&exists); err != nil {
		return false, mapPgError(err)
	}
	return exists, nil
}

// uuidSlice is a passthrough that pgx serialises as a uuid[] cleanly.
// Defined as a helper so a nil slice (no groups) doesn't break the
// query — pgx treats nil-slice as empty array which is what we want.
func uuidSlice(in []uuid.UUID) []uuid.UUID {
	if in == nil {
		return []uuid.UUID{}
	}
	return in
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
		f          model.Folder
		parent     *uuid.UUID
		deleted    *time.Time
		visibility string
		owner      *uuid.UUID
	)
	if err := r.Scan(
		&f.ID, &f.TenantID, &f.WorkspaceID, &parent, &f.Path,
		&f.Name, &f.Depth, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, &deleted,
		&visibility, &owner,
		&f.DocumentCount, &f.ChildFolderCount,
	); err != nil {
		return nil, mapPgError(err)
	}
	f.ParentFolderID = parent
	f.DeletedAt = deleted
	f.Visibility = model.FolderVisibility(visibility)
	f.OwnerID = owner
	return &f, nil
}
