package service

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

// CreateFolder creates a workspace-scoped folder under an optional parent.
// Emits dms.folder.created.v1.
func (s *DocumentService) CreateFolder(ctx context.Context, in *CreateFolderInput) (*model.Folder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if err := validateFolderName(in.Name); err != nil {
		return nil, err
	}
	if err := s.requirePermission(ctx, userID, "edit", "workspace", in.WorkspaceID, map[string]any{
		"workspace_id": in.WorkspaceID.String(),
	}); err != nil {
		return nil, err
	}

	id, err := newExternalID()
	if err != nil {
		return nil, err
	}
	visibility := in.Visibility
	if visibility == "" {
		visibility = model.FolderShared
	}
	folder := &model.Folder{
		TenantID:       tenantID,
		ID:             id,
		WorkspaceID:    in.WorkspaceID,
		ParentFolderID: in.ParentFolderID,
		Name:           in.Name,
		Visibility:     visibility,
		CreatedBy:      userID,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
	}
	// Private folders are owned by the creator at creation; the owner
	// always retains access regardless of the grants table state.
	if visibility == model.FolderPrivate {
		u := userID
		folder.OwnerID = &u
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		// Compute ltree path + depth.
		if in.ParentFolderID == nil {
			folder.Path = ltreeLabel(in.Name, id)
			folder.Depth = 0
		} else {
			parent, err := s.repos.Folders.GetByID(ctx, tx, tenantID, *in.ParentFolderID)
			if err != nil {
				return err
			}
			if parent.WorkspaceID != in.WorkspaceID {
				return vdmserr.Validation("parent_folder_id", "parent is in a different workspace")
			}
			folder.Depth = parent.Depth + 1
			if folder.Depth >= maxFolderDepth {
				return vdmserr.Validation("parent_folder_id", "max folder nesting depth exceeded")
			}
			folder.Path = parent.Path + "." + ltreeLabel(in.Name, id)
		}

		if err := s.repos.Folders.Create(ctx, tx, folder); err != nil {
			return err
		}

		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.created.v1", "folder", folder.ID,
			model.FolderCreatedPayload{
				FolderID:    folder.ID.String(),
				WorkspaceID: folder.WorkspaceID.String(),
				Path:        folder.Path,
				Name:        folder.Name,
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
	return folder, nil
}

func (s *DocumentService) GetFolder(ctx context.Context, id uuid.UUID) (*model.Folder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var f *model.Folder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var err error
		f, err = s.repos.Folders.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		// Breadcrumb: ancestors from root → parent (excluding self).
		if anc, err := s.repos.Folders.Ancestors(ctx, tx, tenantID, f.Path); err == nil {
			f.Ancestors = anc
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	// Two authorization paths:
	//   A) Direct folder grant (admin, owner, or folder_grants row,
	//      including via a group). Skips workspace OPA.
	//   B) Standard workspace permission via OPA + visibility check
	//      — the path that worked before grants existed.
	// We try (A) first so grantee-only users (no workspace membership)
	// don't fail OPA's workspace gate. Falling back to (B) preserves
	// the existing shared-folder-as-member behavior.
	hasDirect, _ := s.callerHasDirectGrantOnFolder(ctx, tenantID, id, userID)
	if !hasDirect {
		if err := s.requirePermission(ctx, userID, "view", "folder", id, map[string]any{
			"workspace_id": f.WorkspaceID.String(),
		}); err != nil {
			return nil, err
		}
		// Visibility check — private folders still need an explicit
		// grant even for workspace members.
		if err := s.checkFolderAccess(ctx, tenantID, id, userID); err != nil {
			return nil, err
		}
	}
	return f, nil
}

func (s *DocumentService) ListFolders(ctx context.Context, workspaceID uuid.UUID, parentID *uuid.UUID) ([]model.Folder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	// Effective workspace access: full workspace permission (member /
	// admin) OR at least one folder grant in the workspace (direct
	// user grant or via a group). Grantee-only callers get a
	// read-only scoped view — the post-filter below drops shared
	// folders so the grantee sees ONLY their entitled folders.
	isMember := true
	if err := s.requirePermission(ctx, userID, "view", "workspace", workspaceID, nil); err != nil {
		if !errors.Is(err, vdmserr.ErrForbidden) {
			return nil, err
		}
		ok, gerr := s.callerHasGrantInWorkspace(ctx, tenantID, workspaceID, userID)
		if gerr != nil {
			return nil, gerr
		}
		if !ok {
			return nil, err
		}
		isMember = false
	}
	var out []model.Folder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		raw, lerr := s.repos.Folders.ListByParent(ctx, tx, tenantID, workspaceID, parentID)
		if lerr != nil {
			return lerr
		}
		// Filter private folders the caller can't access. Shared
		// folders pass through for full members; grantee-only callers
		// see no shared folders at all (their entry was the grant,
		// not workspace membership).
		isAdmin := s.callerIsTenantAdmin(ctx)
		filtered := raw[:0]
		for i := range raw {
			if raw[i].Visibility == model.FolderShared {
				if isAdmin || isMember {
					filtered = append(filtered, raw[i])
				}
				continue
			}
			ok, cerr := s.repos.Folders.CanAccessFolder(ctx, tx, tenantID, raw[i].ID, userID, nil, isAdmin)
			if cerr != nil {
				return cerr
			}
			if ok {
				filtered = append(filtered, raw[i])
			}
		}
		out = filtered
		return nil
	})
	return out, err
}

// ListSharedWithMe returns folders the caller has been granted access
// to (directly or via a group), cross-workspace, scoped to the
// tenant. Owner exclusion is enforced at the repo layer so this is
// purely a discovery surface, never a re-list of "private folders I
// can see anyway".
func (s *DocumentService) ListSharedWithMe(ctx context.Context) ([]model.SharedFolder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	groups := auth.GetUserGroups(ctx)
	groupIDs := make([]uuid.UUID, 0, len(groups))
	groupIDs = append(groupIDs, groups...)
	var out []model.SharedFolder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var lerr error
		out, lerr = s.repos.Folders.ListSharedWithUser(ctx, tx, tenantID, userID, groupIDs)
		return lerr
	})
	return out, err
}

// callerHasDirectGrantOnFolder returns true ONLY when the caller is
// an admin, the folder's owner, or holds an explicit folder_grants
// row (direct user OR via a group). Crucially, it does NOT return
// true for shared folders by default — that's the workspace
// membership case, handled separately via requirePermission. Used by
// GetFolder + ListDocuments to give grantees a path through that
// bypasses the OPA workspace gate.
func (s *DocumentService) callerHasDirectGrantOnFolder(ctx context.Context, tenantID, folderID, userID uuid.UUID) (bool, error) {
	if s.callerIsTenantAdmin(ctx) {
		return true, nil
	}
	groups := auth.GetUserGroups(ctx)
	groupIDs := make([]uuid.UUID, 0, len(groups))
	groupIDs = append(groupIDs, groups...)
	if groupIDs == nil {
		groupIDs = []uuid.UUID{}
	}
	var found bool
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM folders
				 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
				   AND owner_id = $3
			)
			OR EXISTS (
				SELECT 1 FROM folder_grants
				 WHERE tenant_id = $1 AND folder_id = $2
				   AND ((grantee_type = 'user'  AND grantee_id = $3)
				     OR (grantee_type = 'group' AND grantee_id = ANY($4::uuid[])))
			)`, tenantID, folderID, userID, groupIDs).Scan(&found)
	})
	return found, err
}

// callerHasGrantInWorkspace returns true when the caller holds at
// least one folder_grants row (direct or via group) for a folder in
// the given workspace. Used as the grantee-only entry path for the
// workspace shell + scoped folder listing.
func (s *DocumentService) callerHasGrantInWorkspace(ctx context.Context, tenantID, workspaceID, userID uuid.UUID) (bool, error) {
	groups := auth.GetUserGroups(ctx)
	groupIDs := make([]uuid.UUID, 0, len(groups))
	groupIDs = append(groupIDs, groups...)
	if groupIDs == nil {
		groupIDs = []uuid.UUID{}
	}
	var found bool
	err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM folder_grants fg
				  JOIN folders f
				    ON f.tenant_id = fg.tenant_id
				   AND f.id        = fg.folder_id
				 WHERE fg.tenant_id = $1
				   AND f.workspace_id = $2
				   AND f.deleted_at IS NULL
				   AND ((fg.grantee_type = 'user'  AND fg.grantee_id = $3)
				     OR (fg.grantee_type = 'group' AND fg.grantee_id = ANY($4::uuid[])))
			)`, tenantID, workspaceID, userID, groupIDs).Scan(&found)
	})
	return found, err
}

// callerIsTenantAdmin returns true when the request's auth role is
// owner or admin. Used to fast-path the visibility check (admins
// see every folder regardless of visibility).
func (s *DocumentService) callerIsTenantAdmin(ctx context.Context) bool {
	role := auth.GetUserRole(ctx)
	return role == "owner" || role == "admin"
}

// checkFolderAccess wraps CanAccessFolder so the caller doesn't
// have to open a tx. Returns ErrNotFound (not ErrForbidden) on
// denial so a private folder's existence isn't leaked.
func (s *DocumentService) checkFolderAccess(ctx context.Context, tenantID, folderID, userID uuid.UUID) error {
	isAdmin := s.callerIsTenantAdmin(ctx)
	if isAdmin {
		return nil
	}
	var ok bool
	if err := s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		var cerr error
		ok, cerr = s.repos.Folders.CanAccessFolder(ctx, tx, tenantID, folderID, userID, nil, false)
		return cerr
	}); err != nil {
		return err
	}
	if !ok {
		return vdmserr.ErrNotFound
	}
	return nil
}

// UpdateFolder handles rename and/or re-parent. Rename keeps the same id
// label in the ltree path; re-parent rewrites path for the folder and every
// descendant.
func (s *DocumentService) UpdateFolder(ctx context.Context, in *UpdateFolderInput) (*model.Folder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.Name != nil {
		if err := validateFolderName(*in.Name); err != nil {
			return nil, err
		}
	}

	var updated *model.Folder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "edit", "folder", cur.ID, map[string]any{
			"workspace_id": cur.WorkspaceID.String(),
		}); err != nil {
			return err
		}

		if in.Name != nil && *in.Name != cur.Name {
			if err := s.repos.Folders.UpdateName(ctx, tx, tenantID, cur.ID, *in.Name); err != nil {
				return err
			}
			cur.Name = *in.Name
		}

		if in.NewParentFolderID != nil && (cur.ParentFolderID == nil || *cur.ParentFolderID != *in.NewParentFolderID) {
			parent, err := s.repos.Folders.GetByID(ctx, tx, tenantID, *in.NewParentFolderID)
			if err != nil {
				return err
			}
			if parent.WorkspaceID != cur.WorkspaceID {
				return vdmserr.Validation("new_parent_folder_id", "target is in a different workspace")
			}
			if strings.HasPrefix(parent.Path, cur.Path+".") || parent.Path == cur.Path {
				return vdmserr.Conflict("cannot move a folder into itself or its own descendants")
			}
			if parent.Depth+1 >= maxFolderDepth {
				return vdmserr.Validation("new_parent_folder_id", "max folder nesting depth exceeded")
			}
			if err := s.repos.Folders.Move(ctx, tx, tenantID, cur.ID, cur.Path, parent.Path, parent.Depth+1); err != nil {
				return err
			}
			evt, err := model.NewOutboxEvent(tenantID, "dms.folder.moved.v1", "folder", cur.ID,
				model.FolderMovedPayload{
					FolderID: cur.ID.String(),
					OldPath:  cur.Path,
					NewPath:  parent.Path + "." + lastLabel(cur.Path),
					MovedBy:  userID.String(),
				})
			if err != nil {
				return err
			}
			if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
				return err
			}
		}

		updated, err = s.repos.Folders.GetByID(ctx, tx, tenantID, cur.ID)
		return err
	})
	return updated, err
}

// DeleteFolder soft-deletes a folder AND every descendant folder +
// document under it, atomically, as a single restorable cohort.
// Requires "admin" on the folder.
//
// FIX-5 (audit Section 11). The previous implementation refused
// non-empty folders outright, which left users with no way to
// soft-delete a populated folder. SharePoint/Drive/Box/M-Files all
// cascade. RestoreFolder undoes this by cohort id.
func (s *DocumentService) DeleteFolder(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "admin", "folder", cur.ID, map[string]any{
			"workspace_id": cur.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		cohort, err := s.repos.Folders.SoftDeleteSubtree(ctx, tx, tenantID, id, userID)
		if err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.deleted.v1", "folder", id, map[string]any{
			"folder_id":   id.String(),
			"workspace_id": cur.WorkspaceID.String(),
			"cohort_id":   cohort.String(),
			"deleted_by":  userID.String(),
		})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// RestoreFolder undoes a cascade soft-delete by cohort id: every
// folder + document deleted in the SAME DeleteFolder call comes back
// together. Folders soft-deleted before FIX-5 landed have no cohort
// and return ErrValidation — they're unrecoverable from the UI
// (matches today's reality where there was no restore at all).
//
// Auth: requires "admin" on the folder being restored, same as
// DeleteFolder.
func (s *DocumentService) RestoreFolder(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if err := s.requirePermission(ctx, userID, "admin", "folder", cur.ID, map[string]any{
			"workspace_id": cur.WorkspaceID.String(),
		}); err != nil {
			return err
		}
		if err := s.repos.Folders.RestoreSubtree(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.restored.v1", "folder", id, map[string]any{
			"folder_id":    id.String(),
			"workspace_id": cur.WorkspaceID.String(),
			"restored_by":  userID.String(),
		})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// SetFolderVisibility flips a folder between shared and private.
// Promoting shared → private sets owner = caller (the caller becomes
// the access owner). Demoting private → shared clears owner_id so
// the column doesn't leak a stale value.
//
// Authorization:
//   - owner of the folder can flip it
//   - tenant admin/owner can flip any folder
//   - everyone else gets ErrForbidden (NOT ErrNotFound — the caller
//     reached this endpoint via the folder id, so existence is
//     already known; we just refuse the action).
func (s *DocumentService) SetFolderVisibility(ctx context.Context, in *SetFolderVisibilityInput) (*model.Folder, error) {
	if in == nil || in.FolderID == uuid.Nil {
		return nil, errInvalidInput("folder_id", "required")
	}
	if in.Visibility != model.FolderShared && in.Visibility != model.FolderPrivate {
		return nil, errInvalidInput("visibility", "must be 'shared' or 'private'")
	}
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out *model.Folder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
		if err != nil {
			return err
		}
		if !s.canManageFolder(ctx, cur, userID) {
			return vdmserr.ErrForbidden
		}
		// No-op shortcut.
		if cur.Visibility == in.Visibility {
			out = cur
			return nil
		}
		var owner *uuid.UUID
		if in.Visibility == model.FolderPrivate {
			u := userID
			owner = &u
		}
		if err := s.repos.Folders.UpdateVisibility(ctx, tx, tenantID, in.FolderID, in.Visibility, owner); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.visibility_changed.v1", "folder", in.FolderID,
			map[string]any{
				"folder_id":      in.FolderID.String(),
				"old_visibility": string(cur.Visibility),
				"new_visibility": string(in.Visibility),
				"changed_by":     userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		// FIX-4: visibility flips (shared↔private) change who can see
		// the folder's documents — emit permission.changed so search
		// rewrites readable_by on every doc in the folder.
		if err := s.publishFolderACLChange(ctx, tx, tenantID, in.FolderID, cur.WorkspaceID); err != nil {
			return err
		}
		out, err = s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
		return err
	})
	return out, err
}

// ListFolderGrants returns every grant on a folder. Visible to the
// owner + admins (anyone else gets ErrForbidden — the grant list is
// effectively access metadata).
func (s *DocumentService) ListFolderGrants(ctx context.Context, folderID uuid.UUID) ([]model.FolderGrant, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.FolderGrant
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, folderID)
		if err != nil {
			return err
		}
		if !s.canManageFolder(ctx, cur, userID) {
			return vdmserr.ErrForbidden
		}
		out, err = s.repos.Folders.ListGrants(ctx, tx, tenantID, folderID)
		return err
	})
	return out, err
}

// AddFolderGrant grants a user or group access to a private folder.
// Granting to a shared folder is allowed but redundant — the grant
// is recorded and would only take effect if the folder is later
// flipped to private. Same auth as SetFolderVisibility.
func (s *DocumentService) AddFolderGrant(ctx context.Context, in *AddFolderGrantInput) (*model.FolderGrant, error) {
	if in == nil || in.FolderID == uuid.Nil {
		return nil, errInvalidInput("folder_id", "required")
	}
	if in.GranteeID == uuid.Nil {
		return nil, errInvalidInput("grantee_id", "required")
	}
	if in.GranteeType != "user" && in.GranteeType != "group" {
		return nil, errInvalidInput("grantee_type", "must be 'user' or 'group'")
	}
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	id, _ := newExternalID()
	now := time.Now().UTC()
	grant := &model.FolderGrant{
		TenantID:    tenantID,
		ID:          id,
		FolderID:    in.FolderID,
		GranteeType: in.GranteeType,
		GranteeID:   in.GranteeID,
		GrantedBy:   &userID,
		CreatedAt:   now,
	}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, in.FolderID)
		if err != nil {
			return err
		}
		if !s.canManageFolder(ctx, cur, userID) {
			return vdmserr.ErrForbidden
		}
		if err := s.repos.Folders.AddGrant(ctx, tx, grant); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.grant_added.v1", "folder", in.FolderID,
			map[string]any{
				"folder_id":    in.FolderID.String(),
				"grantee_type": in.GranteeType,
				"grantee_id":   in.GranteeID.String(),
				"granted_by":   userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		// FIX-4: emit permission.changed so search reindexes readable_by.
		return s.publishFolderACLChange(ctx, tx, tenantID, in.FolderID, cur.WorkspaceID)
	})
	if err != nil {
		return nil, err
	}
	return grant, nil
}

// RemoveFolderGrant revokes access. Identifies the grant by
// (folder, grantee_type, grantee_id) so the caller doesn't need the
// row id. Same auth as SetFolderVisibility.
func (s *DocumentService) RemoveFolderGrant(ctx context.Context, folderID uuid.UUID, granteeType string, granteeID uuid.UUID) error {
	if folderID == uuid.Nil {
		return errInvalidInput("folder_id", "required")
	}
	if granteeID == uuid.Nil {
		return errInvalidInput("grantee_id", "required")
	}
	if granteeType != "user" && granteeType != "group" {
		return errInvalidInput("grantee_type", "must be 'user' or 'group'")
	}
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		cur, err := s.repos.Folders.GetByID(ctx, tx, tenantID, folderID)
		if err != nil {
			return err
		}
		if !s.canManageFolder(ctx, cur, userID) {
			return vdmserr.ErrForbidden
		}
		if err := s.repos.Folders.RemoveGrant(ctx, tx, tenantID, folderID, granteeType, granteeID); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.folder.grant_removed.v1", "folder", folderID,
			map[string]any{
				"folder_id":    folderID.String(),
				"grantee_type": granteeType,
				"grantee_id":   granteeID.String(),
				"removed_by":   userID.String(),
			})
		if err != nil {
			return err
		}
		if err := s.repos.Outbox.Insert(ctx, tx, evt); err != nil {
			return err
		}
		// FIX-4: emit permission.changed so search reindexes readable_by.
		return s.publishFolderACLChange(ctx, tx, tenantID, folderID, cur.WorkspaceID)
	})
}

// computeFolderReaders returns the (combined readable_by, user IDs,
// group IDs) that can see the folder. Used by both
// publishFolderACLChange (folder ACL events) and CreateDocument /
// UpdateDocument payloads (so freshly-indexed docs land with the
// right ACL from the first event).
//
// readable_by is the union of:
//   - workspace_members on this folder's workspace (any role)
//   - direct user grants on the folder (folder_grants
//     grantee_type='user')
//
// Group grants are surfaced via the groups slice (the indexer
// supports both pre-split sets); user-level expansion of group
// membership stays at query time so adding a new member to a group
// doesn't require reindexing every doc the group can see.
//
// FIX-4 (2026-05-31). Audit C3.
func (s *DocumentService) computeFolderReaders(ctx context.Context, tx pgx.Tx, tenantID, folderID, workspaceID uuid.UUID) (readableBy, users, groups []string, err error) {
	userSet := map[string]struct{}{}
	groupSet := map[string]struct{}{}
	memberRows, err := tx.Query(ctx, `
		SELECT DISTINCT user_id::text FROM workspace_members
		 WHERE tenant_id = $1 AND workspace_id = $2
	`, tenantID, workspaceID)
	if err != nil {
		return nil, nil, nil, err
	}
	for memberRows.Next() {
		var u string
		if err := memberRows.Scan(&u); err == nil {
			userSet[u] = struct{}{}
		}
	}
	memberRows.Close()
	userGrantRows, err := tx.Query(ctx, `
		SELECT grantee_id::text FROM folder_grants
		 WHERE tenant_id = $1 AND folder_id = $2 AND grantee_type = 'user'
	`, tenantID, folderID)
	if err != nil {
		return nil, nil, nil, err
	}
	for userGrantRows.Next() {
		var u string
		if err := userGrantRows.Scan(&u); err == nil {
			userSet[u] = struct{}{}
		}
	}
	userGrantRows.Close()
	groupGrantRows, err := tx.Query(ctx, `
		SELECT grantee_id::text FROM folder_grants
		 WHERE tenant_id = $1 AND folder_id = $2 AND grantee_type = 'group'
	`, tenantID, folderID)
	if err != nil {
		return nil, nil, nil, err
	}
	for groupGrantRows.Next() {
		var g string
		if err := groupGrantRows.Scan(&g); err == nil {
			groupSet[g] = struct{}{}
		}
	}
	groupGrantRows.Close()

	users = make([]string, 0, len(userSet))
	for u := range userSet {
		users = append(users, u)
	}
	groups = make([]string, 0, len(groupSet))
	for g := range groupSet {
		groups = append(groups, g)
	}
	// Combined `readable_by` is the legacy mixed field still
	// consumed by the indexer's pre-split fallback path.
	readableBy = append(append([]string{}, users...), groups...)
	return readableBy, users, groups, nil
}

// publishFolderACLChange emits dms.permission.changed.v1 for the
// folder. Search subscribes to that subject and fans out via
// UpdateReadableByFolder so every doc in the folder gets readable_by
// rewritten — the doc service doesn't enumerate documents.
//
// FIX-4 (2026-05-31). The audit's C3 root cause was that no service
// published this subject. This is the publisher.
func (s *DocumentService) publishFolderACLChange(ctx context.Context, tx pgx.Tx, tenantID, folderID, workspaceID uuid.UUID) error {
	readableBy, users, groups, err := s.computeFolderReaders(ctx, tx, tenantID, folderID, workspaceID)
	if err != nil {
		return err
	}
	evt, err := model.NewOutboxEvent(tenantID, "dms.permission.changed.v1", "folder", folderID, map[string]any{
		"tenant_id":          tenantID.String(),
		"resource_type":      "folder",
		"resource_id":        folderID.String(),
		"workspace_id":       workspaceID.String(),
		"readable_by":        readableBy,
		"readable_by_users":  users,
		"readable_by_groups": groups,
	})
	if err != nil {
		return err
	}
	return s.repos.Outbox.Insert(ctx, tx, evt)
}

// canManageFolder returns true iff the caller may flip visibility,
// list grants, add a grant, or remove a grant on the named folder:
// owner of the folder OR tenant admin/owner.
func (s *DocumentService) canManageFolder(ctx context.Context, f *model.Folder, userID uuid.UUID) bool {
	if s.callerIsTenantAdmin(ctx) {
		return true
	}
	if f.OwnerID != nil && *f.OwnerID == userID {
		return true
	}
	// Shared folders without an owner: the creator is the de-facto
	// manager so they can flip to private + start granting.
	if f.OwnerID == nil && f.CreatedBy == userID {
		return true
	}
	return false
}

// ltreeLabel builds a valid ltree label from a human name + uuid. ltree
// allows only [A-Za-z0-9_]; we slug the name and append the first UUID
// segment to guarantee uniqueness even when names repeat.
func ltreeLabel(name string, id uuid.UUID) string {
	var b strings.Builder
	for _, r := range strings.ToLower(name) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteRune('_')
		}
	}
	slug := b.String()
	if slug == "" {
		slug = "f"
	}
	if len(slug) > 40 {
		slug = slug[:40]
	}
	return slug + "_" + strings.ReplaceAll(id.String()[:8], "-", "")
}

func lastLabel(path string) string {
	i := strings.LastIndexByte(path, '.')
	if i < 0 {
		return path
	}
	return path[i+1:]
}
