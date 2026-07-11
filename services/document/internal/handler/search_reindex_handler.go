// Internal search-reconcile trigger.
//
//	POST /internal/v1/search/reindex          → whole tenant
//	POST /internal/v1/search/reindex {"document_id": "..."} → one doc
//
// Rebuilds the OpenSearch projection from source of truth via
// dms.document.reindexed.v1 outbox events (service.ReindexSearch). Run
// once per tenant after deploying the indexer partial-update fix to
// repair docs whose readable_by/content were wiped by the old
// full-replace-on-update path; keep around for future projection drift.
// Same exposure model as /internal/v1/records/cutoff-sweep: mounted on
// the internal mux (never routed by the gateway), caller supplies
// tenant identity via the internal auth headers.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// SearchReindexHandler mounts the internal reindex route.
type SearchReindexHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewSearchReindexHandler constructs the handler.
func NewSearchReindexHandler(svc *service.DocumentService, log zerolog.Logger) *SearchReindexHandler {
	return &SearchReindexHandler{svc: svc, log: log}
}

// RegisterInternal mounts the route on the internal mux.
func (h *SearchReindexHandler) RegisterInternal(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/search/reindex", h.reindex)
}

func (h *SearchReindexHandler) reindex(w http.ResponseWriter, r *http.Request) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var body struct {
		DocumentID string `json:"document_id"`
	}
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, r, vdmserr.Validation("body", "invalid json"))
			return
		}
	}
	var docID *uuid.UUID
	if body.DocumentID != "" {
		id, err := uuid.Parse(body.DocumentID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("document_id", "invalid uuid"))
			return
		}
		docID = &id
	}
	n, err := h.svc.ReindexSearch(r.Context(), docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	h.log.Info().Str("tenant_id", tenantID.String()).Int("reindexed", n).
		Msg("search reindex emitted")
	writeJSONStatus(w, http.StatusOK, map[string]any{"reindexed": n})
}
