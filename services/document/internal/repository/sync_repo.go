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
	// Delta pages the feed for one reader: rows outside the reader's set
	// are filtered in SQL, so keyset pagination stays exact.
	Delta(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, workspaceID *uuid.UUID, token string, limit int, reader SyncReader) ([]model.SyncChange, string, bool, error)

	// DocSensitivity returns the classification inputs for the given
	// documents (deleted rows included) so the service can apply the
	// per-document view check to a delta page.
	DocSensitivity(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]SyncDocSensitivity, error)

	RegisterDevice(ctx context.Context, tx pgx.Tx, d model.SyncDevice) (uuid.UUID, error)
	ListDevices(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID) ([]model.SyncDevice, error)
	GetDevice(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.SyncDevice, error)
	UpdateCursor(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, cursor string) error
	RevokeDevice(ctx context.Context, tx pgx.Tx, tenantID, id, by uuid.UUID) (bool, error)
	SetSelectiveFolders(ctx context.Context, tx pgx.Tx, tenantID, deviceID uuid.UUID, folderIDs []uuid.UUID) error
}

// SyncReader is who the delta is computed for (D-8). All (tenant
// owner/admin) sees every row; anyone else sees shared folders, private
// folders they own or hold a folder grant on (directly or through one of
// Groups), and only documents filed in such folders — the same reader set
// as the folder listing and FilterAccessibleFolderIDs. Tombstones follow
// the same rule (deleted rows stay attributed to their folder).
type SyncReader struct {
	UserID uuid.UUID
	Groups []uuid.UUID
	All    bool
}

// SyncDocSensitivity is what the classification gate needs about a document.
type SyncDocSensitivity struct {
	SecurityClassification string
	HasPHI, HasPII         bool
}

type syncRepo struct{}

func (r *syncRepo) DocSensitivity(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, ids []uuid.UUID) (map[uuid.UUID]SyncDocSensitivity, error) {
	out := make(map[uuid.UUID]SyncDocSensitivity, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, security_classification, has_phi, has_pii
		  FROM documents WHERE tenant_id = $1 AND id = ANY($2::uuid[])`, tenantID, ids)
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		var v SyncDocSensitivity
		if err := rows.Scan(&id, &v.SecurityClassification, &v.HasPHI, &v.HasPII); err != nil {
			return nil, mapPgError(err)
		}
		out[id] = v
	}
	return out, mapPgError(rows.Err())
}

func NewSyncRepo() SyncRepository { return &syncRepo{} }

// SyncCursorEncode/Decode wrap the shared keyset cursor for the sync feed so the
// service (a different package) can build a first-page/empty cursor.
func SyncCursorEncode(t time.Time, id uuid.UUID) string {
	return encodeCursor(cursor{Sort: "sync", Time: t, ID: id})
}

func (r *syncRepo) Delta(ctx context.Context, tx pgx.Tx, tenantID uuid.UUID, workspaceID *uuid.UUID, token string, limit int, reader SyncReader) (changes []model.SyncChange, nextToken string, more bool, err error) {
	limit = clampPageSize(limit)
	var curTime *time.Time
	var curID uuid.UUID
	if c, ok := decodeCursor(token); ok && c.Sort == "sync" {
		t := c.Time
		curTime = &t
		curID = c.ID
	}
	// No content hash in the feed (D-1): a sha256 is a blob identity here
	// (dedup, ERP blob_checksum relinking), not client metadata.
	rows, err := tx.Query(ctx, `
		SELECT kind, id, updated_at, is_deleted, parent, name, path, version_id, size_bytes, mime
		FROM (
			SELECT 'folder'::text AS kind, f.id, f.updated_at, (f.deleted_at IS NOT NULL) AS is_deleted,
			       f.parent_folder_id AS parent, f.name, f.path::text AS path,
			       NULL::uuid AS version_id, 0::bigint AS size_bytes, ''::text AS mime
			FROM folders f
			WHERE f.tenant_id = $1 AND ($2::uuid IS NULL OR f.workspace_id = $2)
			  AND ($6::boolean
			       OR f.visibility = 'shared'
			       OR f.owner_id = $7
			       OR EXISTS (
			            SELECT 1 FROM folder_grants fg
			             WHERE fg.tenant_id = f.tenant_id AND fg.folder_id = f.id
			               AND ((fg.grantee_type = 'user'  AND fg.grantee_id = $7)
			                 OR (fg.grantee_type = 'group' AND fg.grantee_id = ANY($8::uuid[])))))
			UNION ALL
			SELECT 'document'::text AS kind, d.id, d.updated_at, (d.deleted_at IS NOT NULL) AS is_deleted,
			       d.folder_id AS parent, d.title AS name, ''::text AS path,
			       d.current_version_id, COALESCE(d.total_size_bytes,0), COALESCE(d.mime_type,'')
			FROM documents d
			LEFT JOIN folders pf ON pf.tenant_id = d.tenant_id AND pf.id = d.folder_id
			WHERE d.tenant_id = $1 AND ($2::uuid IS NULL OR d.workspace_id = $2)
			  AND ($6::boolean
			       OR d.folder_id IS NULL
			       OR pf.visibility = 'shared'
			       OR pf.owner_id = $7
			       OR EXISTS (
			            SELECT 1 FROM folder_grants fg
			             WHERE fg.tenant_id = d.tenant_id AND fg.folder_id = d.folder_id
			               AND ((fg.grantee_type = 'user'  AND fg.grantee_id = $7)
			                 OR (fg.grantee_type = 'group' AND fg.grantee_id = ANY($8::uuid[])))))
		) u
		WHERE ($3::timestamptz IS NULL OR (u.updated_at, u.id) > ($3, $4))
		ORDER BY u.updated_at ASC, u.id ASC
		LIMIT $5`,
		tenantID, workspaceID, curTime, curID, limit+1,
		reader.All, reader.UserID, uuidSlice(reader.Groups))
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
			&ch.Path, &verID, &ch.SizeBytes, &ch.Mime); err != nil {
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
