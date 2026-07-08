// Audit-trail viz handler — ADR 0103 §18 F10.
//
// GET /api/v1/audit/documents/{document_id}/viz?bucket=hour|day&since=…
//
// Returns the aggregation JSON straight from Postgres (the repo does
// SELECT json_build_object(...) so we don't re-marshal). Tenant scope
// comes from the X-Auth-Tenant-ID header that the upstream gateway
// stamps. We don't enforce that the caller can read the document —
// the audit table itself is tenant-scoped, and exposing aggregate
// counts of OWN-tenant audit events doesn't leak more than the
// existing /audit/events endpoint already does (same auth model).
package handler

import (
	"errors"
	"net/http"
	"time"
)

func (h *Handler) documentAuditViz(w http.ResponseWriter, r *http.Request) {
	// Reading a document's audit aggregate exposes the same trail data
	// as /audit/events (actor names, action counts), so it takes the
	// same admin gate (issue #74).
	tenantID, ok := h.requireAuditAdmin(w, r, "read")
	if !ok {
		return
	}
	docID := r.PathValue("document_id")
	if docID == "" {
		writeError(w, http.StatusBadRequest, "document_id required")
		return
	}
	bucket := r.URL.Query().Get("bucket")
	if bucket == "" {
		bucket = "day"
	}
	if bucket != "hour" && bucket != "day" {
		writeError(w, http.StatusBadRequest, "bucket must be 'hour' or 'day'")
		return
	}
	// `since` defaults to 90 days ago — enough history for typical
	// audit-review use cases without scanning forever. Override via
	// ?since=2026-01-01T00:00:00Z.
	since := time.Now().Add(-90 * 24 * time.Hour)
	if s := r.URL.Query().Get("since"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be RFC3339")
			return
		}
		since = t
	}

	body, err := h.svc.Viz(r.Context(), tenantID, docID, bucket, since)
	if err != nil {
		// "bucket must be" comes from the repo as a sentinel — surface
		// as 400 rather than the generic 500 path.
		if errors.Is(err, errBadBucket) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		h.log.Error().Err(err).Str("doc", docID).Msg("audit viz failed")
		writeError(w, http.StatusInternalServerError, "viz aggregation failed")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if len(body) == 0 {
		// no events for this doc — return a normalised empty shape
		// rather than `null` so the FE doesn't need to guard.
		w.Write([]byte(`{"time_buckets":[],"actors":[],"actions":[],"sankey_edges":[],"heatmap":[],"total_events":0}`))
		return
	}
	w.Write(body)
}

// Sentinel for the repo's "bad bucket" error. The repo doesn't import
// this package so it returns a plain errors.New; we recognise it by
// message-prefix. Cheap and contained.
var errBadBucket = errors.New("bucket must be 'hour' or 'day'")
