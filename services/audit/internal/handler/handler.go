// Package handler exposes REST endpoints for the audit service.
package handler

import (
	"encoding/csv"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/audit/internal/model"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

// Handler holds HTTP route handlers.
type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

// New constructs a Handler.
func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

// Register mounts routes.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/audit/events", h.listEvents)
	mux.HandleFunc("GET /api/v1/audit/export", h.exportCSV)
	// SIEM pull feed: newline-delimited JSON, full per-event fields
	// (hash chain included), streamed across the whole filtered set
	// with no row cap. A SIEM collector polls this on a schedule with a
	// date_from watermark.
	mux.HandleFunc("GET /api/v1/audit/export/ndjson", h.exportNDJSON)
	mux.HandleFunc("POST /api/v1/audit/verify-integrity", h.verifyIntegrity)
	mux.HandleFunc("GET /api/v1/audit/merkle-proof", h.merkleProof)
	// Ed25519 signed checkpoints — third-party-verifiable tamper-evidence.
	mux.HandleFunc("POST /api/v1/audit/checkpoint", h.createCheckpoint)
	mux.HandleFunc("GET /api/v1/audit/checkpoints", h.listCheckpoints)
	mux.HandleFunc("GET /api/v1/audit/signing-key", h.signingKey)
	mux.HandleFunc("POST /api/v1/audit/data-subject/export", h.dataSubjectExport)
	mux.HandleFunc("POST /api/v1/audit/data-subject/anonymize", h.dataSubjectAnonymize)
	// ADR 0103 — audit-trail visualization aggregation.
	mux.HandleFunc("GET /api/v1/audit/documents/{document_id}/viz", h.documentAuditViz)
}

// parseExportFilter builds a ListFilter from the request's filter query
// params (no pagination — the streamer drives that). Shared by the CSV
// and NDJSON export paths and mirrors listEvents' parsing.
func parseExportFilter(r *http.Request, tenantID string) model.ListFilter {
	f := model.ListFilter{
		TenantID:     tenantID,
		Actor:        r.URL.Query().Get("actor"),
		Action:       r.URL.Query().Get("action"),
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateFrom = &t
		}
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateTo = &t
		}
	}
	return f
}

// streamEvents walks every page of the filtered result set (keyset
// pagination via the service List) and calls fn per event. This lets the
// export endpoints stream the entire history without the old 10k cap or
// holding it all in memory. flush, when non-nil, is invoked per page so
// the client sees bytes promptly.
func (h *Handler) streamEvents(r *http.Request, base model.ListFilter, fn func(*model.AuditEvent) error, flush func()) error {
	base.PageSize = 100
	for {
		events, next, err := h.svc.List(r.Context(), base)
		if err != nil {
			return err
		}
		for _, e := range events {
			if err := fn(e); err != nil {
				return err
			}
		}
		if flush != nil {
			flush()
		}
		if next == "" {
			return nil
		}
		base.PageToken = next
	}
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	f := model.ListFilter{
		TenantID:     tenantID,
		Actor:        r.URL.Query().Get("actor"),
		Action:       r.URL.Query().Get("action"),
		ResourceType: r.URL.Query().Get("resource_type"),
		ResourceID:   r.URL.Query().Get("resource_id"),
		PageToken:    r.URL.Query().Get("page_token"),
	}
	if ps, err := strconv.Atoi(r.URL.Query().Get("page_size")); err == nil {
		f.PageSize = ps
	}
	if v := r.URL.Query().Get("date_from"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateFrom = &t
		}
	}
	if v := r.URL.Query().Get("date_to"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			f.DateTo = &t
		}
	}
	events, nextToken, err := h.svc.List(r.Context(), f)
	if err != nil {
		h.log.Error().Err(err).Msg("list events")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"events":     events,
		"page_token": nextToken,
	})
}

func (h *Handler) exportCSV(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", "attachment; filename=audit_log.csv")
	cw := csv.NewWriter(w)
	flusher, _ := w.(http.Flusher)
	// Enriched columns: actor_name, resource_title, user_agent, and the
	// hash-chain fields were previously dropped, so a CSV export couldn't
	// be independently re-verified or attributed to a device.
	_ = cw.Write([]string{
		"id", "timestamp", "actor", "actor_name", "action",
		"resource_type", "resource_id", "resource_title",
		"ip_address", "user_agent", "previous_hash", "event_hash", "source_event",
	})
	err := h.streamEvents(r, parseExportFilter(r, tenantID), func(e *model.AuditEvent) error {
		return cw.Write([]string{
			e.ID, e.CreatedAt.Format(time.RFC3339), e.Actor, e.ActorName, e.Action,
			e.ResourceType, e.ResourceID, e.ResourceTitle,
			e.IPAddress, e.UserAgent, e.PreviousHash, e.EventHash, e.SourceEvent,
		})
	}, func() {
		cw.Flush()
		if flusher != nil {
			flusher.Flush()
		}
	})
	if err != nil {
		// Headers/rows may already be on the wire; we can't switch to a
		// JSON error now, so just log and stop. The truncated file is the
		// failure signal.
		h.log.Error().Err(err).Msg("csv export stream failed")
	}
	cw.Flush()
}

// ndjsonEvent mirrors AuditEvent but emits Details as raw nested JSON
// (the model stores it as []byte, which would base64-encode) so a SIEM
// ingests the structured payload directly.
type ndjsonEvent struct {
	ID            string          `json:"id"`
	TenantID      string          `json:"tenant_id"`
	EventHash     string          `json:"event_hash"`
	PreviousHash  string          `json:"previous_hash"`
	Actor         string          `json:"actor"`
	ActorName     string          `json:"actor_name,omitempty"`
	Action        string          `json:"action"`
	ResourceType  string          `json:"resource_type"`
	ResourceID    string          `json:"resource_id"`
	ResourceTitle string          `json:"resource_title,omitempty"`
	Details       json.RawMessage `json:"details,omitempty"`
	IPAddress     string          `json:"ip_address,omitempty"`
	UserAgent     string          `json:"user_agent,omitempty"`
	SourceEvent   string          `json:"source_event,omitempty"`
	CreatedAt     time.Time       `json:"created_at"`
}

func (h *Handler) exportNDJSON(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", "attachment; filename=audit_log.ndjson")
	enc := json.NewEncoder(w)
	flusher, _ := w.(http.Flusher)
	err := h.streamEvents(r, parseExportFilter(r, tenantID), func(e *model.AuditEvent) error {
		out := ndjsonEvent{
			ID: e.ID, TenantID: e.TenantID, EventHash: e.EventHash, PreviousHash: e.PreviousHash,
			Actor: e.Actor, ActorName: e.ActorName, Action: e.Action,
			ResourceType: e.ResourceType, ResourceID: e.ResourceID, ResourceTitle: e.ResourceTitle,
			IPAddress: e.IPAddress, UserAgent: e.UserAgent, SourceEvent: e.SourceEvent, CreatedAt: e.CreatedAt,
		}
		// Details holds JSON bytes; pass through as raw so it nests
		// rather than base64-encoding. Guard against non-JSON / empty.
		if json.Valid(e.Details) {
			out.Details = json.RawMessage(e.Details)
		}
		// enc.Encode writes the object followed by a newline → NDJSON.
		return enc.Encode(out)
	}, func() {
		if flusher != nil {
			flusher.Flush()
		}
	})
	if err != nil {
		h.log.Error().Err(err).Msg("ndjson export stream failed")
	}
}

func (h *Handler) verifyIntegrity(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	result, err := h.svc.VerifyIntegrity(r.Context(), tenantID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "verification failed")
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// merkleProof returns a range-scoped Merkle/hash-chain proof over one
// resource's audit events (e.g. a document's trail). The frontend's per-
// document "Verify integrity" action calls this.
//
//	GET /api/v1/audit/merkle-proof?resource_type=document&resource_id={id}
func (h *Handler) merkleProof(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	resourceID := r.URL.Query().Get("resource_id")
	if resourceID == "" {
		writeError(w, http.StatusBadRequest, "resource_id is required")
		return
	}
	resourceType := r.URL.Query().Get("resource_type")
	proof, err := h.svc.MerkleProofForResource(r.Context(), tenantID, resourceType, resourceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "proof generation failed")
		return
	}
	writeJSON(w, http.StatusOK, proof)
}

// signingKey publishes the public signing key so external parties can
// verify checkpoints independently. No tenant required — the key is
// platform-wide.
func (h *Handler) signingKey(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, h.svc.SigningKey())
}

func (h *Handler) createCheckpoint(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	cp, err := h.svc.CreateCheckpoint(r.Context(), tenantID)
	if err != nil {
		if err == service.ErrSigningDisabled {
			writeError(w, http.StatusNotImplemented, err.Error())
			return
		}
		h.log.Error().Err(err).Msg("create checkpoint")
		writeError(w, http.StatusInternalServerError, "checkpoint failed: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, cp)
}

func (h *Handler) listCheckpoints(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusBadRequest, "unauthenticated: no tenant on session")
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	cps, err := h.svc.ListCheckpoints(r.Context(), tenantID, limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list checkpoints failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"checkpoints": cps})
}

type subjectBody struct {
	SubjectID string `json:"subject_id"`
}

func (h *Handler) dataSubjectExport(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var body subjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SubjectID == "" || tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and subject_id required")
		return
	}
	export, err := h.svc.ExportSubject(r.Context(), tenantID, body.SubjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "export failed")
		return
	}
	writeJSON(w, http.StatusOK, export)
}

func (h *Handler) dataSubjectAnonymize(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	var body subjectBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.SubjectID == "" || tenantID == "" {
		writeError(w, http.StatusBadRequest, "tenant_id and subject_id required")
		return
	}
	count, err := h.svc.AnonymizeSubject(r.Context(), tenantID, body.SubjectID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "anonymize failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"anonymized_count": count})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
