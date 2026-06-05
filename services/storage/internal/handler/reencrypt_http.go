package handler

import (
	"encoding/json"
	"net/http"
	"os"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/services/storage/internal/service"
)

// ReencryptHTTPHandler exposes the admin-only blob re-encrypt primitive
// (service.ReencryptBlob) over the storage service's health port so
// `dms-admin kms rewrap*` can drive a bulk migration without re-implementing
// the KMS + S3 + pool wiring the service already owns.
//
// Internal-only. Gated by the shared SEDOC_INTERNAL_API_KEY: the gateway
// strips any client-supplied X-Internal-Service-Key on inbound, and the health
// port is not part of the public surface. It refuses outright when the key env
// is unset (fail-closed — never accept an empty key).
type ReencryptHTTPHandler struct {
	svc *service.Service
}

// NewReencryptHTTPHandler builds the handler around the storage Service.
func NewReencryptHTTPHandler(svc *service.Service) *ReencryptHTTPHandler {
	return &ReencryptHTTPHandler{svc: svc}
}

type reencryptReq struct {
	TenantID     string `json:"tenant_id"`
	BlobID       string `json:"blob_id"`
	TargetRegion string `json:"target_region"`
	TargetTier   string `json:"target_tier,omitempty"`
}

func (h *ReencryptHTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	want := os.Getenv("SEDOC_INTERNAL_API_KEY")
	if want == "" || r.Header.Get("X-Internal-Service-Key") != want {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	var req reencryptReq
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

	res, err := h.svc.ReencryptBlob(r.Context(), service.ReencryptBlobInput{
		TenantID:     tenantID,
		BlobID:       blobID,
		TargetRegion: req.TargetRegion,
		TargetTier:   req.TargetTier,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"blob_id":         res.BlobID.String(),
		"source_region":   res.SourceRegion,
		"target_region":   res.TargetRegion,
		"new_kek_alias":   res.NewKEKAlias,
		"bytes_rewrapped": res.BytesRewrapped,
	})
}
