// iPaaS trigger endpoint — ADR 0090.
// GET /api/v1/integrations/triggers/signatures/completed?since=<rfc3339>&limit=<int>
// Authenticated by API key with scope integrations:read.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vaultdms/vaultdms/pkg/auth"
)

type IntegrationTriggersHandler struct{ pool *pgxpool.Pool }

func NewIntegrationTriggersHandler(pool *pgxpool.Pool) *IntegrationTriggersHandler {
	return &IntegrationTriggersHandler{pool: pool}
}

func (h *IntegrationTriggersHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/integrations/triggers/signatures/completed", h.signaturesCompleted)
}

type triggerSignature struct {
	ID          string    `json:"id"`
	DocumentID  string    `json:"document_id"`
	VersionID   string    `json:"version_id"`
	Status      string    `json:"status"`
	Provider    string    `json:"provider"`
	CompletedAt time.Time `json:"completed_at"`
	CreatedAt   time.Time `json:"created_at"`
}

func (h *IntegrationTriggersHandler) signaturesCompleted(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil {
		http.Error(w, `{"error":"tenant required"}`, http.StatusUnauthorized)
		return
	}
	since := time.Now().Add(-1 * time.Hour)
	if v := r.URL.Query().Get("since"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			http.Error(w, `{"error":"since must be RFC3339"}`, http.StatusBadRequest)
			return
		}
		since = t
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n <= 0 {
			http.Error(w, `{"error":"limit must be positive int"}`, http.StatusBadRequest)
			return
		}
		if n > 100 {
			n = 100
		}
		limit = n
	}
	rows, err := h.pool.Query(r.Context(), `
		SELECT id, document_id, version_id, status, provider, completed_at, created_at
		FROM signature_requests
		WHERE tenant_id = $1
		  AND status = 'completed'
		  AND completed_at IS NOT NULL
		  AND completed_at > $2
		ORDER BY completed_at ASC
		LIMIT $3`,
		tenantID, since, limit)
	if err != nil {
		http.Error(w, `{"error":"query failed"}`, http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := make([]triggerSignature, 0)
	for rows.Next() {
		var s triggerSignature
		var completedAt *time.Time
		if err := rows.Scan(&s.ID, &s.DocumentID, &s.VersionID, &s.Status,
			&s.Provider, &completedAt, &s.CreatedAt); err != nil {
			http.Error(w, `{"error":"scan failed"}`, http.StatusInternalServerError)
			return
		}
		if completedAt != nil {
			s.CompletedAt = *completedAt
		}
		out = append(out, s)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
