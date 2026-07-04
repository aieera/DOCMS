// Scan-capture HTTP surface.
//
//	POST /api/v1/connectors/capture/analyze  — bundle → split proposal (preview)
//	POST /api/v1/connectors/capture/commit   — corrected grouping → documents
//
// SessionAuth populates tenant/user; the user's session token rides through to
// the ingest pipeline so each segment is created under the caller's identity
// (RLS + folder-write permission apply, same as the Drive import).
package handler

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/connector/internal/intake"
)

type CaptureHandler struct{ svc *intake.CaptureService }

func NewCaptureHandler(svc *intake.CaptureService) *CaptureHandler { return &CaptureHandler{svc: svc} }

func (h *CaptureHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/connectors/capture/analyze", h.analyze)
	mux.HandleFunc("POST /api/v1/connectors/capture/commit", h.commit)
}

// analyze proxies the bundle to intelligence and streams the proposal back.
func (h *CaptureHandler) analyze(w http.ResponseWriter, r *http.Request) {
	if auth.TenantIDString(r) == "" {
		writeError(w, http.StatusBadRequest, "tenant required")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<20)) // 64 MiB bundle cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	raw, err := h.svc.Analyze(r.Context(), body)
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(raw)
}

type captureCommitBody struct {
	BundleB64   string                      `json:"bundle_b64"`
	Mime        string                      `json:"mime"`
	PageGroups  [][]int                     `json:"page_groups"`
	WorkspaceID string                      `json:"workspace_id"`
	FolderID    string                      `json:"folder_id"`
	Segments    []intake.CaptureSegmentMeta `json:"segments"`
}

func (h *CaptureHandler) commit(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "tenant and user required")
		return
	}
	var b captureCommitBody
	if err := json.NewDecoder(io.LimitReader(r.Body, 128<<20)).Decode(&b); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if b.WorkspaceID == "" || b.FolderID == "" {
		writeError(w, http.StatusBadRequest, "workspace_id and folder_id are required")
		return
	}
	if len(b.PageGroups) == 0 {
		writeError(w, http.StatusBadRequest, "page_groups required")
		return
	}
	if b.Mime == "" {
		b.Mime = "application/pdf"
	}
	docIDs, err := h.svc.Commit(r.Context(), intake.CaptureCommitInput{
		TenantID:    tenantID,
		ActorID:     userID,
		AuthToken:   sessionToken(r),
		WorkspaceID: b.WorkspaceID,
		FolderID:    b.FolderID,
		BundleB64:   b.BundleB64,
		Mime:        b.Mime,
		PageGroups:  b.PageGroups,
		Segments:    b.Segments,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(map[string]any{"document_ids": docIDs, "count": len(docIDs)})
}
