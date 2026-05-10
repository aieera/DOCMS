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

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
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
		evt, err := model.NewOutboxEvent(tenantID, "dms.workspace.created.v1", "workspace", w.ID,
			map[string]any{
				"workspace_id": w.ID.String(),
				"name":         w.Name,
				"region_pin":   w.RegionPin,
				"created_by":   userID.String(),
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

// ListWorkspaces returns all active workspaces in the tenant.
func (s *DocumentService) ListWorkspaces(ctx context.Context) ([]model.Workspace, error) {
	tenantID, _, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.Workspace
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		ws, err := s.repos.Workspaces.List(ctx, tx, tenantID)
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

// DeleteWorkspace soft-deletes a workspace. Refuses when documents or
// folders still belong to it; the caller must drain contents first.
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
		if w.DocumentCount > 0 || w.FolderCount > 0 {
			return vdmserr.Conflict("workspace is not empty; move or delete its contents first")
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

// --- validation ------------------------------------------------------------

func validateWorkspaceName(name string) error {
	if name == "" {
		return errInvalidInput("name", "required")
	}
	if len(name) > maxWorkspaceNameLen {
		return errInvalidInput("name", "too long")
	}
	return nil
}
