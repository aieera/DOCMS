package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

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
	folder := &model.Folder{
		TenantID:       tenantID,
		ID:             id,
		WorkspaceID:    in.WorkspaceID,
		ParentFolderID: in.ParentFolderID,
		Name:           in.Name,
		CreatedBy:      userID,
		CreatedAt:      time.Now().UTC(),
		UpdatedAt:      time.Now().UTC(),
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
	return f, nil
}

func (s *DocumentService) ListFolders(ctx context.Context, workspaceID uuid.UUID, parentID *uuid.UUID) ([]model.Folder, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if err := s.requirePermission(ctx, userID, "view", "workspace", workspaceID, nil); err != nil {
		return nil, err
	}
	var out []model.Folder
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Folders.ListByParent(ctx, tx, tenantID, workspaceID, parentID)
		return err
	})
	return out, err
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
