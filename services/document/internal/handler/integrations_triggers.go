// iPaaS trigger endpoints — ADR 0090.
//
// Zapier / Make / n8n poll these on a schedule. Each returns a list of
// objects created/updated since a client-supplied RFC3339 timestamp.
// Response order is updated_at ASC so Zapier's "deduplicate by id"
// pattern works correctly. Page size is capped server-side at 100.
//
// Authenticated by API key (Bearer vdms_...) with scope
// "integrations:read". Tenant + user are stamped on ctx by
// middleware.APIKeyAuth before this handler runs.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/pkg/database"
)

// IntegrationTriggersHandler holds the pool. Constructed in main.go
// and mounted under /api/v1/integrations/triggers with the
// APIKeyAuth("integrations:read") middleware applied.
type IntegrationTriggersHandler struct {
	pool *pgxpool.Pool
}

func NewIntegrationTriggersHandler(pool *pgxpool.Pool) *IntegrationTriggersHandler {
	return &IntegrationTriggersHandler{pool: pool}
}

// Register mounts the routes. Caller wraps these with APIKeyAuth.
func (h *IntegrationTriggersHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/integrations/triggers/documents", h.documentsCreated)
}

// triggerItem is the minimal-but-stable shape Zapier polling expects:
// every object MUST have a unique `id` field (used for dedup) and a
// timestamp field for ordering.
type triggerDocument struct {
	ID              string    `json:"id"`
	Title           string    `json:"title"`
	WorkspaceID     string    `json:"workspace_id"`
	FolderID        string    `json:"folder_id"`
	LifecycleState  string    `json:"lifecycle_state"`
	DocumentClass   string    `json:"document_class,omitempty"`
	Tags            []string  `json:"tags"`
	MimeType        string    `json:"mime_type,omitempty"`
	TotalSizeBytes  int64     `json:"total_size_bytes"`
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (h *IntegrationTriggersHandler) documentsCreated(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil {
		http.Error(w, `{"error":"tenant required"}`, http.StatusUnauthorized)
		return
	}
	since, limit, err := parseTriggerParams(r)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadRequest)
		return
	}
	out := make([]triggerDocument, 0)
	qerr := database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, title, workspace_id, folder_id, lifecycle_state,
				document_class, tags, mime_type, total_size_bytes,
				created_at, updated_at
			FROM documents
			WHERE tenant_id = $1
			  AND deleted_at IS NULL
			  AND updated_at > $2
			ORDER BY updated_at ASC
			LIMIT $3`,
			tenantID, since, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d triggerDocument
			var class, mime *string
			if err := rows.Scan(&d.ID, &d.Title, &d.WorkspaceID, &d.FolderID,
				&d.LifecycleState, &class, &d.Tags, &mime, &d.TotalSizeBytes,
				&d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			if class != nil {
				d.DocumentClass = *class
			}
			if mime != nil {
				d.MimeType = *mime
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	if qerr != nil {
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	writeJSONList(w, out)
}

// parseTriggerParams reads ?since=<rfc3339>&limit=<int> and applies
// sane defaults. since defaults to "1 hour ago" so a Zapier poll
// without explicit cursor doesn't return the entire history.
func parseTriggerParams(r *http.Request) (since time.Time, limit int, err error) {
	limit = 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, perr := strconv.Atoi(v)
		if perr != nil || n <= 0 {
			return time.Time{}, 0, errInvalidLimit
		}
		if n > 100 {
			n = 100
		}
		limit = n
	}
	if v := r.URL.Query().Get("since"); v != "" {
		t, perr := time.Parse(time.RFC3339, v)
		if perr != nil {
			return time.Time{}, 0, errInvalidSince
		}
		since = t
	} else {
		since = time.Now().Add(-1 * time.Hour)
	}
	return since, limit, nil
}

var errInvalidLimit = &triggerError{"limit must be a positive integer ≤100"}
var errInvalidSince = &triggerError{"since must be RFC3339 (e.g. 2026-05-17T10:00:00Z)"}

type triggerError struct{ msg string }

func (e *triggerError) Error() string { return e.msg }

func writeJSONList(w http.ResponseWriter, list any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(list)
}

// Ensure ctx import isn't unused if the codebase changes the auth.GetTenantID signature.
var _ = context.TODO
