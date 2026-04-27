// GAP-4 — soft-delete trash + restore.
//
//	GET  /api/v1/admin/documents/trash             list soft-deleted docs
//	POST /api/v1/admin/documents/{id}/restore      undo soft-delete
//
// Admin/owner-gated. The route shape lives under /api/v1/admin/documents
// alongside the existing share-links admin endpoints — same Vite proxy
// entry, same role gate, no new infrastructure.

package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// TrashHandler mounts the trash list + restore endpoints.
type TrashHandler struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewTrashHandler constructs a TrashHandler.
func NewTrashHandler(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) *TrashHandler {
	return &TrashHandler{pool: pool, outbox: outbox, log: log}
}

// Register attaches routes to a mux.
func (h *TrashHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/documents/trash", h.list)
	mux.HandleFunc("POST /api/v1/admin/documents/{id}/restore", h.restore)
}

type trashItem struct {
	ID            string    `json:"id"`
	Title         string    `json:"title"`
	WorkspaceID   string    `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name,omitempty"`
	FolderID      string    `json:"folder_id,omitempty"`
	MimeType      string    `json:"mime_type,omitempty"`
	SizeBytes     int64     `json:"size_bytes"`
	DeletedAt     time.Time `json:"deleted_at"`
	CreatedBy     string    `json:"created_by,omitempty"`
}

func (h *TrashHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "admin", "owner") {
		return
	}
	out := []trashItem{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT d.id::text, d.title, d.workspace_id::text,
			       COALESCE(w.name, ''),
			       COALESCE(d.folder_id::text, ''),
			       COALESCE(d.mime_type, ''),
			       d.total_size_bytes,
			       d.deleted_at,
			       COALESCE(d.created_by::text, '')
			  FROM documents d
			  LEFT JOIN workspaces w ON w.tenant_id = d.tenant_id AND w.id = d.workspace_id
			 WHERE d.tenant_id = $1 AND d.deleted_at IS NOT NULL
			 ORDER BY d.deleted_at DESC
			 LIMIT 500`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it trashItem
			if err := rows.Scan(&it.ID, &it.Title, &it.WorkspaceID, &it.WorkspaceName,
				&it.FolderID, &it.MimeType, &it.SizeBytes, &it.DeletedAt, &it.CreatedBy); err != nil {
				return err
			}
			out = append(out, it)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"items": out})
}

// restore clears deleted_at on a document, but only if it isn't on a
// legal hold (held docs that were soft-deleted before the hold was
// applied stay soft-deleted to honor the hold's preservation
// semantics — restore would un-archive evidence the legal team
// expects to find at exactly the deleted state).
func (h *TrashHandler) restore(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "admin", "owner") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		// Refuse to restore a held document. The compliance UI should
		// release the hold first.
		var held bool
		var title string
		if err := tx.QueryRow(r.Context(), `
			SELECT (under_legal_hold OR hold_count > 0), title
			  FROM documents
			 WHERE tenant_id = $1 AND id = $2 AND deleted_at IS NOT NULL`,
			tenantID, id,
		).Scan(&held, &title); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("document not in trash")
			}
			return err
		}
		if held {
			return vdmserr.Conflict("document is under legal hold; release the hold before restoring")
		}
		if _, err := tx.Exec(r.Context(), `
			UPDATE documents SET deleted_at = NULL, updated_at = now(), updated_by = $1
			 WHERE tenant_id = $2 AND id = $3`,
			userID, tenantID, id,
		); err != nil {
			return err
		}
		// Audit emit. dms.document.restored.v1 mirrors the existing
		// dms.document.deleted.v1 / dms.document.created.v1 namespace.
		payload, _ := json.Marshal(map[string]any{
			"actor_id":      userID.String(),
			"tenant_id":     tenantID.String(),
			"action":        "document.restored",
			"resource_type": "document",
			"resource_id":   id.String(),
			"title":         title,
		})
		evt := database.NewOutboxEvent(tenantID, "dms.document.restored.v1", "document", id, payload)
		return h.outbox.Insert(r.Context(), tx, evt)
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true})
}
