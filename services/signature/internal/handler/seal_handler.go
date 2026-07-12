package handler

import (
	"encoding/json"
	"github.com/aieera/sedoc/pkg/auth"
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
	mux.HandleFunc("POST /api/v1/signatures/internal/seal-ceremony", h.sealCeremony)
}

// sealCeremony is the workflow-owned ceremony seal (ADR 0025 Wave 9): the
// Temporal SignatureWorkflow's SealCeremony activity POSTs here after all
// signers approve. It applies per-signer PAdES-B-LT revisions + a final org
// seal via the DSS sidecar (RegionPin enforced inside), claim-guarded so it's
// idempotent with the dms.signature.completed.v1 fallback consumer.
//
//	POST /api/v1/signatures/internal/seal-ceremony
//	  { "document_id", "version_id", "request_id", "initiated_by"? }
//	  → { "new_version_id", "level", "fingerprint" }  (fresh seal)
//	  → { "already_sealed": true }                    (another trigger won)
func (h *Handler) sealCeremony(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant")
		return
	}
	userID := auth.UserIDString(r)
	var body struct {
		DocumentID  string `json:"document_id"`
		VersionID   string `json:"version_id"`
		RequestID   string `json:"request_id"`
		InitiatedBy string `json:"initiated_by,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.DocumentID == "" || body.VersionID == "" {
		writeError(w, http.StatusBadRequest, "document_id and version_id are required")
		return
	}
	initiatedBy := body.InitiatedBy
	if initiatedBy == "" {
		initiatedBy = userID
	}
	res, alreadySealed, err := h.svc.SealCeremonyForRequest(r.Context(), tenantID, body.DocumentID, body.VersionID, initiatedBy, body.RequestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if alreadySealed {
		writeJSON(w, http.StatusOK, map[string]any{"already_sealed": true})
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handler) sealVersion(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	if tenantID == "" {
		writeError(w, http.StatusUnauthorized, "missing tenant")
		return
	}
	// The sealing actor (the workflow initiator / ops user). Storage requires a
	// non-nil user_id; the OPA admin rule still authorizes via the role on ctx.
	userID := auth.UserIDString(r)
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
	res, err := h.svc.SealVersion(r.Context(), tenantID, body.DocumentID, body.VersionID, userID, body.SignerName, body.Reason)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
