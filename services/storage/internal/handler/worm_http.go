package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/storage/internal/service"
)

// WORMHTTPHandler exposes the admin-triggered object-lock primitive
// (service.ApplyWORM) on the storage service's internal port. The document
// service's /api/v1/documents/{id}/worm-lock admin endpoint calls it after
// resolving the document's current blob. Internal-only, gated by the shared
// SEDOC_INTERNAL_API_KEY (fail-closed when unset).
type WORMHTTPHandler struct {
	svc *service.Service
}

func NewWORMHTTPHandler(svc *service.Service) *WORMHTTPHandler {
	return &WORMHTTPHandler{svc: svc}
}

type wormReq struct {
	TenantID    string `json:"tenant_id"`
	BlobID      string `json:"blob_id"`
	RetainUntil string `json:"retain_until"` // RFC3339
	Mode        string `json:"mode,omitempty"`
}

func (h *WORMHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !internalServiceKeyOK(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req wormReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	tenantID, err := uuid.Parse(req.TenantID)
	if err != nil {
		http.Error(w, "tenant_id not a uuid", http.StatusBadRequest)
		return
	}
	blobID, err := uuid.Parse(req.BlobID)
	if err != nil {
		http.Error(w, "blob_id not a uuid", http.StatusBadRequest)
		return
	}
	retainUntil, err := time.Parse(time.RFC3339, req.RetainUntil)
	if err != nil {
		http.Error(w, "retain_until not RFC3339", http.StatusBadRequest)
		return
	}

	res, err := h.svc.ApplyWORM(r.Context(), service.ApplyWORMInput{
		TenantID:    tenantID,
		BlobID:      blobID,
		RetainUntil: retainUntil,
		Mode:        req.Mode,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(res)
}
