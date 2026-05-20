// Smart folder repository — ADR 0100.
//
// A smart folder is a saved_searches row with is_smart_folder = true.
// Visibility is one of:
//   private    — only the owner sees it in the tree
//   workspace  — visible to anyone with access to workspace_id
//   public     — visible to every user in the tenant
//
// We keep these queries in a dedicated file so the original
// saved-search repository methods stay unchanged.
package repository

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/search/internal/model"
)

// ListSmartFolders returns every smart folder visible to (tenantID,
// userID) — own private folders + workspace-visibility folders the
// caller can see + every public folder in the tenant.
//
// `workspaceIDs` is the caller's accessible workspace set; pass an
// empty slice to hide workspace-scoped folders entirely.
func (r *Repository) ListSmartFolders(
	ctx context.Context,
	tenantID, userID string,
	workspaceIDs []string,
) ([]*model.SavedSearch, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters,
		       notify, notify_interval_minutes, created_at, last_run_at,
		       is_smart_folder, tree_visibility, workspace_id, icon, smart_folder_at
		FROM saved_searches
		WHERE tenant_id = $1
		  AND is_smart_folder = true
		  AND (
		      (tree_visibility = 'private'   AND user_id = $2)
		   OR (tree_visibility = 'workspace' AND workspace_id::text = ANY($3))
		   OR (tree_visibility = 'public')
		  )
		ORDER BY tree_visibility, name`,
		tenantID, userID, workspaceIDs,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*model.SavedSearch
	for rows.Next() {
		ss, err := scanSavedSearchWithSmartFolder(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

// PromoteSmartFolder flips a saved search into a smart folder.
// `visibility` must be one of private|workspace|public; workspaceID
// is required for workspace visibility and ignored otherwise.
// Only the owner can promote.
func (r *Repository) PromoteSmartFolder(
	ctx context.Context,
	tenantID, userID, id string,
	visibility, icon string,
	workspaceID *string,
) (*model.SavedSearch, error) {
	if icon == "" {
		icon = "sparkles"
	}
	// Normalise workspace_id by visibility (the CHECK constraint
	// enforces this too, but doing it here gives a cleaner error
	// message and avoids the round-trip on misuse).
	if visibility != "workspace" {
		workspaceID = nil
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE saved_searches
		SET is_smart_folder = true,
		    tree_visibility = $4,
		    workspace_id    = $5,
		    icon            = $6,
		    smart_folder_at = NOW()
		WHERE id = $1 AND tenant_id = $2 AND user_id = $3`,
		id, tenantID, userID, visibility, workspaceID, icon)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, vdmserr.ErrNotFound
	}
	return r.GetSmartFolder(ctx, tenantID, id)
}

// DemoteSmartFolder flips it back to a regular saved search.
func (r *Repository) DemoteSmartFolder(ctx context.Context, tenantID, userID, id string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE saved_searches
		SET is_smart_folder = false,
		    tree_visibility = 'private',
		    workspace_id    = NULL,
		    smart_folder_at = NULL
		WHERE id = $1 AND tenant_id = $2 AND user_id = $3`,
		id, tenantID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return vdmserr.ErrNotFound
	}
	return nil
}

// GetSmartFolder fetches by id, scoped to tenant (visibility is
// checked at the API layer — callers may legitimately need to read
// a non-owner-but-visible smart folder).
func (r *Repository) GetSmartFolder(ctx context.Context, tenantID, id string) (*model.SavedSearch, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT id, tenant_id, user_id, name, query, filters,
		       notify, notify_interval_minutes, created_at, last_run_at,
		       is_smart_folder, tree_visibility, workspace_id, icon, smart_folder_at
		FROM saved_searches
		WHERE id = $1 AND tenant_id = $2`, id, tenantID)
	return scanSavedSearchWithSmartFolderRow(row)
}

// scanSavedSearchWithSmartFolder reads the saved-search columns +
// the five new smart-folder columns. Separate from scanSavedSearch
// so the existing saved-search code path is unchanged.
func scanSavedSearchWithSmartFolder(rows pgx.Rows) (*model.SavedSearch, error) {
	var (
		ss                    model.SavedSearch
		filtersJSON           []byte
		lastRun               *time.Time
		workspaceID           *string
		smartFolderAt         *time.Time
		notifyIntervalMinutes *int
	)
	if err := rows.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.Name, &ss.Query, &filtersJSON,
		&ss.Notify, &notifyIntervalMinutes, &ss.CreatedAt, &lastRun,
		&ss.IsSmartFolder, &ss.TreeVisibility, &workspaceID, &ss.Icon, &smartFolderAt,
	); err != nil {
		return nil, err
	}
	if filtersJSON != nil {
		_ = json.Unmarshal(filtersJSON, &ss.Filters)
	}
	if notifyIntervalMinutes != nil {
		ss.NotifyIntervalMinutes = *notifyIntervalMinutes
	}
	ss.LastRunAt = lastRun
	ss.WorkspaceID = workspaceID
	ss.SmartFolderAt = smartFolderAt
	return &ss, nil
}

func scanSavedSearchWithSmartFolderRow(row pgx.Row) (*model.SavedSearch, error) {
	var (
		ss                    model.SavedSearch
		filtersJSON           []byte
		lastRun               *time.Time
		workspaceID           *string
		smartFolderAt         *time.Time
		notifyIntervalMinutes *int
	)
	if err := row.Scan(
		&ss.ID, &ss.TenantID, &ss.UserID, &ss.Name, &ss.Query, &filtersJSON,
		&ss.Notify, &notifyIntervalMinutes, &ss.CreatedAt, &lastRun,
		&ss.IsSmartFolder, &ss.TreeVisibility, &workspaceID, &ss.Icon, &smartFolderAt,
	); err != nil {
		return nil, err
	}
	if filtersJSON != nil {
		_ = json.Unmarshal(filtersJSON, &ss.Filters)
	}
	if notifyIntervalMinutes != nil {
		ss.NotifyIntervalMinutes = *notifyIntervalMinutes
	}
	ss.LastRunAt = lastRun
	ss.WorkspaceID = workspaceID
	ss.SmartFolderAt = smartFolderAt
	return &ss, nil
}
