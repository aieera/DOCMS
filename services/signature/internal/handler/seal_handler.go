package handler

import (
	"encoding/json"
	"net/http"
)

// RegisterSeal mounts the internal server-seal endpoint (ADR 0025). main.go
// gates it with the internal-service key so only trusted services (the
// workflow seal trigger, ops tooling) can invoke an organizational seal.
//
//	POST /api/v1/signatures/internal/seal
//	  { "document_id", "version_id", "signer_name"?, "reason"? }
//	  → { "new_version_id", "level", "fingerprint" }
func (h *Handler) RegisterSeal(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/signatures/internal/seal", h.sealVersion)
}

func (h *Handler) sealVersion(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant")
		return
	}
	var body struct {
		DocumentID string `json:"document_id"`
		VersionID  string `json:"version_id"`
		SignerName string `json:"signer_name,omitempty"`
		Reason     string `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.DocumentID == "" || body.VersionID == "" {
		writeError(w, http.StatusBadRequest, "document_id and version_id are required")
		return
	}
	res, err := h.svc.SealVersion(r.Context(), tenantID, body.DocumentID, body.VersionID, body.SignerName, body.Reason)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
