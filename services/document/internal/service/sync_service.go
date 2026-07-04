package service

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
)

// Sync delta + per-device state (§3/§5). Delta is tenant/workspace-scoped and
// keyset-paginated; device ops are scoped to the caller's user.

type SyncDeltaResult struct {
	Changes    []model.SyncChange
	NextCursor string
	HasMore    bool
}

// SyncDelta returns the changes (upserts + tombstones) since cursor, ordered by
// (updated_at, id). workspace_id is REQUIRED and the caller must hold "view" on
// it — the metadata feed (titles/paths) must not leak workspaces the caller
// can't see. When deviceID is supplied it is enforced (a revoked device is
// rejected) and its server-side cursor + last-seen are advanced.
//
// LIMITATION: within an authorized workspace the feed is not further filtered by
// per-document/private-folder ACL, so a workspace member may see titles/paths of
// private folders they can't open (content download still enforces
// EnsureCanViewDocument, so BYTES are never leaked). Per-row ACL filtering of a
// tombstone-carrying delta is a documented follow-up.
func (s *DocumentService) SyncDelta(ctx context.Context, workspaceID, deviceID *uuid.UUID, cursor string, limit int) (*SyncDeltaResult, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if workspaceID == nil {
		return nil, vdmserr.Validation("workspace_id", "required")
	}
	// Workspace-view gate — mirrors the normal document/folder list paths so a
	// non-member can't enumerate an arbitrary workspace's tree.
	if err := s.requirePermission(ctx, userID, "view", "workspace", *workspaceID, map[string]any{
		"workspace_id": workspaceID.String(),
	}); err != nil {
		return nil, err
	}
	var res SyncDeltaResult
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if deviceID != nil {
			dev, e := s.repos.Sync.GetDevice(ctx, tx, tenantID, *deviceID)
			if e != nil {
				return e
			}
			if dev == nil || dev.UserID != userID {
				return vdmserr.NotFound("device not found")
			}
			if dev.RevokedAt != nil {
				return vdmserr.Forbidden("sync device has been revoked")
			}
		}
		changes, next, more, e := s.repos.Sync.Delta(ctx, tx, tenantID, workspaceID, cursor, limit)
		if e != nil {
			return e
		}
		res.Changes, res.NextCursor, res.HasMore = changes, next, more
		// Advance the device's server-tracked position + last-seen.
		if deviceID != nil {
			if e := s.repos.Sync.UpdateCursor(ctx, tx, tenantID, *deviceID, next); e != nil {
				return e
			}
		}
		return nil
	})
	return &res, err
}

func (s *DocumentService) RegisterSyncDevice(ctx context.Context, name, platform string, workspaceID *uuid.UUID) (*model.SyncDevice, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	if name == "" {
		return nil, vdmserr.Validation("name", "required")
	}
	d := model.SyncDevice{TenantID: tenantID, UserID: userID, Name: name, Platform: platform, WorkspaceID: workspaceID}
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		id, e := s.repos.Sync.RegisterDevice(ctx, tx, d)
		if e != nil {
			return e
		}
		d.ID = id
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func (s *DocumentService) ListSyncDevices(ctx context.Context) ([]model.SyncDevice, error) {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return nil, err
	}
	var out []model.SyncDevice
	err = s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		out, err = s.repos.Sync.ListDevices(ctx, tx, tenantID, userID)
		return err
	})
	return out, err
}

// getOwnedDevice loads a device and enforces caller ownership.
func (s *DocumentService) getOwnedDevice(ctx context.Context, tx pgx.Tx, tenantID, userID, deviceID uuid.UUID) (*model.SyncDevice, error) {
	d, err := s.repos.Sync.GetDevice(ctx, tx, tenantID, deviceID)
	if err != nil {
		return nil, err
	}
	if d == nil || d.UserID != userID {
		return nil, vdmserr.NotFound("device not found")
	}
	return d, nil
}

func (s *DocumentService) SaveSyncCursor(ctx context.Context, deviceID uuid.UUID, cursor string) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		return s.repos.Sync.UpdateCursor(ctx, tx, tenantID, deviceID, cursor)
	})
}

func (s *DocumentService) SetSyncDeviceFolders(ctx context.Context, deviceID uuid.UUID, folderIDs []uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		return s.repos.Sync.SetSelectiveFolders(ctx, tx, tenantID, deviceID, folderIDs)
	})
}

func (s *DocumentService) RevokeSyncDevice(ctx context.Context, deviceID uuid.UUID) error {
	tenantID, userID, err := mustCaller(ctx)
	if err != nil {
		return err
	}
	return s.withTenantTx(ctx, tenantID, func(tx pgx.Tx) error {
		if _, e := s.getOwnedDevice(ctx, tx, tenantID, userID, deviceID); e != nil {
			return e
		}
		ok, e := s.repos.Sync.RevokeDevice(ctx, tx, tenantID, deviceID, userID)
		if e != nil {
			return e
		}
		if !ok {
			return vdmserr.NotFound("device not found or already revoked")
		}
		return nil
	})
}
