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

	"github.com/vaultdms/vaultdms/pkg/auth"
	"github.com/vaultdms/vaultdms/services/search/internal/model"
	"github.com/vaultdms/vaultdms/services/search/internal/repository"
	"github.com/vaultdms/vaultdms/services/search/internal/service"
)

// Handler holds HTTP route handlers for the search service.
type Handler struct {
	svc       *service.Service
	debouncer *service.PermissionDebouncer
	log       zerolog.Logger
}

// New constructs a Handler. debouncer may be nil — when nil the
// /admin/permission-propagation-stats endpoint reports zeros for
// the queue depth (the histogram percentiles still come through
// from the global Prometheus registry).
func New(svc *service.Service, debouncer *service.PermissionDebouncer, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, debouncer: debouncer, log: log}
}

// Register mounts all routes onto the supplied mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/search", h.search)
	// ADR 0082 — GET variant accepts the spec's URL syntax:
	// /search?q=X&facet=tag,author&filter=tag:contract&filter=author:alice
	// Same service-layer entry point as POST; only the parsing differs.
	mux.HandleFunc("GET /api/v1/search", h.searchGET)
	mux.HandleFunc("GET /api/v1/search/autocomplete", h.autocomplete)
	// ADR 0084 — grouped suggester: documents/tags/people/recent.
	mux.HandleFunc("GET /api/v1/search/suggest", h.suggest)
	mux.HandleFunc("POST /api/v1/saved-searches", h.createSavedSearch)
	mux.HandleFunc("GET /api/v1/saved-searches", h.listSavedSearches)
	mux.HandleFunc("DELETE /api/v1/saved-searches/{id}", h.deleteSavedSearch)
	// ADR 0085 — edit + alert + subscribe.
	mux.HandleFunc("PATCH /api/v1/saved-searches/{id}", h.patchSavedSearch)
	mux.HandleFunc("POST /api/v1/saved-searches/{id}/subscribe", h.subscribeSavedSearch)
	mux.HandleFunc("DELETE /api/v1/saved-searches/{id}/subscribe/{user_id}", h.unsubscribeSavedSearch)
	// ADR 0100 — smart folders. Static path comes BEFORE the {id}
	// variant so the mux doesn't try to parse "smart-folders" as a
	// UUID. /promote is the toggle that flips a regular saved search
	// into a tree-visible smart folder (or back).
	mux.HandleFunc("GET /api/v1/saved-searches/smart-folders", h.listSmartFolders)
	mux.HandleFunc("POST /api/v1/saved-searches/{id}/promote", h.promoteSmartFolder)
	mux.HandleFunc("POST /api/v1/saved-searches/{id}/demote", h.demoteSmartFolder)
	// ADR 0069 — platform-admin federated search.
	mux.HandleFunc("POST /api/v1/platform/search/federated", h.federatedSearch)
	mux.HandleFunc("GET /api/v1/platform/search/federated/audit", h.listFederatedAudit)
	// ADR 0083 §"SLI" — admin dashboard for permission-propagation lag.
	// Returns histogram percentiles + counter totals + debouncer
	// queue depth so the page works without Grafana.
	mux.HandleFunc("GET /api/v1/admin/permission-propagation-stats", h.permissionPropagationStats)
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
	// ADR 0083 — share-link follower path. In production this should
	// come from a gateway-injected header rather than a free-text
	// POST body field; allowed here for development + integration
	// testing. The model.SearchRequest field threads it into the
	// share_tokens query clause.
	ShareToken string            `json:"share_token,omitempty"`
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
	// Sidebar "Author" facet filter — display-name set, multi-valued.
	// Mirrors model.SearchFilters.CreatedByName. Was missing from this
	// struct, so the JSON decoder silently dropped the key and Author
	// filtering was a no-op end-to-end.
	CreatedByName  []string          `json:"created_by_name"`
	// Residency-region filter. Same story as CreatedByName — the
	// model has it, the wire body had to be added.
	RegionPin      []string          `json:"region_pin"`
	CustomMetadata map[string]string `json:"custom_metadata"`
	HasContent     *bool             `json:"has_content"`
}

func (h *Handler) search(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
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
		TenantID:   tenantID,
		UserID:     userID,
		GroupIDs:   groupIDs,
		ShareToken: body.ShareToken,
		Query:      body.Query,
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
			CreatedByName:  body.Filters.CreatedByName,
			RegionPin:      body.Filters.RegionPin,
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
	// ADR 0111 — surface the dense-vector degradation as a response
	// header so dashboards + load balancers can count it without
	// parsing the JSON body. Value is the mode that ACTUALLY ran
	// (typically "lexical") — empty header means "no degradation".
	if result.Degraded != "" {
		w.Header().Set("X-Search-Mode-Degraded", result.Degraded)
	}
	writeJSON(w, http.StatusOK, result)
}

// searchGET handles the spec's URL-style search shape (ADR 0082).
// Identity headers + parsing logic live in url_search.go; this
// handler glues them together with the same service.Search() entry
// point used by the POST path.
func (h *Handler) searchGET(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	req := parseSearchRequestFromURL(r)
	result, err := h.svc.Search(r.Context(), req)
	if err != nil {
		h.log.Error().Err(err).Msg("search GET failed")
		writeError(w, http.StatusInternalServerError, "search failed")
		return
	}
	if result.Degraded != "" {
		w.Header().Set("X-Search-Mode-Degraded", result.Degraded)
	}
	writeJSON(w, http.StatusOK, result)
}

// suggest serves the §7.5 / ADR 0084 grouped autocomplete shape.
// Three OpenSearch sources (title / tags / created_by_name) plus
// the user's recent-search ledger, all under the standard tenant +
// readable_by permission filter.
func (h *Handler) suggest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	groups := splitHeader(r.Header.Get("X-Group-IDs"))
	q := r.URL.Query().Get("q")
	limit := 10
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}

	req := &model.SearchRequest{
		TenantID: tenantID, UserID: userID, GroupIDs: groups,
	}
	result, err := h.svc.Suggest(r.Context(), req, q, limit)
	if err != nil {
		h.log.Error().Err(err).Msg("suggest failed")
		writeError(w, http.StatusInternalServerError, "suggest failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// ---- autocomplete ---------------------------------------------------------

func (h *Handler) autocomplete(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
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
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
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
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
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
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
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

// ---- ADR 0085 — saved-search edit + alert + subscribe ----------------

type patchSavedSearchBody struct {
	Name                  *string      `json:"name,omitempty"`
	Query                 *string      `json:"query,omitempty"`
	Filters               *filtersBody `json:"filters,omitempty"`
	Notify                *bool        `json:"notify,omitempty"`
	NotifyIntervalMinutes *int         `json:"notify_interval_minutes,omitempty"`
	AlertFrequencyCron    *string      `json:"alert_frequency_cron,omitempty"`
}

func (h *Handler) patchSavedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "saved search id required")
		return
	}
	var body patchSavedSearchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	patch := repository.SavedSearchPatch{
		Name:                  body.Name,
		Query:                 body.Query,
		Notify:                body.Notify,
		NotifyIntervalMinutes: body.NotifyIntervalMinutes,
		AlertFrequencyCron:    body.AlertFrequencyCron,
	}
	if body.Filters != nil {
		f := model.SearchFilters{
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
		}
		patch.Filters = &f
	}
	updated, err := h.svc.UpdateSavedSearch(r.Context(), tenantID, userID, id, patch)
	if err != nil {
		h.log.Error().Err(err).Msg("patch saved search failed")
		writeError(w, http.StatusNotFound, "saved search not found")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

type subscribeBody struct {
	UserID   string   `json:"user_id"`
	Channels []string `json:"channels"`
}

func (h *Handler) subscribeSavedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	role := auth.RoleString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	id := r.PathValue("id")
	if id == "" {
		writeError(w, http.StatusBadRequest, "saved search id required")
		return
	}
	var body subscribeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	target := body.UserID
	if target == "" {
		// Self-subscribe shorthand — empty body or {channels} only.
		target = userID
	}
	// Adding someone else as a subscriber requires admin role.
	// Self-subscribing is always allowed.
	if target != userID && role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "owner|admin required to subscribe other users")
		return
	}
	if err := h.svc.AddSubscriber(r.Context(), tenantID, id, target, userID, body.Channels); err != nil {
		h.log.Error().Err(err).Msg("add subscriber failed")
		writeError(w, http.StatusInternalServerError, "subscribe failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "subscribed", "user_id": target})
}

func (h *Handler) unsubscribeSavedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	role := auth.RoleString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	id := r.PathValue("id")
	target := r.PathValue("user_id")
	if id == "" || target == "" {
		writeError(w, http.StatusBadRequest, "saved search id and user_id required")
		return
	}
	// A user can always remove themselves; admin can remove anyone.
	if target != userID && role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "owner|admin required to unsubscribe other users")
		return
	}
	if err := h.svc.RemoveSubscriber(r.Context(), tenantID, id, target); err != nil {
		writeError(w, http.StatusNotFound, "subscription not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- ADR 0069 — platform-admin federated search ---------------------

type federatedSearchBody struct {
	Query        string      `json:"query"`
	Filters      filtersBody `json:"filters"`
	Reason       string      `json:"reason"`
	MaxPerTenant int         `json:"max_per_tenant"`
	PageSize     int         `json:"page_size"`
}

func (h *Handler) federatedSearch(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	var body federatedSearchBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	in := service.FederatedSearchInput{
		CallerID: userID,
		Reason:   body.Reason,
		Query:    body.Query,
		Filters: model.SearchFilters{
			WorkspaceID:    body.Filters.WorkspaceID,
			DocumentClass:  body.Filters.DocumentClass,
			LifecycleState: body.Filters.LifecycleState,
			Tags:           body.Filters.Tags,
			MimeType:       body.Filters.MimeType,
			CreatedBy:      body.Filters.CreatedBy,
		},
		MaxPerTenant: body.MaxPerTenant,
		PageSize:     body.PageSize,
	}
	result, err := h.svc.FederatedSearch(r.Context(), in)
	if err != nil {
		// Map the sentinel errors to canonical HTTP codes. Audit
		// rows for denial paths are written by the service before
		// the error returns; the handler's only job here is the
		// status code.
		switch err {
		case service.ErrFederatedReasonTooShort:
			writeError(w, http.StatusBadRequest, "reason must be at least 10 chars — explain why this cross-tenant search is needed")
			return
		case service.ErrFederatedNotPlatformAdmin:
			writeError(w, http.StatusForbidden, "platform.search.federated permission required")
			return
		case service.ErrFederatedQuotaExceeded:
			writeError(w, http.StatusTooManyRequests, "daily federated query limit reached (100/day)")
			return
		}
		h.log.Error().Err(err).Msg("federated search failed")
		writeError(w, http.StatusInternalServerError, "federated search failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (h *Handler) listFederatedAudit(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	// No platform-admin gate on the LIST: anyone can ask for their
	// own audit rows, but the SQL predicate scopes to caller_id so
	// non-admins just see an empty list (their successful queries
	// never landed there).
	limit := 20
	if v, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && v > 0 {
		limit = v
	}
	rows, err := h.svc.ListMyFederatedAudit(r.Context(), userID, limit)
	if err != nil {
		h.log.Error().Err(err).Msg("list federated audit failed")
		writeError(w, http.StatusInternalServerError, "list audit failed")
		return
	}
	if rows == nil {
		rows = []repository.FederatedAuditRecord{}
	}
	writeJSON(w, http.StatusOK, rows)
}

// permissionPropagationStats serves the §7.3 SLI summary the admin
// dashboard polls — p50/p95/p99 of the propagation-lag histogram +
// success/failure counts + the debouncer's pending-queue depth.
//
// Owner|admin gated. Reads in-process Prometheus state, so the
// histogram_quantile() math matches what Grafana would compute
// scraping /metrics.
func (h *Handler) permissionPropagationStats(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	role := auth.RoleString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID required")
		return
	}
	if role != "owner" && role != "admin" {
		writeError(w, http.StatusForbidden, "owner|admin required")
		return
	}
	if h.debouncer == nil {
		// Service started without the debouncer (e.g. legacy deploy).
		// Report zeros rather than 5xx — the page renders an empty
		// state.
		writeJSON(w, http.StatusOK, service.PropagationStats{})
		return
	}
	writeJSON(w, http.StatusOK, h.debouncer.CollectPropagationStats())
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
