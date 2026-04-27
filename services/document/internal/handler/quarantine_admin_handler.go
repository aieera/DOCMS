// Admin quarantine review queue backend. Fronts the quarantine_events
// table (services/document/migrations/000012 + 000013) with four HTTP
// endpoints the React /admin/quarantine page calls:
//
//	GET  /api/v1/admin/quarantine                       — list
//	POST /api/v1/admin/quarantine/{id}/release          — re-admit
//	POST /api/v1/admin/quarantine/{id}/delete           — purge
//	POST /api/v1/admin/quarantine/{id}/reviewed         — ack only
//
// Every mutation writes an outbox row under dms.audit.quarantine_*.v1
// inside the same tenant-scoped tx, so the audit chain stays intact and
// the /admin/audit-log page shows the review action. Release itself is a
// *request* — an async worker handles the actual S3 copy from the
// quarantine bucket back to hot. The row status flips to 'released'
// immediately so the admin UI reflects the decision; the worker emits a
// terminal upload_completed.v1 when the bytes land.
//
// Permission: the backend trusts the X-User-Role header for admin/owner;
// the session-auth middleware upstream is responsible for signing that
// header. Non-admin requests get 403. A finer-grained quarantine.review
// policy check lives in OPA (separate concern, tracked in the tech-debt
// ledger).

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// QuarantineAdminHandler owns the /api/v1/admin/quarantine surface.
type QuarantineAdminHandler struct {
	pool   *pgxpool.Pool
	outbox *database.OutboxRepository
	log    zerolog.Logger
}

// NewQuarantineAdminHandler constructs the handler. The outbox repo is
// shared with the rest of the document service — every mutation appends
// to its tenant outbox inside the same tx.
func NewQuarantineAdminHandler(pool *pgxpool.Pool, outbox *database.OutboxRepository, log zerolog.Logger) *QuarantineAdminHandler {
	return &QuarantineAdminHandler{pool: pool, outbox: outbox, log: log}
}

// Register attaches the four routes.
func (h *QuarantineAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/quarantine", h.list)
	mux.HandleFunc("POST /api/v1/admin/quarantine/{id}/release", h.release)
	mux.HandleFunc("POST /api/v1/admin/quarantine/{id}/delete", h.del)
	mux.HandleFunc("POST /api/v1/admin/quarantine/{id}/reviewed", h.reviewed)
}

type quarantineItemDTO struct {
	ID           string    `json:"id"`
	UploadID     string    `json:"upload_id"`
	TenantID     string    `json:"tenant_id"`
	Filename     string    `json:"filename"`
	UploaderID   string    `json:"uploader_id"`
	UploaderName string    `json:"uploader_name"`
	Reason       string    `json:"reason"`
	Signature    string    `json:"signature"`
	DeclaredMIME string    `json:"declared_mime"`
	DetectedMIME string    `json:"detected_mime"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	Status       string    `json:"status"`
	ReviewedBy   string    `json:"reviewed_by,omitempty"`
	ReviewedAt   *time.Time `json:"reviewed_at,omitempty"`
}

type quarantineListResponse struct {
	Items      []quarantineItemDTO `json:"items"`
	TotalCount int                 `json:"total_count"`
}

type actionBody struct {
	Reason string `json:"reason"`
}

// list returns up to 200 most-recent quarantine_events for the caller's
// tenant with a JOIN to upload_sessions + users for filename + uploader.
// 200 is the cap because the admin queue is triage-oriented; pagination
// is out-of-scope until volume justifies it (tracked in the ledger).
func (h *QuarantineAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	var items []quarantineItemDTO
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT
				qe.id, qe.upload_id, qe.tenant_id,
				COALESCE(u.filename, ''),
				COALESCE(u.created_by::text, ''),
				COALESCE(usr.display_name, ''),
				qe.reason, COALESCE(qe.signature, ''),
				COALESCE(qe.declared_mime, ''),
				COALESCE(qe.detected_mime, ''),
				COALESCE(u.total_size, 0),
				qe.created_at, qe.status,
				COALESCE(qe.reviewed_by::text, ''),
				qe.reviewed_at
			FROM quarantine_events qe
			LEFT JOIN upload_sessions u
				ON u.tenant_id = qe.tenant_id AND u.id = qe.upload_id
			LEFT JOIN users usr
				ON usr.tenant_id = qe.tenant_id AND usr.id = u.created_by
			WHERE qe.tenant_id = current_setting('app.current_tenant', true)::uuid
			ORDER BY qe.created_at DESC
			LIMIT 200
		`)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var it quarantineItemDTO
			var reviewedAt *time.Time
			if err := rows.Scan(
				&it.ID, &it.UploadID, &it.TenantID,
				&it.Filename, &it.UploaderID, &it.UploaderName,
				&it.Reason, &it.Signature,
				&it.DeclaredMIME, &it.DetectedMIME,
				&it.SizeBytes,
				&it.CreatedAt, &it.Status,
				&it.ReviewedBy, &reviewedAt,
			); err != nil {
				return err
			}
			it.ReviewedAt = reviewedAt
			items = append(items, it)
		}
		return rows.Err()
	})
	if err != nil {
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, quarantineListResponse{Items: items, TotalCount: len(items)})
}

func (h *QuarantineAdminHandler) release(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "released", "dms.audit.quarantine_released.v1", "quarantine.release")
}

func (h *QuarantineAdminHandler) del(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "deleted", "dms.audit.quarantine_deleted.v1", "quarantine.delete")
}

func (h *QuarantineAdminHandler) reviewed(w http.ResponseWriter, r *http.Request) {
	h.mutate(w, r, "reviewed", "dms.audit.quarantine_reviewed.v1", "quarantine.reviewed")
}

// mutate is the shared body for release / delete / reviewed. Flips the
// row's status, records reviewer + timestamp, and appends a matching
// audit event to the outbox. All in one tenant-scoped tx so partial
// success is impossible.
func (h *QuarantineAdminHandler) mutate(w http.ResponseWriter, r *http.Request, newStatus, eventType, auditAction string) {
	tenantID, userID, ok := h.requireAdmin(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeProxyJSON(w, http.StatusBadRequest, map[string]any{"type": "INVALID_ARGUMENT", "message": "invalid id"})
		return
	}

	// Optional reason body; we audit whatever the admin typed so the
	// review trail has context. Empty body is tolerated — reason
	// defaults to the action name.
	body := actionBody{}
	if r.ContentLength > 0 {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Reason == "" {
		body.Reason = auditAction
	}

	var uploadID uuid.UUID
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		// Single UPDATE with RETURNING — proves the row exists + belongs
		// to this tenant AND we have upload_id for the audit payload, in
		// one round-trip.
		err := tx.QueryRow(r.Context(), `
			UPDATE quarantine_events
			SET status = $1, reviewed_by = $2, reviewed_at = now()
			WHERE tenant_id = current_setting('app.current_tenant', true)::uuid
			  AND id = $3
			RETURNING upload_id
		`, newStatus, userID, id).Scan(&uploadID)
		if err != nil {
			return err
		}
		// Audit fan-out. Subject matches the dms.audit.> stream filter
		// so the audit service ingests it into audit_events and the
		// /admin/audit-log page renders the action.
		payload, _ := json.Marshal(map[string]any{
			"actor_id":      userID.String(),
			"tenant_id":     tenantID.String(),
			"action":        auditAction,
			"resource_type": "upload",
			"resource_id":   uploadID.String(),
			"quarantine_id": id.String(),
			"reason":        body.Reason,
		})
		evt := database.NewOutboxEvent(tenantID, eventType, "upload", uploadID, payload)
		return h.outbox.Insert(r.Context(), tx, evt)
	})
	if err != nil {
		writeProxyErr(w, r, err)
		return
	}
	writeProxyJSON(w, http.StatusOK, map[string]any{"ok": true, "status": newStatus})
}

// requireAdmin parses tenant + user from the session-auth headers and
// returns 403 if the caller's role isn't admin/owner. Returns ok=false
// after writing the response, so handlers can early-exit.
func (h *QuarantineAdminHandler) requireAdmin(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantRaw := r.Header.Get("X-Auth-Tenant-ID")
	userRaw := r.Header.Get("X-User-ID")
	role := r.Header.Get("X-User-Role")
	if role != "admin" && role != "owner" {
		writeProxyJSON(w, http.StatusForbidden, map[string]any{"type": "FORBIDDEN", "message": "admin role required"})
		return uuid.Nil, uuid.Nil, false
	}
	tenantID, err1 := uuid.Parse(tenantRaw)
	userID, err2 := uuid.Parse(userRaw)
	if err1 != nil || err2 != nil || tenantID == uuid.Nil || userID == uuid.Nil {
		writeProxyJSON(w, http.StatusUnauthorized, map[string]any{"type": "UNAUTHORIZED", "message": "missing identity headers"})
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, userID, true
}

// writeProxyErr maps a domain error to an HTTP response reusing the
// canonical ToHTTPError path. Everything else becomes a 500.
func writeProxyErr(w http.ResponseWriter, r *http.Request, err error) {
	he := vdmserr.ToHTTPError(err, auth.GetCorrelationID(r.Context()))
	writeProxyJSON(w, he.Code, he)
}

// Ensure package-level compile dependency on context so the signature
// search in this file doesn't miss the import when we add helpers later.
var _ = context.Background
