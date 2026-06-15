// External-key REST surface (Workstream "Stable external key + upsert-by-
// external-key"). Lets the ERP create-or-version + resolve documents by the
// business key it owns (e.g. "INV-2024-00188") instead of SeDoc's UUID.
//
//	POST /api/v1/workspaces/{workspace_id}/documents:upsert
//	  Auth: SeDoc session OR Bearer API key (documents:write scope).
//	  Wrapped with the Idempotency-Key layer in main.go.
//	  Body: { external_id, folder_id, title?, doc_type?, document_class?,
//	          tags?, custom_metadata?, region_pin?, change_summary?,
//	          version: { blob_checksum, blob_ref?, mime?, size? } }
//	  201 (created) | 200 (versioned / no-op):
//	          { document_id, current_version_id, current_version_number,
//	            created, version_created }
//
//	GET /api/v1/documents:byExternalKey?external_id=...
//	  Auth: same. Permission-filtered (404 when unknown or not viewable).
//	  200: { document_id, current_version_id }
//
// Implemented as a plain HTTP handler (not a gRPC-gateway RPC) for the same
// reason the bulk + m365 ingest surfaces are: the request carries a nested,
// ERP-shaped body and the route uses an AIP custom verb (":upsert").
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// ExternalKeyHandler owns the documents:upsert + documents:byExternalKey routes.
type ExternalKeyHandler struct {
	svc *service.DocumentService
}

// NewExternalKeyHandler constructs the handler.
func NewExternalKeyHandler(svc *service.DocumentService) *ExternalKeyHandler {
	return &ExternalKeyHandler{svc: svc}
}

// Register mounts both routes on the supplied mux.
func (h *ExternalKeyHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/documents:upsert", h.upsert)
	mux.HandleFunc("GET /api/v1/documents:byExternalKey", h.byExternalKey)
}

// ---- wire types ----------------------------------------------------

type upsertVersionReq struct {
	BlobChecksum string `json:"blob_checksum"`
	BlobRef      string `json:"blob_ref"`
	Mime         string `json:"mime"`
	Size         int64  `json:"size"`
}

type upsertDocReq struct {
	ExternalID     string           `json:"external_id"`
	FolderID       string           `json:"folder_id"`
	Title          string           `json:"title"`
	DocType        string           `json:"doc_type"`
	DocumentClass  string           `json:"document_class"`
	Tags           []string         `json:"tags"`
	CustomMetadata map[string]any   `json:"custom_metadata"`
	RegionPin      string           `json:"region_pin"`
	ChangeSummary  string           `json:"change_summary"`
	Version        upsertVersionReq `json:"version"`
}

type upsertResp struct {
	DocumentID           string `json:"document_id"`
	CurrentVersionID     string `json:"current_version_id"`
	CurrentVersionNumber int    `json:"current_version_number"`
	Created              bool   `json:"created"`
	VersionCreated       bool   `json:"version_created"`
}

type byExternalKeyResp struct {
	DocumentID       string `json:"document_id"`
	CurrentVersionID string `json:"current_version_id"`
}

// ---- handlers ------------------------------------------------------

func (h *ExternalKeyHandler) upsert(w http.ResponseWriter, r *http.Request) {
	_, userID, ok := callers(w, r)
	if !ok {
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}
	var body upsertDocReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	folderID, err := uuid.Parse(strings.TrimSpace(body.FolderID))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("folder_id", "required, must be a uuid"))
		return
	}
	res, err := h.svc.UpsertDocumentByExternalKey(r.Context(), &service.UpsertByExternalKeyInput{
		ExternalID:     body.ExternalID,
		WorkspaceID:    wsID,
		FolderID:       folderID,
		Title:          body.Title,
		DocumentClass:  body.DocumentClass,
		DocType:        body.DocType,
		Tags:           body.Tags,
		CustomMetadata: body.CustomMetadata,
		RegionPin:      body.RegionPin,
		BlobChecksum:   body.Version.BlobChecksum,
		BlobRef:        body.Version.BlobRef,
		Mime:           body.Version.Mime,
		Size:           body.Version.Size,
		ChangeSummary:  body.ChangeSummary,
		UpdatedBy:      userID,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	writeJSON(w, status, upsertResp{
		DocumentID:           res.DocumentID.String(),
		CurrentVersionID:     res.CurrentVersionID.String(),
		CurrentVersionNumber: res.CurrentVersionNumber,
		Created:              res.Created,
		VersionCreated:       res.VersionCreated,
	})
}

func (h *ExternalKeyHandler) byExternalKey(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	ext := strings.TrimSpace(r.URL.Query().Get("external_id"))
	if ext == "" {
		writeErr(w, r, vdmserr.Validation("external_id", "required"))
		return
	}
	docID, curVer, err := h.svc.GetDocumentByExternalKey(r.Context(), ext)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := byExternalKeyResp{DocumentID: docID.String()}
	if curVer != uuid.Nil {
		resp.CurrentVersionID = curVer.String()
	}
	writeJSON(w, http.StatusOK, resp)
}
