// Package service: workspace CRUD. Workspaces are the top-level tenant
// container; every folder + document belongs to one. Emits
// dms.workspace.created / updated / deleted events to the outbox.
package service

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/validation"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

const (
	maxWorkspaceNameLen        = 120
	maxWorkspaceDescriptionLen = 1000
	defaultWorkspaceRegionPin  = "us-east-1"
)

// CreateWorkspaceInput is the shape for CreateWorkspace.
type CreateWorkspaceInput struct {
	Name        string
	Description string
	RegionPin   string
}

// UpdateWorkspaceInput is the shape for UpdateWorkspace.
type UpdateWorkspaceInput struct {
	ID          uuid.UUID
	Name        string
	Description string
}

// CreateWorkspace creates a tenant-scoped workspace. Requires admin
// within the tenant (or owner). No per-workspace policy check is made
// on creation since the caller's role authority is tenant-wide.
func (s *DocumentService) CreateWorkspace(ctx context.Context, in *CreateWorkspaceInput) (*model.Workspace, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if err := validateWorkspaceName(name); err != nil {
		return nil, err
	}
	// Min-length validator. Prod rejects "g"/"tre"-class names;
	// dev/staging logs a warning so seed scripts still load.
	if err := validation.EntityName(s.env, name); err != nil {
		return nil, errInvalidInput("name", "must be at least 2 characters")
	}
	if validation.EntityNameTooShort(name) {
		s.log.Warn().Str("name", name).Str("tenant", tenantID.String()).Msg("workspace name shorter than 2 chars (allowed in non-prod)")
	}
	if len(in.Description) > maxWorkspaceDescriptionLen {
		return nil, errInvalidInput("description", "too long")
	}
	regionPin := in.RegionPin
	if regionPin == "" {
		regionPin = defaultWorkspaceRegionPin
	}

	id, err := uuid.NewV7()
	if err != nil {
		return nil, err
	}
	w := &model.Workspace{
		TenantID:    tenantID,
		ID:          id,
		Name:        name,
		Description: strings.TrimSpace(in.Description),
		RegionPin:   regionPin,
		CreatedBy:   userID,
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}

	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.repos.Workspaces.Create(ctx, tx, w); err != nil {
			return err
		}
		// Auto-enroll the creator as a workspace admin so the workspace
		// has at least one member from the moment of creation. Without
		// this, every fresh workspace shows "0 members" in the UI and
		// the creator can't even view it through the workspace ACL —
		// only the tenant-role rules let them in. Use the same tx so a
		// downstream failure rolls both inserts back.
		if err := s.repos.Workspaces.AddMember(ctx, tx, tenantID, w.ID, userID, userID, "admin"); err != nil {
			return err
		}
		// Auto-create a root folder so uploads work immediately. The
		// CreateDocument validator requires a non-nil folder_id, and
		// the frontend's useUpload picks the workspace's root folder
		// when no explicit folder is selected. Without this, every
		// fresh workspace returns 400 from CreateDocument because
		// `getFolders(workspaceId)` finds no rows. Same tx so the
		// workspace + member + root folder land atomically.
		rootID, err := uuid.NewV7()
		if err != nil {
			return err
		}
		root := &model.Folder{
			TenantID:    tenantID,
			ID:          rootID,
			WorkspaceID: w.ID,
			Name:        "Root",
			Path:        ltreeLabel("Root", rootID),
			Depth:       0,
			CreatedBy:   userID,
			CreatedAt:   time.Now().UTC(),
			UpdatedAt:   time.Now().UTC(),
		}
		if err := s.repos.Folders.Create(ctx, tx, root); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.workspace.created.v1", "workspace", w.ID,
			map[string]any{
				"workspace_id":   w.ID.String(),
				"name":           w.Name,
				"region_pin":     w.RegionPin,
				"created_by":     userID.String(),
				"root_folder_id": rootID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	if err != nil {
		return nil, err
	}
	return w, nil
}

// GetWorkspace loads a single workspace with counts.
func (s *DocumentService) GetWorkspace(ctx context.Context, id uuid.UUID) (*model.Workspace, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if id == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	var out *model.Workspace
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		w, err := s.repos.Workspaces.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		out = w
		return nil
	})
	return out, err
}

// ListWorkspaces returns the workspaces the caller can access:
// tenant owner/admin sees every active workspace; everyone else sees
// only ones they created or are in via workspace_members. The repo
// does the filtering — see workspaceRepo.List for the rationale.
func (s *DocumentService) ListWorkspaces(ctx context.Context) ([]model.Workspace, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	role := auth.GetUserRole(ctx)
	groups := auth.GetUserGroups(ctx)
	var out []model.Workspace
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ws, err := s.repos.Workspaces.List(ctx, tx, tenantID, userID, groups, role)
		if err != nil {
			return err
		}
		out = ws
		return nil
	})
	return out, err
}

// UpdateWorkspace renames or updates the description of a workspace.
// Empty name is interpreted as "no change"; explicit empty description
// is permitted.
func (s *DocumentService) UpdateWorkspace(ctx context.Context, in *UpdateWorkspaceInput) (*model.Workspace, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in.ID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	name := strings.TrimSpace(in.Name)
	if name != "" {
		if err := validateWorkspaceName(name); err != nil {
			return nil, err
		}
	}
	if len(in.Description) > maxWorkspaceDescriptionLen {
		return nil, errInvalidInput("description", "too long")
	}
	var out *model.Workspace
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.requirePermission(ctx, userID, "admin", "workspace", in.ID, map[string]any{
			"workspace_id": in.ID.String(),
		}); err != nil {
			return err
		}
		if err := s.repos.Workspaces.Update(ctx, tx, tenantID, in.ID, name, in.Description); err != nil {
			return err
		}
		w, err := s.repos.Workspaces.GetByID(ctx, tx, tenantID, in.ID)
		if err != nil {
			return err
		}
		out = w
		evt, err := model.NewOutboxEvent(tenantID, "dms.workspace.updated.v1", "workspace", w.ID,
			map[string]any{
				"workspace_id": w.ID.String(),
				"updated_by":   userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

// DeleteWorkspace soft-deletes a workspace. Refuses when there are any
// documents OR more than one folder. CreateWorkspace ships every workspace
// with an auto-created "Root" folder, so the strict "FolderCount==0" rule
// made it impossible to delete a brand-new empty workspace from the UI;
// we loosen to allow up to one folder, which the same tx soft-deletes
// alongside the workspace. (A user-created top-level folder also satisfies
// FolderCount==1, but with zero documents it's still user-visibly empty
// and safe to drop.)
func (s *DocumentService) DeleteWorkspace(ctx context.Context, id uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	if id == uuid.Nil {
		return errInvalidInput("workspace_id", "required")
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if err := s.requirePermission(ctx, userID, "admin", "workspace", id, map[string]any{
			"workspace_id": id.String(),
		}); err != nil {
			return err
		}
		w, err := s.repos.Workspaces.GetByID(ctx, tx, tenantID, id)
		if err != nil {
			return err
		}
		if !workspaceIsUserEmpty(w) {
			return vdmserr.Conflict("workspace is not empty; move or delete its contents first")
		}
		if w.FolderCount == 1 {
			if err := s.repos.Folders.SoftDeleteAllInWorkspace(ctx, tx, tenantID, id); err != nil {
				return err
			}
		}
		if err := s.repos.Workspaces.SoftDelete(ctx, tx, tenantID, id); err != nil {
			return err
		}
		evt, err := model.NewOutboxEvent(tenantID, "dms.workspace.deleted.v1", "workspace", id,
			map[string]any{
				"workspace_id": id.String(),
				"deleted_by":   userID.String(),
			})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
}

// TransferWorkspaceOwnershipInput names the workspace + the user who
// should become the new creator/owner.
type TransferWorkspaceOwnershipInput struct {
	WorkspaceID uuid.UUID
	NewOwnerID  uuid.UUID
}

// TransferWorkspaceOwnership reassigns workspaces.created_by to the named
// user. This is the canonical "owner" field in our single-owner model
// (no per-workspace owner role exists; created_by carries the semantic).
//
// Gate: caller must be the current creator OR hold tenant role=owner.
// The new owner must already be an active workspace member — this avoids
// silently granting access to a non-member as a side effect.
//
// Emits dms.workspace.owner_transferred.v1 with previous/new owner so
// audit + search-readers consumers can react.
func (s *DocumentService) TransferWorkspaceOwnership(ctx context.Context, in *TransferWorkspaceOwnershipInput) (*model.Workspace, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if in == nil || in.WorkspaceID == uuid.Nil {
		return nil, errInvalidInput("workspace_id", "required")
	}
	if in.NewOwnerID == uuid.Nil {
		return nil, errInvalidInput("new_owner_id", "required")
	}
	// Caller's tenant role (owner/admin/member/viewer). Tenant-owner can
	// bypass the "must-be-current-creator" gate so a tenant-wide admin
	// can rescue an orphaned workspace.
	callerInfo, _ := auth.User(ctx)

	var out *model.Workspace
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		w, err := s.repos.Workspaces.GetByID(ctx, tx, tenantID, in.WorkspaceID)
		if err != nil {
			return err
		}
		if callerInfo.Role != "owner" && w.CreatedBy != userID {
			return vdmserr.ErrForbidden
		}
		if w.CreatedBy == in.NewOwnerID {
			return errInvalidInput("new_owner_id", "is already the owner")
		}
		isMember, err := s.repos.Workspaces.IsMember(ctx, tx, tenantID, in.WorkspaceID, in.NewOwnerID)
		if err != nil {
			return err
		}
		if !isMember {
			return errInvalidInput("new_owner_id", "must already be a workspace member")
		}
		prev := w.CreatedBy
		if err := s.repos.Workspaces.UpdateCreatedBy(ctx, tx, tenantID, in.WorkspaceID, in.NewOwnerID); err != nil {
			return err
		}
		refreshed, err := s.repos.Workspaces.GetByID(ctx, tx, tenantID, in.WorkspaceID)
		if err != nil {
			return err
		}
		out = refreshed
		evt, err := model.NewOutboxEvent(tenantID, "dms.workspace.owner_transferred.v1", "workspace", in.WorkspaceID, map[string]any{
			"workspace_id":   in.WorkspaceID.String(),
			"previous_owner": prev.String(),
			"new_owner":      in.NewOwnerID.String(),
			"transferred_by": userID.String(),
		})
		if err != nil {
			return err
		}
		return s.repos.Outbox.Insert(ctx, tx, evt)
	})
	return out, err
}

// --- validation ------------------------------------------------------------

// workspaceIsUserEmpty reports whether a workspace can be soft-deleted
// from the UI: zero documents AND at most one folder. CreateWorkspace
// auto-creates a "Root" folder, so the strict "FolderCount==0" rule
// would block deletion of every brand-new empty workspace. Allowing
// one residual folder lets DeleteWorkspace soft-delete it in the same
// tx — the document-count gate (which we keep strict) ensures the
// workspace is genuinely user-visibly empty.
func workspaceIsUserEmpty(w *model.Workspace) bool {
	return w.DocumentCount == 0 && w.FolderCount <= 1
}

func validateWorkspaceName(name string) error {
	if name == "" {
		return errInvalidInput("name", "required")
	}
	if len(name) > maxWorkspaceNameLen {
		return errInvalidInput("name", "too long")
	}
	return nil
}
