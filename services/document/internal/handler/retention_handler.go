// Wave 10 — tenant admin CRUD for retention_policies.
//
//	GET    /api/v1/admin/retention-policies
//	POST   /api/v1/admin/retention-policies
//	GET    /api/v1/admin/retention-policies/{id}
//	PATCH  /api/v1/admin/retention-policies/{id}
//	DELETE /api/v1/admin/retention-policies/{id}
//
// The table `retention_policies` exists from migration 000001 but had
// no read/write endpoints — customers had to edit SQL directly.
// Wired under a dedicated mux in main.go. Headers-based auth
// matches the other admin mux handlers (compliance, privacy,
// residency, share-links).
//
// Rules engine note: this handler is CRUD only. Evaluating which
// documents a policy applies to at upload time is a separate task —
// see the Wave 8.1 out-of-scope "Retention-policy rules engine"
// entry. The Wave 8.1 retention cron already consumes
// `documents.retention_until` which is set by the engine once it
// ships.
package handler

import (
	"context"
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

// RetentionPolicyHandler mounts retention-policy CRUD routes.
type RetentionPolicyHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// NewRetentionPolicyHandler constructs a handler.
func NewRetentionPolicyHandler(pool *pgxpool.Pool, log zerolog.Logger) *RetentionPolicyHandler {
	return &RetentionPolicyHandler{pool: pool, log: log}
}

// Register attaches routes.
func (h *RetentionPolicyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/retention-policies", h.list)
	mux.HandleFunc("POST /api/v1/admin/retention-policies", h.create)
	mux.HandleFunc("GET /api/v1/admin/retention-policies/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/admin/retention-policies/{id}", h.update)
	mux.HandleFunc("DELETE /api/v1/admin/retention-policies/{id}", h.delete)
}

type retentionPolicyDTO struct {
	ID                  string    `json:"id"`
	Name                string    `json:"name"`
	Description         string    `json:"description,omitempty"`
	DocumentClassFilter string    `json:"document_class_filter,omitempty"`
	TagFilter           []string  `json:"tag_filter,omitempty"`
	WorkspaceFilter     string    `json:"workspace_filter,omitempty"`
	RetainDays          int       `json:"retain_days"`
	ThenAction          string    `json:"then_action"` // archive | dispose
	ArchiveDays         int       `json:"archive_days,omitempty"`
	IsActive            bool      `json:"is_active"`
	CreatedBy           string    `json:"created_by,omitempty"`
	CreatedAt           time.Time `json:"created_at"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type createPolicyBody struct {
	Name                string   `json:"name"`
	Description         string   `json:"description"`
	DocumentClassFilter string   `json:"document_class_filter,omitempty"`
	TagFilter           []string `json:"tag_filter,omitempty"`
	WorkspaceFilter     string   `json:"workspace_filter,omitempty"`
	RetainDays          int      `json:"retain_days"`
	ThenAction          string   `json:"then_action"`
	ArchiveDays         int      `json:"archive_days,omitempty"`
	IsActive            *bool    `json:"is_active,omitempty"`
}

type updatePolicyBody struct {
	Name                *string  `json:"name,omitempty"`
	Description         *string  `json:"description,omitempty"`
	DocumentClassFilter *string  `json:"document_class_filter,omitempty"`
	TagFilter           []string `json:"tag_filter,omitempty"`
	WorkspaceFilter     *string  `json:"workspace_filter,omitempty"`
	RetainDays          *int     `json:"retain_days,omitempty"`
	ThenAction          *string  `json:"then_action,omitempty"`
	ArchiveDays         *int     `json:"archive_days,omitempty"`
	IsActive            *bool    `json:"is_active,omitempty"`
}

func (h *RetentionPolicyHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	out := []retentionPolicyDTO{}
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id::text, name, COALESCE(description,''), COALESCE(document_class_filter,''),
			       COALESCE(tag_filter, '{}'::text[]), COALESCE(workspace_filter::text, ''),
			       retain_days, then_action, COALESCE(archive_days, 0),
			       is_active, COALESCE(created_by::text,''), created_at, updated_at
			  FROM retention_policies
			 WHERE tenant_id = $1
			 ORDER BY name`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var p retentionPolicyDTO
			if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.DocumentClassFilter,
				&p.TagFilter, &p.WorkspaceFilter, &p.RetainDays, &p.ThenAction, &p.ArchiveDays,
				&p.IsActive, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt); err != nil {
				return err
			}
			out = append(out, p)
		}
		return rows.Err()
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *RetentionPolicyHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var p retentionPolicyDTO
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			SELECT id::text, name, COALESCE(description,''), COALESCE(document_class_filter,''),
			       COALESCE(tag_filter, '{}'::text[]), COALESCE(workspace_filter::text, ''),
			       retain_days, then_action, COALESCE(archive_days, 0),
			       is_active, COALESCE(created_by::text,''), created_at, updated_at
			  FROM retention_policies WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&p.ID, &p.Name, &p.Description, &p.DocumentClassFilter,
			&p.TagFilter, &p.WorkspaceFilter, &p.RetainDays, &p.ThenAction, &p.ArchiveDays,
			&p.IsActive, &p.CreatedBy, &p.CreatedAt, &p.UpdatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("policy not found")
			}
			return err
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusOK, p)
}

func (h *RetentionPolicyHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	var body createPolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Name == "" {
		writeErr(w, r, vdmserr.Validation("name", "required"))
		return
	}
	if body.RetainDays <= 0 {
		writeErr(w, r, vdmserr.Validation("retain_days", "must be > 0"))
		return
	}
	if body.ThenAction != "archive" && body.ThenAction != "dispose" {
		writeErr(w, r, vdmserr.Validation("then_action", "must be archive or dispose"))
		return
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	var wsFilter any
	if body.WorkspaceFilter != "" {
		ws, err := uuid.Parse(body.WorkspaceFilter)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("workspace_filter", "not a uuid"))
			return
		}
		wsFilter = ws
	}
	var archiveDays any
	if body.ArchiveDays > 0 {
		archiveDays = body.ArchiveDays
	}
	id := uuid.New()
	err := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			INSERT INTO retention_policies
			    (tenant_id, id, name, description, document_class_filter, tag_filter,
			     workspace_filter, retain_days, then_action, archive_days, is_active, created_by)
			VALUES ($1,$2,$3,NULLIF($4,''),NULLIF($5,''),$6,$7,$8,$9,$10,$11,$12)`,
			tenantID, id, body.Name, body.Description, body.DocumentClassFilter, body.TagFilter,
			wsFilter, body.RetainDays, body.ThenAction, archiveDays, active, userID,
		)
		return err
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{
		"id":   id.String(),
		"name": body.Name,
	})
}

func (h *RetentionPolicyHandler) update(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body updatePolicyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.ThenAction != nil && *body.ThenAction != "archive" && *body.ThenAction != "dispose" {
		writeErr(w, r, vdmserr.Validation("then_action", "must be archive or dispose"))
		return
	}
	if body.RetainDays != nil && *body.RetainDays <= 0 {
		writeErr(w, r, vdmserr.Validation("retain_days", "must be > 0"))
		return
	}
	// Workspace filter validation when set.
	var wsFilter any
	if body.WorkspaceFilter != nil && *body.WorkspaceFilter != "" {
		ws, err := uuid.Parse(*body.WorkspaceFilter)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("workspace_filter", "not a uuid"))
			return
		}
		wsFilter = ws
	}

	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(), `
			UPDATE retention_policies
			   SET name                  = COALESCE($1, name),
			       description           = COALESCE($2, description),
			       document_class_filter = COALESCE(NULLIF($3, ''), document_class_filter),
			       tag_filter            = COALESCE($4, tag_filter),
			       workspace_filter      = COALESCE($5, workspace_filter),
			       retain_days           = COALESCE($6, retain_days),
			       then_action           = COALESCE($7, then_action),
			       archive_days          = COALESCE($8, archive_days),
			       is_active             = COALESCE($9, is_active),
			       updated_at            = now()
			 WHERE tenant_id = $10 AND id = $11`,
			body.Name, body.Description, ptrStrOrNil(body.DocumentClassFilter),
			body.TagFilter, wsFilter, body.RetainDays, body.ThenAction,
			body.ArchiveDays, body.IsActive, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("policy not found")
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *RetentionPolicyHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM retention_policies WHERE tenant_id = $1 AND id = $2`,
			tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("policy not found")
		}
		return nil
	})
	if err != nil {
		writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ptrStrOrNil returns *s if s is non-nil and non-empty, otherwise nil.
// We want COALESCE to skip the update when the field was omitted, and
// a pointer-to-empty-string would otherwise overwrite with empty.
func ptrStrOrNil(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

var _ = context.Background // keep context reachable
