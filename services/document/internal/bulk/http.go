package bulk

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
)

// HTTPHandler exposes the bulk surface over HTTP NDJSON for the
// admin wizard. Routes:
//
//   POST /api/v1/admin/bulk/import        body: NDJSON, response: NDJSON
//   GET  /api/v1/admin/bulk/export        query: resource=document|workspace|folder
//                                                 [&workspace_id=&from=&to=&page_size=]
//                                         response: NDJSON
//
// Both endpoints stream — the import response writes one JSON object
// per processed line as soon as it's done, the export streams one
// JSON object per row. Supports very large datasets without
// buffering everything in memory.
type HTTPHandler struct {
	svc *Service
	log zerolog.Logger
}

func NewHTTPHandler(svc *Service, log zerolog.Logger) *HTTPHandler {
	return &HTTPHandler{svc: svc, log: log}
}

func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/bulk/import", h.handleImport)
	mux.HandleFunc("GET /api/v1/admin/bulk/export", h.handleExport)
}

// importLine is the JSON-decoded shape of one NDJSON input line.
// The wizard sends a discriminator + one of five payload variants.
type importLine struct {
	Resource  string          `json:"resource"`
	Workspace json.RawMessage `json:"workspace,omitempty"`
	Folder    json.RawMessage `json:"folder,omitempty"`
	Document  json.RawMessage `json:"document,omitempty"`
	User      json.RawMessage `json:"user,omitempty"`
	Group     json.RawMessage `json:"group,omitempty"`
}

// requestEnvelope is the optional first line of the NDJSON body.
// Carries request_id + batch size. If omitted, defaults are used
// and a fresh request_id is minted server-side (loses idempotency
// — clients should always send this).
type requestEnvelope struct {
	RequestID string `json:"request_id,omitempty"`
	BatchSize int    `json:"batch_size,omitempty"`
}

func (h *HTTPHandler) handleImport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := h.callerTenant(r)
	if err != nil {
		writeErrJSON(w, http.StatusUnauthorized, err.Error())
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 1<<20), 8<<20) // up to 8 MiB per line for big metadata blobs
	envelope := requestEnvelope{BatchSize: 100}
	itemsByRequest := make([]*sedocv1.BulkItem, 0, envelope.BatchSize)

	flushBatch := func(reqID string) {
		if len(itemsByRequest) == 0 {
			return
		}
		req := &sedocv1.BulkImportRequest{
			RequestId: reqID,
			Items:     itemsByRequest,
		}
		resp, perr := h.svc.ProcessBatch(r.Context(), tenantID, req)
		if perr != nil {
			_ = enc.Encode(map[string]any{"error": perr.Error()})
		} else {
			for _, res := range resp.GetResults() {
				_ = enc.Encode(res)
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		itemsByRequest = itemsByRequest[:0]
	}

	first := true
	batchSeq := 0
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		// Try to decode the first non-empty line as the envelope.
		// Falls through to a normal item line if the decoded
		// resource discriminator is non-empty.
		if first {
			first = false
			var env requestEnvelope
			if jerr := json.Unmarshal(line, &env); jerr == nil && env.RequestID != "" && env.BatchSize >= 0 {
				if env.BatchSize > 0 {
					envelope.BatchSize = env.BatchSize
				}
				envelope.RequestID = env.RequestID
				continue
			}
		}
		var raw importLine
		if jerr := json.Unmarshal(line, &raw); jerr != nil {
			_ = enc.Encode(map[string]any{"error": jerr.Error()})
			continue
		}
		item, ierr := decodeItem(raw)
		if ierr != nil {
			_ = enc.Encode(map[string]any{"error": ierr.Error()})
			continue
		}
		itemsByRequest = append(itemsByRequest, item)
		if len(itemsByRequest) >= envelope.BatchSize {
			flushBatch(batchRequestID(envelope.RequestID, batchSeq))
			batchSeq++
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		_ = enc.Encode(map[string]any{"error": err.Error()})
	}
	flushBatch(batchRequestID(envelope.RequestID, batchSeq))
}

// batchRequestID composes a deterministic request id per batch when
// the client supplied a base. Without a base, mints a fresh UUID
// (idempotency lost — flagged in the docs).
func batchRequestID(base string, seq int) string {
	if base == "" {
		id, _ := uuid.NewV7()
		return id.String()
	}
	return fmt.Sprintf("%s-%d", base, seq)
}

func decodeItem(raw importLine) (*sedocv1.BulkItem, error) {
	switch raw.Resource {
	case "workspace":
		var w sedocv1.BulkWorkspace
		if err := json.Unmarshal(raw.Workspace, &w); err != nil {
			return nil, fmt.Errorf("workspace decode: %w", err)
		}
		return &sedocv1.BulkItem{Resource: &sedocv1.BulkItem_Workspace{Workspace: &w}}, nil
	case "folder":
		var f sedocv1.BulkFolder
		if err := json.Unmarshal(raw.Folder, &f); err != nil {
			return nil, fmt.Errorf("folder decode: %w", err)
		}
		return &sedocv1.BulkItem{Resource: &sedocv1.BulkItem_Folder{Folder: &f}}, nil
	case "document":
		var d sedocv1.BulkDocument
		if err := json.Unmarshal(raw.Document, &d); err != nil {
			return nil, fmt.Errorf("document decode: %w", err)
		}
		return &sedocv1.BulkItem{Resource: &sedocv1.BulkItem_Document{Document: &d}}, nil
	case "user":
		var u sedocv1.BulkUser
		if err := json.Unmarshal(raw.User, &u); err != nil {
			return nil, fmt.Errorf("user decode: %w", err)
		}
		return &sedocv1.BulkItem{Resource: &sedocv1.BulkItem_User{User: &u}}, nil
	case "group":
		var g sedocv1.BulkGroup
		if err := json.Unmarshal(raw.Group, &g); err != nil {
			return nil, fmt.Errorf("group decode: %w", err)
		}
		return &sedocv1.BulkItem{Resource: &sedocv1.BulkItem_Group{Group: &g}}, nil
	default:
		return nil, fmt.Errorf("unknown resource %q (expected workspace|folder|document|user|group)", raw.Resource)
	}
}

func (h *HTTPHandler) handleExport(w http.ResponseWriter, r *http.Request) {
	tenantID, err := h.callerTenant(r)
	if err != nil {
		writeErrJSON(w, http.StatusUnauthorized, err.Error())
		return
	}
	resource := r.URL.Query().Get("resource")
	kind := parseResourceKind(resource)
	if kind == sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_UNSPECIFIED {
		writeErrJSON(w, http.StatusBadRequest, "resource must be one of workspace|folder|document")
		return
	}
	wsID := uuid.Nil
	if s := r.URL.Query().Get("workspace_id"); s != "" {
		wsID, _ = uuid.Parse(s)
	}
	from, _ := time.Parse(time.RFC3339, r.URL.Query().Get("from"))
	to, _ := time.Parse(time.RFC3339, r.URL.Query().Get("to"))

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)

	exErr := h.svc.Export(r.Context(), tenantID, ExportOptions{
		Resource: kind, WorkspaceID: wsID, From: from, To: to,
	}, func(page *sedocv1.BulkExportResponse) error {
		for _, item := range page.GetItems() {
			if err := enc.Encode(item); err != nil {
				return err
			}
		}
		if flusher != nil {
			flusher.Flush()
		}
		return nil
	})
	if exErr != nil {
		_ = enc.Encode(map[string]any{"error": exErr.Error()})
	}
}

func parseResourceKind(s string) sedocv1.BulkResourceKind {
	switch s {
	case "workspace":
		return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_WORKSPACE
	case "folder":
		return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_FOLDER
	case "document":
		return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_DOCUMENT
	case "user":
		return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_USER
	case "group":
		return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_GROUP
	}
	return sedocv1.BulkResourceKind_BULK_RESOURCE_KIND_UNSPECIFIED
}

func (h *HTTPHandler) callerTenant(r *http.Request) (uuid.UUID, error) {
	u, err := auth.User(r.Context())
	if err == nil && u.TenantID != uuid.Nil {
		return u.TenantID, nil
	}
	return auth.GetTenantID(r.Context())
}

func writeErrJSON(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// silence unused import noise on builds when context isn't read
// directly — kept here to make the file's intent clear.
var _ = context.Background
