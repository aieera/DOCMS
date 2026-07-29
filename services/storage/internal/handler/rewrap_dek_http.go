package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/storage/internal/service"
)

// RewrapDEKHTTPHandler exposes the in-place DEK re-wrap primitive
// (service.RewrapDEK) over the storage health port so `dms-admin kms
// rewrap` can re-wrap a tenant's blobs under the live KEK version after a
// `kms rotate` — without moving bytes or re-implementing the KMS wiring.
//
// Same internal-only gate as ReencryptHTTPHandler: SEDOC_INTERNAL_API_KEY,
// fail-closed when unset; the gateway strips client-supplied
// X-Internal-Service-Key and the health port isn't public.
type RewrapDEKHTTPHandler struct {
	svc *service.Service
}

// NewRewrapDEKHTTPHandler builds the handler around the storage Service.
func NewRewrapDEKHTTPHandler(svc *service.Service) *RewrapDEKHTTPHandler {
	return &RewrapDEKHTTPHandler{svc: svc}
}

type rewrapDEKReq struct {
	TenantID string `json:"tenant_id"`
	BlobID   string `json:"blob_id"`
}

func (h *RewrapDEKHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if !internalServiceKeyOK(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req rewrapDEKReq
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

	res, err := h.svc.RewrapDEK(r.Context(), service.RewrapDEKInput{
		TenantID: tenantID,
		BlobID:   blobID,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"blob_id":       res.BlobID.String(),
		"old_kek_alias": res.OldKEKAlias,
		"new_kek_alias": res.NewKEKAlias,
		"changed":       res.Changed,
	})
}
