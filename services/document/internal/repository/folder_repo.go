package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
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

// FolderPageDefaultLimit / FolderPageMaxLimit bound a single ListByParent page
// (Workstream 6) so a parent with 100k direct children is never materialised in
// one shot.
const (
	FolderPageDefaultLimit = 50
	FolderPageMaxLimit     = 200
)

// ListByParent returns one keyset page of a folder's direct children, ordered by
// (name, id). A non-nil cursor (cursorID != uuid.Nil) resumes AFTER the given
// (name, id). limit is clamped to [1, FolderPageMaxLimit] with a default of
// FolderPageDefaultLimit. Child + document counts for the page are filled with
// TWO aggregate queries (GROUP BY) — O(1) round-trips per listing regardless of
// page size, never an N+1 per row.
func (r *folderRepo) ListByParent(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, parent *uuid.UUID, cursorName string, cursorID uuid.UUID, limit int) ([]model.Folder, error) {
	if limit <= 0 {
		limit = FolderPageDefaultLimit
	}
	if limit > FolderPageMaxLimit {
		limit = FolderPageMaxLimit
	}

	q := `
		SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
		       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
		       f.visibility, f.owner_id,
		       0::bigint, 0::bigint
		FROM folders f
		WHERE f.tenant_id = $1 AND f.workspace_id = $2 AND f.deleted_at IS NULL`
	args := []any{tenantID, workspaceID}
	if parent == nil {
		q += ` AND f.parent_folder_id IS NULL`
	} else {
		q += fmt.Sprintf(` AND f.parent_folder_id = $%d`, len(args)+1)
		args = append(args, *parent)
	}
	if cursorID != uuid.Nil {
		// Row-value comparison gives the stable (name, id) keyset order.
		q += fmt.Sprintf(` AND (f.name, f.id) > ($%d, $%d)`, len(args)+1, len(args)+2)
		args = append(args, cursorName, cursorID)
	}
	q += fmt.Sprintf(` ORDER BY f.name ASC, f.id ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Folder
	for rows.Next() {
		f, serr := scanFolderWithCounts(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	if err := r.fillCounts(ctx, tx, tenantID, workspaceID, out); err != nil {
		return nil, err
	}
	return out, nil
}

// fillCounts populates ChildFolderCount + DocumentCount for a page of folders
// with two GROUP BY aggregates (not a per-row correlated subquery). The child
// aggregate uses idx_folders_parent; the doc aggregate uses
// idx_documents_workspace_folder (all page folders share the workspace).
func (r *folderRepo) fillCounts(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, page []model.Folder) error {
	if len(page) == 0 {
		return nil
	}
	ids := make([]uuid.UUID, len(page))
	for i := range page {
		ids[i] = page[i].ID
	}

	childCount := make(map[uuid.UUID]int64, len(ids))
	crows, err := tx.Query(ctx, `
		SELECT parent_folder_id, count(*)
		  FROM folders
		 WHERE tenant_id = $1 AND parent_folder_id = ANY($2::uuid[]) AND deleted_at IS NULL
		 GROUP BY parent_folder_id`, tenantID, ids)
	if err != nil {
		return mapPgError(err)
	}
	for crows.Next() {
		var pid uuid.UUID
		var n int64
		if err := crows.Scan(&pid, &n); err != nil {
			crows.Close()
			return mapPgError(err)
		}
		childCount[pid] = n
	}
	crows.Close()
	if err := crows.Err(); err != nil {
		return mapPgError(err)
	}

	docCount := make(map[uuid.UUID]int64, len(ids))
	drows, err := tx.Query(ctx, `
		SELECT folder_id, count(*)
		  FROM documents
		 WHERE tenant_id = $1 AND workspace_id = $2 AND folder_id = ANY($3::uuid[]) AND deleted_at IS NULL
		 GROUP BY folder_id`, tenantID, workspaceID, ids)
	if err != nil {
		return mapPgError(err)
	}
	for drows.Next() {
		var fid uuid.UUID
		var n int64
		if err := drows.Scan(&fid, &n); err != nil {
			drows.Close()
			return mapPgError(err)
		}
		docCount[fid] = n
	}
	drows.Close()
	if err := drows.Err(); err != nil {
		return mapPgError(err)
	}

	for i := range page {
		page[i].ChildFolderCount = childCount[page[i].ID]
		page[i].DocumentCount = docCount[page[i].ID]
	}
	return nil
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

// SubtreeDeleteResult is what SoftDeleteSubtree returns. CohortID
// groups the rows for cohort-scoped restore. FolderIDs is every
// folder soft-deleted by this call (including the root). DocumentIDs
// is every document soft-deleted by this call. Callers (the service
// layer) use the two id slices to populate the folder.deleted.v1
// outbox payload so downstream consumers (search) can DeleteByQuery
// without needing a path-recursive view of the tree.
//
// FIX-5 follow-up — required for the search-side delete consumer
// that previously had no way to know which docs were nuked by a
// cascade.
type SubtreeDeleteResult struct {
	CohortID    uuid.UUID
	FolderIDs   []uuid.UUID
	DocumentIDs []uuid.UUID
}

// SoftDeleteSubtree cascades a folder soft-delete to every descendant
// folder + every document under that subtree, stamping a single
// cohort id on all of them so RestoreSubtree can bring them back as
// one atomic group. Returns the cohort id plus the id slices the
// caller needs to fan out downstream events (see SubtreeDeleteResult).
//
// Uses the folder's ltree path (`<@` "ancestor of or equal to") so
// arbitrary-depth subtrees are handled in two UPDATE … RETURNING
// statements. Already-deleted rows are skipped (deleted_at IS NULL
// predicate) so a partial-delete redo is idempotent.
//
// FIX-5 (audit Section 11). The previous DeleteFolder refused non-
// empty folders outright with no restore path — competing products
// ship this as table stakes.
func (r *folderRepo) SoftDeleteSubtree(ctx context.Context, tx pgx.Tx, tenantID, rootID, deletedBy uuid.UUID) (*SubtreeDeleteResult, error) {
	cohortID, err := uuid.NewRandom()
	if err != nil {
		return nil, err
	}
	var rootPath string
	if err := tx.QueryRow(ctx, `
		SELECT path::text FROM folders
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, rootID).Scan(&rootPath); err != nil {
		if err == pgx.ErrNoRows {
			return nil, vdmserr.ErrNotFound
		}
		return nil, mapPgError(err)
	}
	// Documents under any folder in this subtree. Run BEFORE the
	// folder update so the documents.folder_id JOIN still sees live
	// rows; the deleted_at predicate makes the order moot in practice
	// but the explicit ordering keeps the intent readable.
	// RETURNING id lets us hand the affected doc ids back to the
	// caller for downstream fan-out.
	docIDs := []uuid.UUID{}
	docRows, err := tx.Query(ctx, `
		UPDATE documents
		   SET deleted_at = now(),
		       deleted_cohort_id = $3,
		       deleted_by = $4,
		       updated_at = now()
		 WHERE tenant_id = $1
		   AND deleted_at IS NULL
		   AND folder_id IN (
		     SELECT id FROM folders
		      WHERE tenant_id = $1 AND deleted_at IS NULL
		        AND path <@ $2::ltree
		   )
		RETURNING id
	`, tenantID, rootPath, cohortID, deletedBy)
	if err != nil {
		return nil, mapPgError(err)
	}
	for docRows.Next() {
		var id uuid.UUID
		if err := docRows.Scan(&id); err != nil {
			docRows.Close()
			return nil, mapPgError(err)
		}
		docIDs = append(docIDs, id)
	}
	docRows.Close()

	folderIDs := []uuid.UUID{}
	folderRows, err := tx.Query(ctx, `
		UPDATE folders
		   SET deleted_at = now(),
		       deleted_cohort_id = $3,
		       deleted_by = $4,
		       updated_at = now()
		 WHERE tenant_id = $1 AND deleted_at IS NULL
		   AND path <@ $2::ltree
		RETURNING id
	`, tenantID, rootPath, cohortID, deletedBy)
	if err != nil {
		return nil, mapPgError(err)
	}
	for folderRows.Next() {
		var id uuid.UUID
		if err := folderRows.Scan(&id); err != nil {
			folderRows.Close()
			return nil, mapPgError(err)
		}
		folderIDs = append(folderIDs, id)
	}
	folderRows.Close()
	return &SubtreeDeleteResult{
		CohortID:    cohortID,
		FolderIDs:   folderIDs,
		DocumentIDs: docIDs,
	}, nil
}

// RestoreSubtree un-deletes every folder + document that shares the
// cohort id stamped on the given root folder. Cohort-scoping is what
// makes restore safe: any descendant that happened to be deleted
// INDEPENDENTLY (different cohort, different cohort=NULL legacy
// soft-delete) stays deleted.
//
// Returns the ids of the documents it un-deleted (so the caller can
// re-emit their search projections — QA SD-02: cascade delete removed
// them from the index, restore re-indexed nothing) and ErrNotFound if
// the root is not currently deleted or has no cohort id (legacy
// soft-deletes without cohort cannot be restored — they have no scope).
func (r *folderRepo) RestoreSubtree(ctx context.Context, tx pgx.Tx, tenantID, rootID uuid.UUID) ([]uuid.UUID, error) {
	var cohort *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT deleted_cohort_id FROM folders
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NOT NULL
	`, tenantID, rootID).Scan(&cohort); err != nil {
		if err == pgx.ErrNoRows {
			return nil, vdmserr.ErrNotFound
		}
		return nil, mapPgError(err)
	}
	if cohort == nil {
		return nil, vdmserr.Validation("folder", "deleted before cascade support; cannot restore")
	}
	if _, err := tx.Exec(ctx, `
		UPDATE folders
		   SET deleted_at = NULL,
		       deleted_cohort_id = NULL,
		       deleted_by = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND deleted_cohort_id = $2
	`, tenantID, *cohort); err != nil {
		return nil, mapPgError(err)
	}
	rows, err := tx.Query(ctx, `
		UPDATE documents
		   SET deleted_at = NULL,
		       deleted_cohort_id = NULL,
		       deleted_by = NULL,
		       updated_at = now()
		 WHERE tenant_id = $1 AND deleted_cohort_id = $2
		 RETURNING id
	`, tenantID, *cohort)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var docIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		docIDs = append(docIDs, id)
	}
	return docIDs, rows.Err()
}

// FolderPurgeTargets is what a permanent folder delete will remove.
// CohortID is nil for legacy soft-deletes (pre-FIX-5, no cohort) —
// those resolve by ltree subtree instead. LiveDocRefs counts live
// (not soft-deleted) documents still pointing at any target folder;
// purging while it is non-zero would orphan their folder_id FK, so
// the service refuses.
type FolderPurgeTargets struct {
	WorkspaceID uuid.UUID
	CohortID    *uuid.UUID
	FolderIDs   []uuid.UUID
	DocumentIDs []uuid.UUID
	LiveDocRefs int64
}

// PurgeTargets resolves the full row set a permanent delete of the
// given trashed folder covers. The root must be soft-deleted. Cohort
// rows (folders + documents stamped with the same deleted_cohort_id)
// when a cohort exists; otherwise the soft-deleted subtree under the
// root's ltree path (legacy pre-cascade deletes, which would else be
// immortal).
func (r *folderRepo) PurgeTargets(ctx context.Context, tx pgx.Tx, tenantID, rootID uuid.UUID) (*FolderPurgeTargets, error) {
	out := &FolderPurgeTargets{}
	var (
		rootPath  string
		deletedAt *time.Time
	)
	if err := tx.QueryRow(ctx, `
		SELECT workspace_id, path::text, deleted_at, deleted_cohort_id
		  FROM folders
		 WHERE tenant_id = $1 AND id = $2
	`, tenantID, rootID).Scan(&out.WorkspaceID, &rootPath, &deletedAt, &out.CohortID); err != nil {
		return nil, mapPgError(err)
	}
	if deletedAt == nil {
		return nil, vdmserr.Validation("folder", "must be soft-deleted before purge")
	}
	collect := func(q string, args ...any) ([]uuid.UUID, error) {
		rows, err := tx.Query(ctx, q, args...)
		if err != nil {
			return nil, mapPgError(err)
		}
		defer rows.Close()
		ids := []uuid.UUID{}
		for rows.Next() {
			var id uuid.UUID
			if err := rows.Scan(&id); err != nil {
				return nil, mapPgError(err)
			}
			ids = append(ids, id)
		}
		return ids, rows.Err()
	}
	var err error
	if out.CohortID != nil {
		out.FolderIDs, err = collect(`
			SELECT id FROM folders
			 WHERE tenant_id = $1 AND deleted_cohort_id = $2
		`, tenantID, *out.CohortID)
		if err != nil {
			return nil, err
		}
		out.DocumentIDs, err = collect(`
			SELECT id FROM documents
			 WHERE tenant_id = $1 AND deleted_cohort_id = $2
			   AND deleted_at IS NOT NULL
		`, tenantID, *out.CohortID)
		if err != nil {
			return nil, err
		}
	} else {
		out.FolderIDs, err = collect(`
			SELECT id FROM folders
			 WHERE tenant_id = $1 AND deleted_at IS NOT NULL
			   AND path <@ $2::ltree
		`, tenantID, rootPath)
		if err != nil {
			return nil, err
		}
		out.DocumentIDs, err = collect(`
			SELECT id FROM documents
			 WHERE tenant_id = $1 AND deleted_at IS NOT NULL
			   AND folder_id = ANY($2)
		`, tenantID, out.FolderIDs)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM documents
		 WHERE tenant_id = $1 AND deleted_at IS NULL AND folder_id = ANY($2)
	`, tenantID, out.FolderIDs).Scan(&out.LiveDocRefs); err != nil {
		return nil, mapPgError(err)
	}
	return out, nil
}

// HardDeleteFolders drops folder rows outright. Parent+child rows in
// the same call are fine (FK checks fire at end of statement); rows
// referenced from OUTSIDE the set (routing rules, retention scopes,
// live documents) surface as a Conflict via mapPgError — the caller
// translates that into an actionable message.
func (r *folderRepo) HardDeleteFolders(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) error {
	if len(ids) == 0 {
		return nil
	}
	_, err := tx.Exec(ctx, `
		DELETE FROM folders WHERE tenant_id = $1 AND id = ANY($2)
	`, tenantID, ids)
	return mapPgError(err)
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

// FilterAccessibleFolderIDs is the batch form of CanAccessFolder.
// One query for the whole input set instead of N. Returns a map keyed
// by folder_id where true means "accessible to user". Folders absent
// from the map (e.g. deleted, or not in the input) are implicitly
// inaccessible. Skips the isAdmin short-circuit — caller must do that.
//
// SQL union covers all four "yes" cases:
//  1. visibility = 'shared'
//  2. owner_id = userID
//  3. folder_grants row for the user directly
//  4. folder_grants row for any of the user's groups
func (r *folderRepo) FilterAccessibleFolderIDs(
	ctx context.Context, tx pgx.Tx,
	tenantID uuid.UUID, folderIDs []uuid.UUID,
	userID uuid.UUID, userGroups []uuid.UUID,
) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(folderIDs))
	if len(folderIDs) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT f.id
		  FROM folders f
		 WHERE f.tenant_id = $1
		   AND f.id = ANY($2::uuid[])
		   AND f.deleted_at IS NULL
		   AND (
		         f.visibility = 'shared'
		      OR f.owner_id = $3
		      OR EXISTS (
		           SELECT 1 FROM folder_grants fg
		            WHERE fg.tenant_id = f.tenant_id
		              AND fg.folder_id = f.id
		              AND ((fg.grantee_type = 'user'  AND fg.grantee_id = $3)
		                OR (fg.grantee_type = 'group' AND fg.grantee_id = ANY($4::uuid[])))
		         )
		       )
	`, tenantID, uuidSlice(folderIDs), userID, uuidSlice(userGroups))
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ListSharedWithUser returns the cross-workspace "shared with me"
// list — folders the caller has been granted access to (directly or
// via a group) but does NOT own. Owner exclusion is what makes this
// a discovery surface; an owner sees their private folders via the
// normal workspace browse path.
//
// For mixed (direct + group) grants on the same folder we return the
// direct grant row (DISTINCT ON keeps a single row per folder).
func (r *folderRepo) ListSharedWithUser(
	ctx context.Context, tx pgx.Tx,
	tenantID, userID uuid.UUID,
	userGroups []uuid.UUID,
) ([]model.SharedFolder, error) {
	rows, err := tx.Query(ctx, `
		SELECT DISTINCT ON (f.id)
		       f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
		       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
		       f.visibility, f.owner_id,
		       COALESCE((SELECT count(*) FROM documents d
		                 WHERE d.tenant_id = f.tenant_id AND d.folder_id = f.id
		                   AND d.deleted_at IS NULL), 0) AS doc_count,
		       COALESCE((SELECT count(*) FROM folders c
		                 WHERE c.tenant_id = f.tenant_id AND c.parent_folder_id = f.id
		                   AND c.deleted_at IS NULL), 0) AS child_count,
		       w.name,
		       fg.grantee_type,
		       CASE WHEN fg.grantee_type = 'group' THEN fg.grantee_id END,
		       fg.created_at
		  FROM folder_grants fg
		  JOIN folders f       ON f.tenant_id = fg.tenant_id AND f.id = fg.folder_id
		  JOIN workspaces w    ON w.tenant_id = f.tenant_id  AND w.id = f.workspace_id
		 WHERE fg.tenant_id = $1
		   AND f.deleted_at IS NULL
		   AND w.deleted_at IS NULL
		   AND (f.owner_id IS NULL OR f.owner_id <> $2)
		   AND ((fg.grantee_type = 'user'  AND fg.grantee_id = $2)
		     OR (fg.grantee_type = 'group' AND fg.grantee_id = ANY($3::uuid[])))
		 ORDER BY f.id, (fg.grantee_type = 'user') DESC, fg.created_at ASC
	`, tenantID, userID, uuidSlice(userGroups))
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := make([]model.SharedFolder, 0)
	for rows.Next() {
		var (
			f          model.Folder
			parent     *uuid.UUID
			deleted    *time.Time
			visibility string
			owner      *uuid.UUID
			wsName     string
			via        string
			groupID    *uuid.UUID
			grantedAt  time.Time
		)
		if err := rows.Scan(
			&f.ID, &f.TenantID, &f.WorkspaceID, &parent, &f.Path,
			&f.Name, &f.Depth, &f.CreatedBy, &f.CreatedAt, &f.UpdatedAt, &deleted,
			&visibility, &owner,
			&f.DocumentCount, &f.ChildFolderCount,
			&wsName, &via, &groupID, &grantedAt,
		); err != nil {
			return nil, mapPgError(err)
		}
		f.ParentFolderID = parent
		f.DeletedAt = deleted
		f.Visibility = model.FolderVisibility(visibility)
		f.OwnerID = owner
		out = append(out, model.SharedFolder{
			Folder:        f,
			WorkspaceName: wsName,
			GrantedVia:    via,
			GroupID:       groupID,
			GrantedAt:     grantedAt,
		})
	}
	return out, mapPgError(rows.Err())
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

// ListEmptyFolders returns live (non-deleted) leaf folders that have no
// live child folders and no live documents. See the interface doc for
// the workspace / age scoping rules. Counts are forced to 0 in the
// projection — these folders are empty by construction.
func (r *folderRepo) ListEmptyFolders(ctx context.Context, tx pgx.Tx, tenantID, workspaceID uuid.UUID, olderThan time.Time, limit int) ([]model.Folder, error) {
	if limit <= 0 {
		limit = FolderPageDefaultLimit
	}
	if limit > FolderPageMaxLimit {
		limit = FolderPageMaxLimit
	}
	q := `
		SELECT f.id, f.tenant_id, f.workspace_id, f.parent_folder_id, f.path::text,
		       f.name, f.depth, f.created_by, f.created_at, f.updated_at, f.deleted_at,
		       f.visibility, f.owner_id,
		       0::bigint, 0::bigint
		FROM folders f
		WHERE f.tenant_id = $1 AND f.deleted_at IS NULL
		  AND NOT EXISTS (
		      SELECT 1 FROM folders c
		      WHERE c.tenant_id = f.tenant_id AND c.parent_folder_id = f.id AND c.deleted_at IS NULL
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM documents d
		      WHERE d.tenant_id = f.tenant_id AND d.folder_id = f.id AND d.deleted_at IS NULL
		  )`
	args := []any{tenantID}
	if workspaceID != uuid.Nil {
		q += fmt.Sprintf(` AND f.workspace_id = $%d`, len(args)+1)
		args = append(args, workspaceID)
	}
	if !olderThan.IsZero() {
		q += fmt.Sprintf(` AND f.created_at < $%d`, len(args)+1)
		args = append(args, olderThan)
	}
	q += fmt.Sprintf(` ORDER BY f.depth DESC, f.id ASC LIMIT $%d`, len(args)+1)
	args = append(args, limit)

	rows, err := tx.Query(ctx, q, args...)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Folder
	for rows.Next() {
		f, serr := scanFolderWithCounts(rows)
		if serr != nil {
			return nil, serr
		}
		out = append(out, *f)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	return out, nil
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
