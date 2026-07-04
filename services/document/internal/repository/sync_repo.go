package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/services/document/internal/model"
)

// SyncRepository backs the cursor-based sync delta + per-device sync state
// (§3/§5). The delta is a keyset-paginated (updated_at, id) feed over the
// documents + folders tables (tombstones included via deleted_at) — no separate
// change table. Device rows only track the client's cursor + selective-sync set.
type SyncRepository interface {
	Delta(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, workspaceID *uuid.UUID, token string, limit int) ([]model.SyncChange, string, bool, error)

	RegisterDevice(ctx context.Context, tx pgx.Tx, d model.SyncDevice) (uuid.UUID, error)
	ListDevices(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.SyncDevice, error)
	GetDevice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SyncDevice, error)
	UpdateCursor(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, cursor string) error
	RevokeDevice(ctx context.Context, tx pgx.Tx, tenantID, id, by uuid.UUID) (bool, error)
	SetSelectiveFolders(ctx context.Context, tx pgx.Tx, tenantID, deviceID uuid.UUID, folderIDs []uuid.UUID) error
}

type syncRepo struct{}

func NewSyncRepo() SyncRepository { return &syncRepo{} }

// SyncCursorEncode/Decode wrap the shared keyset cursor for the sync feed so the
// service (a different package) can build a first-page/empty cursor.
func SyncCursorEncode(t time.Time, id uuid.UUID) string {
	return encodeCursor(cursor{Sort: "sync", Time: t, ID: id})
}

func (r *syncRepo) Delta(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, workspaceID *uuid.UUID, token string, limit int) ([]model.SyncChange, string, bool, error) {
	limit = clampPageSize(limit)
	var curTime *time.Time
	var curID uuid.UUID
	if c, ok := decodeCursor(token); ok && c.Sort == "sync" {
		t := c.Time
		curTime = &t
		curID = c.ID
	}
	rows, err := tx.Query(ctx, `
		SELECT kind, id, updated_at, is_deleted, parent, name, path, version_id, sha256, size_bytes, mime
		FROM (
			SELECT 'folder'::text AS kind, id, updated_at, (deleted_at IS NOT NULL) AS is_deleted,
			       parent_folder_id AS parent, name, path::text AS path,
			       NULL::uuid AS version_id, ''::text AS sha256, 0::bigint AS size_bytes, ''::text AS mime
			FROM folders
			WHERE tenant_id = $1 AND ($2::uuid IS NULL OR workspace_id = $2)
			UNION ALL
			SELECT 'document'::text AS kind, id, updated_at, (deleted_at IS NOT NULL) AS is_deleted,
			       folder_id AS parent, title AS name, ''::text AS path,
			       current_version_id, COALESCE(sha256_hash,''), COALESCE(total_size_bytes,0), COALESCE(mime_type,'')
			FROM documents
			WHERE tenant_id = $1 AND ($2::uuid IS NULL OR workspace_id = $2)
		) u
		WHERE ($3::timestamptz IS NULL OR (u.updated_at, u.id) > ($3, $4))
		ORDER BY u.updated_at ASC, u.id ASC
		LIMIT $5`,
		tenantID, workspaceID, curTime, curID, limit+1)
	if err != nil {
		return nil, "", false, mapPgError(err)
	}
	defer rows.Close()

	out := make([]model.SyncChange, 0, limit+1)
	for rows.Next() {
		var (
			ch        model.SyncChange
			isDeleted bool
			parent    *uuid.UUID
			verID     *uuid.UUID
		)
		if err := rows.Scan(&ch.Kind, &ch.ID, &ch.UpdatedAt, &isDeleted, &parent, &ch.Name,
			&ch.Path, &verID, &ch.SHA256, &ch.SizeBytes, &ch.Mime); err != nil {
			return nil, "", false, mapPgError(err)
		}
		ch.ParentID = parent
		ch.VersionID = verID
		if isDeleted {
			ch.Op = model.SyncOpDelete
		} else {
			ch.Op = model.SyncOpUpsert
		}
		out = append(out, ch)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, mapPgError(err)
	}

	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	next := token
	if len(out) > 0 {
		last := out[len(out)-1]
		next = SyncCursorEncode(last.UpdatedAt, last.ID)
	}
	return out, next, hasMore, nil
}

func (r *syncRepo) RegisterDevice(ctx context.Context, tx pgx.Tx, d model.SyncDevice) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO sync_devices (tenant_id, user_id, name, platform, workspace_id, cursor)
		VALUES ($1,$2,$3,$4,$5,$6) RETURNING id`,
		d.TenantID, d.UserID, d.Name, d.Platform, d.WorkspaceID, d.Cursor).Scan(&id)
	if err != nil {
		return uuid.Nil, mapPgError(err)
	}
	return id, nil
}

func (r *syncRepo) ListDevices(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.SyncDevice, error) {
	rows, err := tx.Query(ctx, `
		SELECT id, user_id, name, platform, workspace_id, cursor, created_at, last_seen_at, revoked_at
		FROM sync_devices WHERE tenant_id = $1 AND user_id = $2 ORDER BY created_at DESC`, tenantID, userID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []model.SyncDevice{}
	for rows.Next() {
		d := model.SyncDevice{TenantID: tenantID}
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.WorkspaceID, &d.Cursor,
			&d.CreatedAt, &d.LastSeenAt, &d.RevokedAt); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, mapPgError(err)
	}
	for i := range out {
		fs, err := r.selectiveFolders(ctx, tx, tenantID, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].SelectiveFolders = fs
	}
	return out, nil
}

func (r *syncRepo) selectiveFolders(ctx context.Context, tx pgx.Tx, tenantID, deviceID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := tx.Query(ctx, `SELECT folder_id FROM sync_device_folders WHERE tenant_id=$1 AND device_id=$2 ORDER BY added_at`, tenantID, deviceID)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, mapPgError(err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (r *syncRepo) GetDevice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SyncDevice, error) {
	d := model.SyncDevice{TenantID: tenantID}
	err := tx.QueryRow(ctx, `
		SELECT id, user_id, name, platform, workspace_id, cursor, created_at, last_seen_at, revoked_at
		FROM sync_devices WHERE tenant_id=$1 AND id=$2`, tenantID, id).Scan(
		&d.ID, &d.UserID, &d.Name, &d.Platform, &d.WorkspaceID, &d.Cursor, &d.CreatedAt, &d.LastSeenAt, &d.RevokedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	fs, err := r.selectiveFolders(ctx, tx, tenantID, id)
	if err != nil {
		return nil, err
	}
	d.SelectiveFolders = fs
	return &d, nil
}

func (r *syncRepo) UpdateCursor(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, cursorTok string) error {
	_, err := tx.Exec(ctx, `UPDATE sync_devices SET cursor=$3, last_seen_at=now() WHERE tenant_id=$1 AND id=$2`, tenantID, id, cursorTok)
	if err != nil {
		return mapPgError(err)
	}
	return nil
}

func (r *syncRepo) RevokeDevice(ctx context.Context, tx pgx.Tx, tenantID, id, by uuid.UUID) (bool, error) {
	ct, err := tx.Exec(ctx, `UPDATE sync_devices SET revoked_at=now(), revoked_by=$3 WHERE tenant_id=$1 AND id=$2 AND revoked_at IS NULL`, tenantID, id, by)
	if err != nil {
		return false, mapPgError(err)
	}
	return ct.RowsAffected() > 0, nil
}

func (r *syncRepo) SetSelectiveFolders(ctx context.Context, tx pgx.Tx, tenantID, deviceID uuid.UUID, folderIDs []uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM sync_device_folders WHERE tenant_id=$1 AND device_id=$2`, tenantID, deviceID); err != nil {
		return mapPgError(err)
	}
	for _, fid := range folderIDs {
		if _, err := tx.Exec(ctx, `INSERT INTO sync_device_folders (tenant_id, device_id, folder_id) VALUES ($1,$2,$3)
			ON CONFLICT (tenant_id, device_id, folder_id) DO NOTHING`, tenantID, deviceID, fid); err != nil {
			return mapPgError(err)
		}
	}
	return nil
}
