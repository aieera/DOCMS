package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
)

func notFoundErr() error { return vdmserr.ErrNotFound }

type workspaceRepo struct{}

func (r *workspaceRepo) Create(ctx context.Context, tx pgx.Tx, w *model.Workspace) error {
	settings := w.Settings
	if len(settings) == 0 {
		settings = []byte(`{}`)
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO workspaces (
			tenant_id, id, name, description, settings, region_pin,
			created_by, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $8)
	`, w.TenantID, w.ID, w.Name, w.Description, settings, w.RegionPin,
		w.CreatedBy, w.CreatedAt)
	return mapPgError(err)
}

// AddMember inserts a workspace_members row. Idempotent via
// ON CONFLICT DO NOTHING so the auto-add on creation is safe to
// re-run during a transaction retry. role must be one of
// admin/member/viewer (CHECK constraint enforces it).
func (r *workspaceRepo) AddMember(
	ctx context.Context, tx pgx.Tx,
	tenantID, workspaceID, userID, addedBy uuid.UUID,
	role string,
) error {
	if role == "" {
		role = "admin"
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO workspace_members (tenant_id, workspace_id, user_id, role, added_by, added_at)
		VALUES ($1, $2, $3, $4, $5, now())
		ON CONFLICT (tenant_id, workspace_id, user_id) DO NOTHING
	`, tenantID, workspaceID, userID, role, addedBy)
	return mapPgError(err)
}

func (r *workspaceRepo) GetByID(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) (*model.Workspace, error) {
	row := tx.QueryRow(ctx, `
		SELECT w.tenant_id, w.id, w.name, COALESCE(w.description, ''),
		       COALESCE(w.region_pin, ''), w.settings::text::bytea,
		       w.created_by, w.created_at, w.updated_at, w.deleted_at,
		       COALESCE((SELECT count(*) FROM documents d
		                 WHERE d.tenant_id = w.tenant_id AND d.workspace_id = w.id
		                   AND d.deleted_at IS NULL), 0),
		       COALESCE((SELECT count(*) FROM folders f
		                 WHERE f.tenant_id = w.tenant_id AND f.workspace_id = w.id
		                   AND f.deleted_at IS NULL), 0)
		FROM workspaces w
		WHERE w.tenant_id = $1 AND w.id = $2 AND w.deleted_at IS NULL
	`, tenantID, id)
	return scanWorkspace(row)
}

// List returns the workspaces the caller can access.
//
// Tenant owner/admin sees every active workspace (the gateway grants
// them cross-workspace access anyway). Members see only:
//   - workspaces they created, OR
//   - workspaces they're in via workspace_members.
//
// Previously this returned every workspace in the tenant — the UI
// then had to render "No access" hints because a member's click hit
// 403 on the inner /documents call. With per-caller filtering, the
// frontend just renders whatever comes back.
func (r *workspaceRepo) List(ctx context.Context, tx pgx.Tx, tenantID, userID uuid.UUID, role string) ([]model.Workspace, error) {
	const baseSelect = `
		SELECT w.tenant_id, w.id, w.name, COALESCE(w.description, ''),
		       COALESCE(w.region_pin, ''), w.settings::text::bytea,
		       w.created_by, w.created_at, w.updated_at, w.deleted_at,
		       COALESCE((SELECT count(*) FROM documents d
		                 WHERE d.tenant_id = w.tenant_id AND d.workspace_id = w.id
		                   AND d.deleted_at IS NULL), 0),
		       COALESCE((SELECT count(*) FROM folders f
		                 WHERE f.tenant_id = w.tenant_id AND f.workspace_id = w.id
		                   AND f.deleted_at IS NULL), 0)
		  FROM workspaces w
		 WHERE w.tenant_id = $1 AND w.deleted_at IS NULL`

	var (
		rows pgx.Rows
		err  error
	)
	if role == "owner" || role == "admin" {
		rows, err = tx.Query(ctx, baseSelect+" ORDER BY w.created_at ASC, w.id ASC", tenantID)
	} else {
		// Grantee-only access: a folder grant alone admits the caller
		// to the workspace shell (read-only, scoped to the granted
		// folder(s)). Group grants resolve via the precomputed
		// userGroups slice on ctx; we deliberately ignore them in the
		// non-admin gate here and rely on a separate UNION query for
		// the grantee case to keep this hot path's plan stable.
		// Direct user grants are the common case; group grants are
		// surfaced via the dedicated /folders/shared-with-me endpoint.
		rows, err = tx.Query(ctx, baseSelect+`
		   AND (
		     w.created_by = $2
		     OR EXISTS (
		       SELECT 1 FROM workspace_members wm
		        WHERE wm.tenant_id    = w.tenant_id
		          AND wm.workspace_id = w.id
		          AND wm.user_id      = $2
		     )
		     OR EXISTS (
		       SELECT 1 FROM folder_grants fg
		         JOIN folders f
		           ON f.tenant_id = fg.tenant_id
		          AND f.id        = fg.folder_id
		        WHERE fg.tenant_id   = w.tenant_id
		          AND f.workspace_id = w.id
		          AND fg.grantee_type = 'user'
		          AND fg.grantee_id   = $2
		     )
		   )
		 ORDER BY w.created_at ASC, w.id ASC`, tenantID, userID)
	}
	if err != nil {
		return nil, mapPgError(err)
	}
	defer rows.Close()
	var out []model.Workspace
	for rows.Next() {
		w, err := scanWorkspace(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *w)
	}
	return out, rows.Err()
}

func (r *workspaceRepo) Update(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, name, description string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE workspaces
		   SET name = COALESCE(NULLIF($3, ''), name),
		       description = CASE WHEN $4::text IS NULL THEN description ELSE $4 END,
		       updated_at = $5
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, name, description, time.Now().UTC())
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr()
	}
	return nil
}

// UpdateCreatedBy reassigns the workspace's creator (the "owner" in the
// single-owner model). Used by TransferWorkspaceOwnership. Callers gate
// on tenant role / current-creator BEFORE invoking this.
func (r *workspaceRepo) UpdateCreatedBy(ctx context.Context, tx pgx.Tx, tenantID, id, newCreatedBy uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE workspaces
		   SET created_by = $3, updated_at = now()
		 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id, newCreatedBy)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr()
	}
	return nil
}

// IsMember reports whether the given user has any active workspace_members
// row for the workspace. Used by TransferWorkspaceOwnership to require the
// new owner already be involved with the workspace.
func (r *workspaceRepo) IsMember(ctx context.Context, tx pgx.Tx, tenantID, workspaceID, userID uuid.UUID) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM workspace_members
			WHERE tenant_id = $1 AND workspace_id = $2 AND user_id = $3
		)
	`, tenantID, workspaceID, userID).Scan(&ok)
	if err != nil {
		return false, mapPgError(err)
	}
	return ok, nil
}

func (r *workspaceRepo) SoftDelete(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID) error {
	tag, err := tx.Exec(ctx, `
		UPDATE workspaces SET deleted_at = now()
		WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NULL
	`, tenantID, id)
	if err != nil {
		return mapPgError(err)
	}
	if tag.RowsAffected() == 0 {
		return notFoundErr()
	}
	return nil
}

type workspaceRow interface {
	Scan(dest ...any) error
}

func scanWorkspace(row workspaceRow) (*model.Workspace, error) {
	var w model.Workspace
	err := row.Scan(
		&w.TenantID, &w.ID, &w.Name, &w.Description,
		&w.RegionPin, &w.Settings,
		&w.CreatedBy, &w.CreatedAt, &w.UpdatedAt, &w.DeletedAt,
		&w.DocumentCount, &w.FolderCount,
	)
	if err != nil {
		return nil, mapPgError(err)
	}
	return &w, nil
}
