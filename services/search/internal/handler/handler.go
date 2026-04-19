// Package handler exposes the REST API for the search service.
// Routes are registered on a standard http.ServeMux.
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/search/internal/model"
	"github.com/vaultdms/vaultdms/services/search/internal/service"
)

// Handler holds HTTP route handlers for the search service.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register mounts all routes onto the supplied mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/search", h.search)
	mux.HandleFunc("GET /api/v1/search/autocomplete", h.autocomplete)
	mux.HandleFunc("POST /api/v1/saved-searches", h.createSavedSearch)
	mux.HandleFunc("GET /api/v1/saved-searches", h.listSavedSearches)
	mux.HandleFunc("DELETE /api/v1/saved-searches/{id}", h.deleteSavedSearch)
	// Wave 12.4: internal DSR subject-erase endpoint. Callers are
	// the workflow service's EraseWorkflow activity, running behind
	// the platform's internal-API-key middleware.
	mux.HandleFunc("POST /internal/v1/search/purge-subject", h.purgeSubject)
}

type purgeSubjectBody struct {
	TenantID  string `json:"tenant_id"`
	SubjectID string `json:"subject_id"`
}

func (h *Handler) purgeSubject(w http.ResponseWriter, r *http.Request) {
	var body purgeSubjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.TenantID == "" || body.SubjectID == "" {
		http.Error(w, "tenant_id and subject_id required", http.StatusBadRequest)
		return
	}
	deleted, err := h.svc.PurgeSubject(r.Context(), body.TenantID, body.SubjectID)
	if err != nil {
		h.log.Error().Err(err).Msg("purge subject")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"deleted": deleted})
}

// ---- search ---------------------------------------------------------------

type searchRequestBody struct {
	Query      string            `json:"query"`
	Filters    filtersBody       `json:"filters"`
	Facets     []string          `json:"facets"`
	SortBy     string            `json:"sort_by"`
	SortOrder  string            `json:"sort_order"`
	PageSize   int               `json:"page_size"`
	PageToken  string            `json:"page_token"`
	SearchMode string            `json:"search_mode"`
	Highlight  bool              `json:"highlight"`
	Explain    bool              `json:"explain"`
}

type filtersBody struct {
	WorkspaceID    string            `json:"workspace_id"`
	FolderID       string            `json:"folder_id"`
	DocumentClass  []string          `json:"document_class"`
	LifecycleState []string          `json:"lifecycle_state"`
	Tags           []string          `json:"tags"`
	CreatedAfter   string            `json:"created_after"`
	CreatedBefore  string            `json:"created_before"`
	MimeType       []string          `json:"mime_type"`
	SizeMinBytes   *int64            `json:"size_min_bytes"`
	SizeMaxBytes   *int64            `json:"size_max_bytes"`
	CreatedBy      string            `json:"created_by"`
	CustomMetadata map[string]string `json:"custom_metadata"`
	HasContent     *bool             `json:"has_content"`
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	groupIDs := splitHeader(r.Header.Get("X-Group-IDs"))

	var body searchRequestBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}

	req := &model.SearchRequest{
		TenantID:  tenantID,
		UserID:    userID,
		GroupIDs:  groupIDs,
		Query:     body.Query,
		// §7.1 / D6 — normalize mode at the edge; unknown values
		// fall back to "lexical" so a stale client can't silently
		// get a degraded search.
		Mode:      model.NormalizeMode(body.SearchMode),
		Facets:    body.Facets,
		SortBy:    body.SortBy,
		SortOrder: body.SortOrder,
		PageSize:  body.PageSize,
		PageToken: body.PageToken,
		Highlight: body.Highlight,
		Explain:   body.Explain,
		Filters: model.SearchFilters{
			WorkspaceID:    body.Filters.WorkspaceID,
			FolderID:       body.Filters.FolderID,
			DocumentClass:  body.Filters.DocumentClass,
			LifecycleState: body.Filters.LifecycleState,
			Tags:           body.Filters.Tags,
			MimeType:       body.Filters.MimeType,
			SizeMinBytes:   body.Filters.SizeMinBytes,
			SizeMaxBytes:   body.Filters.SizeMaxBytes,
			CreatedBy:      body.Filters.CreatedBy,
			CustomMetadata: body.Filters.CustomMetadata,
			HasContent:     body.Filters.HasContent,
		},
	}
	if body.Filters.CreatedAfter != "" {
		if t, err := time.Parse(time.RFC3339, body.Filters.CreatedAfter); err == nil {
			req.Filters.CreatedAfter = &t
		}
	}
	if body.Filters.CreatedBefore != "" {
		if t, err := time.Parse(time.RFC3339, body.Filters.CreatedBefore); err == nil {
			req.Filters.CreatedBefore = &t
		}
	}

	result, err := h.svc.Search(r.Context(), req)
	if err != nil {
		h.log.Error().Err(err).Msg("search failed")
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ---- autocomplete ---------------------------------------------------------

func (h *Handler) autocomplete(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	groupIDs := splitHeader(r.Header.Get("X-Group-IDs"))
	q := r.URL.Query().Get("q")
	if q == "" {
		writeError(w, http.StatusBadRequest, "q parameter required")
		return
	}
	limit := 10
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	result, err := h.svc.Autocomplete(r.Context(), tenantID, userID, groupIDs, q, limit)
	if err != nil {
		h.log.Error().Err(err).Msg("autocomplete failed")
		writeError(w, http.StatusInternalServerError, "autocomplete failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ---- saved searches -------------------------------------------------------

type createSavedSearchBody struct {
	Name                  string      `json:"name"`
	Query                 string      `json:"query"`
	Filters               filtersBody `json:"filters"`
	Notify                bool        `json:"notify"`
	NotifyIntervalMinutes int         `json:"notify_interval_minutes"`
}

func (h *Handler) createSavedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	var body createSavedSearchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Name == "" || body.Query == "" {
		writeError(w, http.StatusBadRequest, "name and query are required")
		return
	}
	if body.NotifyIntervalMinutes <= 0 {
		body.NotifyIntervalMinutes = 15
	}
	ss := &model.SavedSearch{
		TenantID:              tenantID,
		UserID:                userID,
		Name:                  body.Name,
		Query:                 body.Query,
		Notify:                body.Notify,
		NotifyIntervalMinutes: body.NotifyIntervalMinutes,
	}
	if err := h.svc.CreateSavedSearch(r.Context(), ss); err != nil {
		h.log.Error().Err(err).Msg("create saved search failed")
		writeError(w, http.StatusInternalServerError, "create failed")
		return
	}
	writeJSON(w, http.StatusCreated, ss)
}

func (h *Handler) listSavedSearches(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	list, err := h.svc.ListSavedSearches(r.Context(), tenantID, userID)
	if err != nil {
		h.log.Error().Err(err).Msg("list saved searches failed")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if list == nil {
		list = []*model.SavedSearch{}
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handler) deleteSavedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "saved search id required")
		return
	}
	if err := h.svc.DeleteSavedSearch(r.Context(), tenantID, userID, id); err != nil {
		writeError(w, http.StatusNotFound, "saved search not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers --------------------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

func splitHeader(h string) []string {
	if h == "" {
		return nil
	}
	parts := strings.Split(h, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
