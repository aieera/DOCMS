// Wave 8 Prompt 8.3 — GDPR DSR HTTP endpoints.
//
// Mounted under /api/v1/privacy/*. Each endpoint accepts the
// spec-documented body, inserts a row in privacy_dsr_requests,
// kicks off the matching Temporal workflow, and returns the request
// id so the client can poll GET /privacy/dsr/{id}.
//
// Workflow startup is best-effort: if the temporal client is nil
// (service booted without Temporal), the row is still written and
// the status stays `pending`. An operator can then re-dispatch
// via `dms-admin dsr redispatch <id>` (future wave).
package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"go.temporal.io/sdk/client"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

const (
	dsrTaskQueue = "vaultdms-default"

	workflowExport    = "ExportWorkflow"
	workflowErase     = "EraseWorkflow"
	workflowAnonymize = "AnonymizeWorkflow"
)

// PrivacyHandler mounts /privacy/* REST endpoints.
type PrivacyHandler struct {
	pool *pgxpool.Pool
	tc   client.Client // may be nil if temporal unreachable at boot
	salt string        // tenant-wide anonymization salt (env-injected)
	log  zerolog.Logger
}

// NewPrivacyHandler constructs a handler.
func NewPrivacyHandler(pool *pgxpool.Pool, tc client.Client, salt string, log zerolog.Logger) *PrivacyHandler {
	return &PrivacyHandler{pool: pool, tc: tc, salt: salt, log: log}
}

// Register mounts:
//
//	POST /api/v1/privacy/dsr/export
//	POST /api/v1/privacy/dsr/erase
//	POST /api/v1/privacy/dsr/anonymize
//	GET  /api/v1/privacy/dsr/{id}
//	GET  /api/v1/privacy/dsr
func (h *PrivacyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/privacy/dsr/export", h.submit("export", workflowExport))
	mux.HandleFunc("POST /api/v1/privacy/dsr/erase", h.submit("erase", workflowErase))
	mux.HandleFunc("POST /api/v1/privacy/dsr/anonymize", h.submit("anonymize", workflowAnonymize))
	mux.HandleFunc("GET /api/v1/privacy/dsr/{id}", h.get)
	mux.HandleFunc("GET /api/v1/privacy/dsr", h.list)
	// ADR 0037: structured conflict surface for the kanban detail panel.
	mux.HandleFunc("GET /api/v1/privacy/dsr/{id}/conflicts", h.listConflicts)
	mux.HandleFunc("POST /api/v1/privacy/dsr/{id}/conflicts/{conflict_id}/resolve", h.resolveConflict)
}

type submitBody struct {
	SubjectEmail      string `json:"subject_email"`
	VerificationToken string `json:"verification_token,omitempty"`
}

func (h *PrivacyHandler) submit(requestType, workflowName string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tenantID, userID, ok := callers(w, r)
		if !ok {
			return
		}
		var body submitBody
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, r, vdmserr.Validation("body", "invalid json"))
			return
		}
		if body.SubjectEmail == "" {
			writeErr(w, r, vdmserr.Validation("subject_email", "required"))
			return
		}
		if requestType == "erase" && body.VerificationToken == "" {
			writeErr(w, r, vdmserr.Validation("verification_token", "required for erase"))
			return
		}

		requestID, err := uuid.NewV7()
		if err != nil {
			writeErr(w, r, vdmserr.Wrap(vdmserr.ErrInternal, err))
			return
		}

		// Insert the request row inside a tenant-scoped tx.
		err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
			_, err := tx.Exec(r.Context(), `
				INSERT INTO privacy_dsr_requests
				    (tenant_id, id, request_type, subject_email, requested_by, status)
				VALUES ($1, $2, $3, $4, $5, 'pending')`,
				tenantID, requestID, requestType, body.SubjectEmail, userID,
			)
			return err
		})
		if err != nil {
			writeErr(w, r, vdmserr.FromPgError(err))
			return
		}

		// Best-effort workflow start.
		if h.tc != nil {
			input := map[string]any{
				"tenant_id":          tenantID.String(),
				"request_id":         requestID.String(),
				"subject_email":      body.SubjectEmail,
				"requested_by":       userID.String(),
				"verification_token": body.VerificationToken,
				"tenant_salt":        h.salt,
			}
			_, werr := h.tc.ExecuteWorkflow(r.Context(),
				client.StartWorkflowOptions{
					ID:        "dsr-" + requestID.String(),
					TaskQueue: dsrTaskQueue,
				}, workflowName, input,
			)
			if werr != nil {
				h.log.Warn().Err(werr).Str("request_id", requestID.String()).
					Msg("workflow start failed; request stays pending for operator redispatch")
			}
		} else {
			h.log.Warn().Str("request_id", requestID.String()).
				Msg("temporal client nil; request stays pending")
		}

		writeJSONStatus(w, http.StatusAccepted, map[string]any{
			"request_id":   requestID.String(),
			"status":       "pending",
			"status_url":   fmt.Sprintf("/api/v1/privacy/dsr/%s", requestID.String()),
			"request_type": requestType,
			"created_at":   time.Now().UTC().Format(time.RFC3339),
		})
	}
}

func (h *PrivacyHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	row := map[string]any{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var (
			status, requestType, subject string
			workflowRunID, exportURL     *string
			expires, completed           *time.Time
			summary                      []byte
			blocked                      *string
			created                      time.Time
		)
		err := tx.QueryRow(r.Context(), `
			SELECT request_type, subject_email, status, workflow_run_id,
			       export_url, export_url_expires_at, result_summary,
			       blocked_reason, created_at, completed_at
			  FROM privacy_dsr_requests
			 WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&requestType, &subject, &status, &workflowRunID,
			&exportURL, &expires, &summary, &blocked, &created, &completed)
		if err != nil {
			return err
		}
		row["id"] = id.String()
		row["request_type"] = requestType
		row["subject_email"] = subject
		row["status"] = status
		row["created_at"] = created
		if completed != nil {
			row["completed_at"] = completed
		}
		if workflowRunID != nil {
			row["workflow_run_id"] = *workflowRunID
		}
		if exportURL != nil {
			row["export_url"] = *exportURL
			row["export_url_expires_at"] = expires
		}
		if blocked != nil {
			row["blocked_reason"] = *blocked
		}
		if len(summary) > 0 {
			var s any
			_ = json.Unmarshal(summary, &s)
			row["result_summary"] = s
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, row)
}

func (h *PrivacyHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	status := q.Get("status")
	switch status {
	case "", "pending", "running", "completed", "blocked", "failed":
	default:
		writeErr(w, r, vdmserr.Validation("status", "invalid"))
		return
	}
	// ADR 0037 augmented columns: due_at (SLA countdown for kanban),
	// intake_source ('admin' vs 'public_form'), and the verification
	// timestamp + open conflict count for the conflict surface. Older
	// clients ignore the extra JSON fields.
	sql := `SELECT r.id::text, r.request_type, r.subject_email, r.status,
	               r.created_at, r.completed_at, r.due_at,
	               COALESCE(r.intake_source, 'admin'),
	               r.requester_identity_verified_at,
	               COALESCE(r.blocked_reason, ''),
	               (SELECT COUNT(*) FROM dsr_conflicts c
	                 WHERE c.tenant_id = r.tenant_id
	                   AND c.request_id = r.id
	                   AND c.resolved_at IS NULL)::int AS open_conflicts
	          FROM privacy_dsr_requests r
	         WHERE r.tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		sql += " AND r.status = $2"
		args = append(args, status)
	}
	sql += " ORDER BY r.created_at DESC LIMIT 200"

	out := []map[string]any{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				id, rt, subj, st, intakeSrc, blockedReason string
				created                                    time.Time
				// due_at is nullable on the schema — must be a pointer scan,
				// not time.Time, or pgx panics on NULL (CLAUDE.md scanner
				// discipline). completed_at + verifiedAt are also nullable.
				dueAt, completed, verifiedAt               *time.Time
				openConflicts                              int
			)
			if err := rows.Scan(&id, &rt, &subj, &st, &created, &completed, &dueAt,
				&intakeSrc, &verifiedAt, &blockedReason, &openConflicts); err != nil {
				return err
			}
			m := map[string]any{
				"id":             id,
				"request_type":   rt,
				"subject_email":  subj,
				"status":         st,
				"created_at":     created,
				"intake_source":  intakeSrc,
				"open_conflicts": openConflicts,
			}
			if dueAt != nil {
				m["due_at"] = dueAt
			}
			if completed != nil {
				m["completed_at"] = completed
			}
			if verifiedAt != nil {
				m["requester_identity_verified_at"] = verifiedAt
			}
			if blockedReason != "" {
				m["blocked_reason"] = blockedReason
			}
			out = append(out, m)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// listConflicts returns dsr_conflicts rows for the request, both
// resolved and unresolved. The kanban detail card iterates these to
// render the resolution panel; resolved rows stay visible as audit
// trail.
func (h *PrivacyHandler) listConflicts(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	out := []map[string]any{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id::text, conflict_type, conflict_details,
			       resolved_by, resolved_at, COALESCE(resolution_action, ''),
			       COALESCE(resolution_notes, ''), created_at
			  FROM dsr_conflicts
			 WHERE tenant_id = $1 AND request_id = $2
			 ORDER BY created_at ASC
		`, tenantID, id)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var (
				cid, ctype, action, notes string
				details                   []byte
				resolvedBy                *uuid.UUID
				resolvedAt                *time.Time
				createdAt                 time.Time
			)
			if err := rows.Scan(&cid, &ctype, &details, &resolvedBy, &resolvedAt,
				&action, &notes, &createdAt); err != nil {
				return err
			}
			row := map[string]any{
				"id":                cid,
				"conflict_type":     ctype,
				"created_at":        createdAt,
				"resolution_action": action,
				"resolution_notes":  notes,
			}
			if len(details) > 0 {
				var d any
				_ = json.Unmarshal(details, &d)
				row["conflict_details"] = d
			}
			if resolvedBy != nil {
				row["resolved_by"] = resolvedBy.String()
			}
			if resolvedAt != nil {
				row["resolved_at"] = resolvedAt
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"conflicts": out})
}

type resolveConflictBody struct {
	Action string `json:"action"` // partial_erase | release_hold | reject_request | other
	Notes  string `json:"notes"`
}

// resolveConflict stamps resolved_by + resolved_at + resolution_action
// + resolution_notes on a single conflict row. Idempotent — second
// call against the same row is a no-op (WHERE resolved_at IS NULL).
//
// Per ADR 0037 §"What we did not do", this handler does NOT itself
// release any legal hold. resolution_action='release_hold' merely
// records that an officer acted; the actual release goes through the
// existing /compliance/holds/{id}/release endpoint with its own
// audit trail and authorization. Coupling the two would create an
// erasure-via-DSR backdoor around the hold gate.
func (h *PrivacyHandler) resolveConflict(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	requestID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	conflictID, err := uuid.Parse(r.PathValue("conflict_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("conflict_id", "invalid uuid"))
		return
	}
	var body resolveConflictBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Action == "" {
		writeErr(w, r, vdmserr.Validation("action", "required"))
		return
	}
	switch body.Action {
	case "partial_erase", "release_hold", "reject_request", "other":
	default:
		writeErr(w, r, vdmserr.Validation("action",
			"must be one of partial_erase | release_hold | reject_request | other"))
		return
	}

	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			UPDATE dsr_conflicts
			   SET resolved_by = $1,
			       resolved_at = now(),
			       resolution_action = $2,
			       resolution_notes = NULLIF($3, '')
			 WHERE tenant_id = $4 AND id = $5 AND request_id = $6
			   AND resolved_at IS NULL`,
			userID, body.Action, body.Notes, tenantID, conflictID, requestID,
		)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.Conflict("conflict not found or already resolved")
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"ok": true, "action": body.Action})
}

// unused import guard — context is indirectly referenced via r.Context().
var _ = context.Background
var _ = fmt.Sprintf
