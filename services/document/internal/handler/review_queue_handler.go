// Native review/triage queue REST surface (Workstream 4). The human-in-the-loop
// queue for "new version of X vs. new document?" decisions on low-confidence
// ingestion reads. Permission-gated via the SessionOrAPIKey + TenantHTTP chain
// in main.go; the resolve commit paths additionally enforce OPA edit checks
// (CreateVersion / CreateDocument run as the reviewer).
//
//	GET  /api/v1/review-queue?status=pending&limit=&cursor=
//	  Keyset-paginated (created_at,id DESC). 200:
//	    { items: [ {id, ingestion_item_id, reason, suggested_match_document_id,
//	                confidence, status, ...} ], next_cursor }
//
//	GET  /api/v1/review-queue/{id}     → one item + its OCR text
//
//	POST /api/v1/review-queue/{id}/resolve
//	  Body: { decision: new_version|new_document|reject, target_document_id?,
//	          external_key?, notes? }
//	  On resolve: executes the chosen commit (Workstream-1 upsert / new doc /
//	  new version), updates the ingestion_item, emits dms.review.resolved.v1.
package handler

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// ReviewQueueHandler owns the /api/v1/review-queue routes.
type ReviewQueueHandler struct {
	svc *service.DocumentService
}

// NewReviewQueueHandler constructs the handler.
func NewReviewQueueHandler(svc *service.DocumentService) *ReviewQueueHandler {
	return &ReviewQueueHandler{svc: svc}
}

// Register mounts the review-queue routes on the supplied mux.
func (h *ReviewQueueHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/review-queue", h.list)
	mux.HandleFunc("GET /api/v1/review-queue/{id}", h.get)
	mux.HandleFunc("POST /api/v1/review-queue/{id}/resolve", h.resolve)
}

// ---- wire types ------------------------------------------------------------

type reviewItemResp struct {
	ID                       string  `json:"id"`
	IngestionItemID          string  `json:"ingestion_item_id"`
	WorkspaceID              string  `json:"workspace_id"`
	TargetCustomerRef        string  `json:"target_customer_ref"`
	DocumentClass            string  `json:"document_class"`
	ExtractedExternalKey     string  `json:"extracted_external_key"`
	SuggestedMatchDocumentID string  `json:"suggested_match_document_id,omitempty"`
	Confidence               float64 `json:"confidence"`
	Reason                   string  `json:"reason"`
	Status                   string  `json:"status"`
	OCRText                  string  `json:"ocr_text,omitempty"`
	CreatedAt                string  `json:"created_at"`
}

type reviewListResp struct {
	Items      []reviewItemResp `json:"items"`
	NextCursor string           `json:"next_cursor,omitempty"`
}

type resolveReq struct {
	Decision         string `json:"decision"`
	TargetDocumentID string `json:"target_document_id"`
	ExternalKey      string `json:"external_key"`
	Notes            string `json:"notes"`
}

type resolveResp struct {
	Status     string `json:"status"`
	DocumentID string `json:"document_id,omitempty"`
	VersionID  string `json:"version_id,omitempty"`
}

// ---- handlers --------------------------------------------------------------

func (h *ReviewQueueHandler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = string(model.ReviewPending)
	}
	limit := 50
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	cursorTime, cursorID, cerr := decodeReviewCursor(r.URL.Query().Get("cursor"))
	if cerr != nil {
		writeErr(w, r, vdmserr.Validation("cursor", "malformed"))
		return
	}
	items, err := h.svc.ListReviewQueueKeyset(r.Context(), status, cursorTime, cursorID, limit)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := reviewListResp{Items: make([]reviewItemResp, 0, len(items))}
	for i := range items {
		out.Items = append(out.Items, toReviewItemResp(&items[i], ""))
	}
	// A full page implies there may be more — hand back a cursor at the last row.
	if len(items) == limit {
		last := items[len(items)-1]
		out.NextCursor = encodeReviewCursor(last.CreatedAt, last.ID)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *ReviewQueueHandler) get(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	detail, err := h.svc.GetReviewItem(r.Context(), id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toReviewItemResp(detail.Item, detail.OCRText))
}

func (h *ReviewQueueHandler) resolve(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body resolveReq
	if derr := json.NewDecoder(r.Body).Decode(&body); derr != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in := &service.ResolveReviewInput{
		ReviewItemID: id,
		Decision:     strings.TrimSpace(body.Decision),
		ExternalKey:  body.ExternalKey,
		Notes:        body.Notes,
	}
	if strings.TrimSpace(body.TargetDocumentID) != "" {
		docID, perr := uuid.Parse(strings.TrimSpace(body.TargetDocumentID))
		if perr != nil {
			writeErr(w, r, vdmserr.Validation("target_document_id", "not a uuid"))
			return
		}
		in.TargetDocumentID = docID
	}
	res, err := h.svc.ResolveReviewItem(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	resp := resolveResp{Status: string(res.Status)}
	if res.DocumentID != uuid.Nil {
		resp.DocumentID = res.DocumentID.String()
	}
	if res.VersionID != uuid.Nil {
		resp.VersionID = res.VersionID.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- mappers + cursor helpers ----------------------------------------------

func toReviewItemResp(it *model.ReviewQueueItem, ocrText string) reviewItemResp {
	r := reviewItemResp{
		ID:                   it.ID.String(),
		IngestionItemID:      it.IngestionItemID.String(),
		WorkspaceID:          it.WorkspaceID.String(),
		TargetCustomerRef:    it.TargetCustomerRef,
		DocumentClass:        it.DocumentClass,
		ExtractedExternalKey: it.ExtractedExternalKey,
		Confidence:           it.Confidence,
		Reason:               string(it.Reason),
		Status:               string(it.Status),
		OCRText:              ocrText,
		CreatedAt:            it.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	if it.SuggestedMatchDocumentID != nil {
		r.SuggestedMatchDocumentID = it.SuggestedMatchDocumentID.String()
	}
	return r
}

// encodeReviewCursor packs the keyset position (created_at, id) into an opaque
// base64 token: "<RFC3339Nano>|<uuid>".
func encodeReviewCursor(t time.Time, id uuid.UUID) string {
	raw := t.UTC().Format(time.RFC3339Nano) + "|" + id.String()
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

// decodeReviewCursor reverses encodeReviewCursor. Empty cursor → zero time
// (first page).
func decodeReviewCursor(cursor string) (time.Time, uuid.UUID, error) {
	cursor = strings.TrimSpace(cursor)
	if cursor == "" {
		return time.Time{}, uuid.Nil, nil
	}
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return time.Time{}, uuid.Nil, vdmserr.Validation("cursor", "malformed")
	}
	t, terr := time.Parse(time.RFC3339Nano, parts[0])
	if terr != nil {
		return time.Time{}, uuid.Nil, terr
	}
	id, ierr := uuid.Parse(parts[1])
	if ierr != nil {
		return time.Time{}, uuid.Nil, ierr
	}
	return t, id, nil
}
