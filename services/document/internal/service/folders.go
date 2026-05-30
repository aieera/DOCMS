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

	id, err := uuid.NewV7()
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
	if err := s.requirePermission(ctx, userID, "view", "folder", id, map[string]any{
		"workspace_id": f.WorkspaceID.String(),
	}); err != nil {
		return nil, err
	}
	// Phase 2 visibility gate. requirePermission above handles
	// workspace + folder OPA rules; CanAccessFolder layers the
	// private-folder rule on top so a private folder's owner +
	// grantees see it and everyone else gets ErrNotFound (NOT
	// ErrForbidden — leaking existence of a private folder defeats
	// the purpose).
	if err := s.checkFolderAccess(ctx, tenantID, id, userID); err != nil {
		return nil, err
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

// DeleteFolder soft-deletes a folder, refusing if it has children (docs or
// sub-folders). Requires "admin" on the folder.
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
		has, err := s.repos.Folders.HasChildren(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if has {
			return vdmserr.Conflict("folder is not empty")
		}
		return s.repos.Folders.SoftDelete(ctx, tx, tenantID, id)
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
	id, _ := uuid.NewV7()
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
		return s.repos.Outbox.Insert(ctx, tx, evt)
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
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
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
