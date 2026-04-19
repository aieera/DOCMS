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
	sql := `SELECT id::text, request_type, subject_email, status, created_at, completed_at
	          FROM privacy_dsr_requests WHERE tenant_id = $1`
	args := []any{tenantID}
	if status != "" {
		sql += " AND status = $2"
		args = append(args, status)
	}
	sql += " ORDER BY created_at DESC LIMIT 200"

	out := []map[string]any{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), sql, args...)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var id, rt, subj, st string
			var created time.Time
			var completed *time.Time
			if err := rows.Scan(&id, &rt, &subj, &st, &created, &completed); err != nil {
				return err
			}
			m := map[string]any{
				"id":            id,
				"request_type":  rt,
				"subject_email": subj,
				"status":        st,
				"created_at":    created,
			}
			if completed != nil {
				m["completed_at"] = completed
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

// unused import guard — context is indirectly referenced via r.Context().
var _ = context.Background
