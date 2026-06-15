// Pre-commit ingestion REST surface (Workstream 3 — "fix OCR ordering").
//
//	POST /api/v1/ingest
//	  Auth: SeDoc session OR Bearer API key (documents:write scope).
//	  Wrapped with the Idempotency-Key layer in main.go.
//	  Stages a completed blob (already uploaded via the storage
//	  initiate/PUT/complete flow) WITHOUT creating a document/version, and
//	  emits dms.ingestion.received.v1 so the intelligence worker OCRs +
//	  extracts a business key against the blob ref.
//	  Body: { workspace_id, folder_id, target_customer_ref?, blob_checksum,
//	          blob_ref? | content_blob_id?, mime?, size?, region_pin?,
//	          document_class? }
//	  201: { ingestion_item_id, status }
//
//	GET  /api/v1/ingest/items?status=&limit=  → staged items
//
// The human review/triage queue (Workstream 4) is a separate top-level
// resource — see review_queue_handler.go (/api/v1/review-queue).
package handler

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// IngestionHandler owns the /api/v1/ingest routes.
type IngestionHandler struct {
	svc *service.DocumentService
}

// NewIngestionHandler constructs the handler.
func NewIngestionHandler(svc *service.DocumentService) *IngestionHandler {
	return &IngestionHandler{svc: svc}
}

// Register mounts the ingestion routes on the supplied mux. The review-queue
// surface lives in review_queue_handler.go (top-level /api/v1/review-queue, WS4).
func (h *IngestionHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/ingest", h.ingest)
	mux.HandleFunc("GET /api/v1/ingest/items", h.listItems)
}

// ---- wire types ------------------------------------------------------------

type ingestReq struct {
	WorkspaceID       string `json:"workspace_id"`
	FolderID          string `json:"folder_id"`
	TargetCustomerRef string `json:"target_customer_ref"`
	BlobChecksum      string `json:"blob_checksum"`
	BlobRef           string `json:"blob_ref"`
	ContentBlobID     string `json:"content_blob_id"` // alias for blob_ref
	Mime              string `json:"mime"`
	Size              int64  `json:"size"`
	RegionPin         string `json:"region_pin"`
	DocumentClass     string `json:"document_class"`
}

type ingestResp struct {
	IngestionItemID string `json:"ingestion_item_id"`
	Status          string `json:"status"`
}

type ingestionItemResp struct {
	ID                   string  `json:"id"`
	WorkspaceID          string  `json:"workspace_id"`
	TargetCustomerRef    string  `json:"target_customer_ref"`
	Status               string  `json:"status"`
	ExtractedExternalKey string  `json:"extracted_external_key"`
	MatchDocumentID      string  `json:"match_document_id,omitempty"`
	Confidence           float64 `json:"confidence"`
	DocumentClass        string  `json:"document_class"`
	CreatedAt            string  `json:"created_at"`
}

// ---- handlers --------------------------------------------------------------

func (h *IngestionHandler) ingest(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var body ingestReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	wsID, err := uuid.Parse(strings.TrimSpace(body.WorkspaceID))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "required, must be a uuid"))
		return
	}
	folderID, err := uuid.Parse(strings.TrimSpace(body.FolderID))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("folder_id", "required, must be a uuid"))
		return
	}
	blobRef := strings.TrimSpace(body.BlobRef)
	if blobRef == "" {
		blobRef = strings.TrimSpace(body.ContentBlobID)
	}
	item, err := h.svc.CreateIngestionItem(r.Context(), &service.CreateIngestionInput{
		WorkspaceID:       wsID,
		FolderID:          folderID,
		TargetCustomerRef: body.TargetCustomerRef,
		BlobChecksum:      body.BlobChecksum,
		BlobRef:           blobRef,
		Mime:              body.Mime,
		Size:              body.Size,
		RegionPin:         body.RegionPin,
		DocumentClass:     body.DocumentClass,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, ingestResp{
		IngestionItemID: item.ID.String(),
		Status:          string(item.Status),
	})
}

func (h *IngestionHandler) listItems(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	items, err := h.svc.ListIngestionItems(r.Context(),
		strings.TrimSpace(r.URL.Query().Get("status")), queryLimit(r))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]ingestionItemResp, 0, len(items))
	for i := range items {
		out = append(out, toIngestionItemResp(&items[i]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// ---- mappers ---------------------------------------------------------------

func toIngestionItemResp(it *model.IngestionItem) ingestionItemResp {
	r := ingestionItemResp{
		ID:                   it.ID.String(),
		WorkspaceID:          it.WorkspaceID.String(),
		TargetCustomerRef:    it.TargetCustomerRef,
		Status:               string(it.Status),
		ExtractedExternalKey: it.ExtractedExternalKey,
		Confidence:           it.Confidence,
		DocumentClass:        it.DocumentClass,
		CreatedAt:            it.CreatedAt.UTC().Format("2006-01-02T15:04:05Z07:00"),
	}
	if it.MatchDocumentID != nil {
		r.MatchDocumentID = it.MatchDocumentID.String()
	}
	return r
}

func queryLimit(r *http.Request) int {
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
