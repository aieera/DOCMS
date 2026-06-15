// Package bff is the Files BFF [C]: the server-side API the file-explorer /
// review / dashboard UIs call. It holds the SeDoc API key (never exposed to the
// browser), authenticates the ERP user, checks per-customer authorization via
// the ERP, and only then proxies to SeDoc scoped to that customer's folder
// subtree. SeDoc's correlation_id is surfaced on errors for support.
package bff

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
)

// BFF wires the dependencies.
type BFF struct {
	st          *store.Store
	doc         *sedoc.Client
	authz       erp.Authorizer
	workspaceID string
	log         zerolog.Logger
}

// New constructs the BFF.
func New(st *store.Store, doc *sedoc.Client, authz erp.Authorizer, workspaceID string, log zerolog.Logger) *BFF {
	return &BFF{st: st, doc: doc, authz: authz, workspaceID: workspaceID, log: log}
}

// Register mounts the routes.
func (b *BFF) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	// Per-customer (customer-scoped) routes.
	mux.HandleFunc("GET /files/customers/{ref}/tree", b.tree)
	mux.HandleFunc("GET /files/customers/{ref}/folders/{folder_id}/documents", b.folderDocuments)
	mux.HandleFunc("POST /files/customers/{ref}/upload", b.upload)
	mux.HandleFunc("GET /files/search", b.search)
	// Document-id routes (authz resolved via the document's folder → customer).
	mux.HandleFunc("GET /files/documents/{id}", b.documentDetail)
	mux.HandleFunc("GET /files/documents/{id}/download", b.download)
	mux.HandleFunc("POST /files/documents/{id}/versions/{vid}/restore", b.restore)
	// Admin routes (review queue + sync ops).
	mux.HandleFunc("GET /files/review-queue", b.adminReviewList)
	mux.HandleFunc("GET /files/review-queue/{id}", b.adminReviewGet)
	mux.HandleFunc("POST /files/review-queue/{id}/resolve", b.adminReviewResolve)
	mux.HandleFunc("GET /files/sync/log", b.adminSyncLog)
	mux.HandleFunc("POST /files/sync/retry/{id}", b.adminSyncRetry)
}

// ---- auth + authz ----------------------------------------------------------

// erpUser returns the upstream-authenticated ERP user id, or "" (→ 401). In
// production this header is stamped by the ERP's auth proxy; the BFF trusts it
// the way SeDoc trusts X-User-ID behind Kong.
func erpUser(r *http.Request) string { return r.Header.Get("X-ERP-User") }

func isAdmin(r *http.Request) bool { return r.Header.Get("X-ERP-Admin") == "true" }

// authorizeCustomer runs the full gate: authenticated user → ERP customer authz
// → resolve the customer's folder map. Writes the HTTP error and returns nil on
// any failure.
func (b *BFF) authorizeCustomer(w http.ResponseWriter, r *http.Request, ref string) *store.CustomerMapping {
	user := erpUser(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return nil
	}
	ok, err := b.authz.CanAccessCustomer(r.Context(), user, ref)
	if err != nil {
		b.log.Error().Err(err).Str("customer", ref).Msg("authz check")
		writeErr(w, http.StatusBadGateway, "authz check failed", "")
		return nil
	}
	if !ok {
		// 404 (not 403) so we don't even confirm the customer exists to an
		// unauthorized user.
		writeErr(w, http.StatusNotFound, "not found", "")
		return nil
	}
	m, err := b.st.GetCustomer(r.Context(), ref)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "customer not provisioned yet", "")
		return nil
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return nil
	}
	return m
}

// folderInCustomer reports whether a folder id belongs to a customer's subtree.
func folderInCustomer(m *store.CustomerMapping, folderID string) bool {
	if folderID == m.MainFolderID || folderID == m.BucketFolderID {
		return true
	}
	for _, id := range m.SubfolderIDs {
		if id == folderID {
			return true
		}
	}
	return false
}

// ---- customer-scoped handlers ----------------------------------------------

func (b *BFF) tree(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	m := b.authorizeCustomer(w, r, ref)
	if m == nil {
		return
	}
	// The 6 subfolders (with doc counts) are the children of the main folder.
	res, err := b.doc.ListFolders(r.Context(), b.workspaceID, m.MainFolderID, "", 50)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"customer_ref":    ref,
		"name":            m.Name,
		"main_folder_id":  m.MainFolderID,
		"folders":         res.Folders,
		"next_page_token": res.NextPageToken,
	})
}

func (b *BFF) folderDocuments(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	m := b.authorizeCustomer(w, r, ref)
	if m == nil {
		return
	}
	folderID := r.PathValue("folder_id")
	if !folderInCustomer(m, folderID) {
		writeErr(w, http.StatusForbidden, "folder not in customer subtree", "")
		return
	}
	res, err := b.doc.ListDocuments(r.Context(), b.workspaceID, folderID, r.URL.Query().Get("cursor"), 50)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (b *BFF) upload(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	m := b.authorizeCustomer(w, r, ref)
	if m == nil {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart form with a 'file' field", "")
		return
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing 'file'", "")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file", "")
		return
	}
	mime := hdr.Header.Get("Content-Type")
	if mime == "" {
		mime = "application/octet-stream"
	}
	// A fresh idempotency base per interactive upload.
	base := "bff-upload:" + uuid.NewString()
	up, err := b.doc.UploadBlob(r.Context(), base, "us-east-1", hdr.Filename, mime, data)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	res, err := b.doc.Ingest(r.Context(), base+":ingest", sedoc.IngestInput{
		WorkspaceID:       b.workspaceID,
		FolderID:          m.SubfolderIDs["attachments"],
		TargetCustomerRef: ref,
		BlobChecksum:      up.SHA256,
		ContentBlobID:     up.ContentBlobID,
		Mime:              mime,
	})
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	if res.IngestionItemID != "" {
		_ = b.st.TrackIngestion(r.Context(), res.IngestionItemID, ref, res.Status)
	}
	writeJSON(w, http.StatusOK, res)
}

func (b *BFF) search(w http.ResponseWriter, r *http.Request) {
	ref := r.URL.Query().Get("customer_ref")
	if ref == "" {
		writeErr(w, http.StatusBadRequest, "customer_ref required", "")
		return
	}
	m := b.authorizeCustomer(w, r, ref)
	if m == nil {
		return
	}
	// Scope: run the query workspace-wide, then drop any hit whose folder isn't
	// in this customer's subtree so results never leak across customers.
	body := map[string]any{
		"query":     r.URL.Query().Get("q"),
		"page_size": 50,
		"filters":   map[string]any{"workspace_id": b.workspaceID},
	}
	raw, err := b.doc.Search(r.Context(), body)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, scopeSearchToCustomer(raw, m))
}

// ---- document-id handlers (authz via folder → customer) --------------------

// resolveDocCustomer loads the document, maps its folder to a customer, and runs
// the authz gate. Returns the document on success; writes the error otherwise.
func (b *BFF) resolveDocCustomer(w http.ResponseWriter, r *http.Request, docID string) *sedoc.Document {
	user := erpUser(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return nil
	}
	doc, err := b.doc.GetDocument(r.Context(), docID)
	if err != nil {
		b.proxyErr(w, err)
		return nil
	}
	ref, err := b.st.CustomerByFolder(r.Context(), doc.FolderID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found", "")
		return nil
	}
	ok, aerr := b.authz.CanAccessCustomer(r.Context(), user, ref)
	if aerr != nil {
		writeErr(w, http.StatusBadGateway, "authz check failed", "")
		return nil
	}
	if !ok {
		writeErr(w, http.StatusNotFound, "not found", "")
		return nil
	}
	return doc
}

func (b *BFF) documentDetail(w http.ResponseWriter, r *http.Request) {
	doc := b.resolveDocCustomer(w, r, r.PathValue("id"))
	if doc == nil {
		return
	}
	vers, err := b.doc.ListVersions(r.Context(), doc.ID)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"document": doc, "versions": vers.Versions})
}

func (b *BFF) download(w http.ResponseWriter, r *http.Request) {
	doc := b.resolveDocCustomer(w, r, r.PathValue("id"))
	if doc == nil {
		return
	}
	rc, ct, err := b.doc.StreamContent(r.Context(), doc.ID)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	defer rc.Close()
	w.Header().Set("Content-Type", ct)
	// Stream straight through — flat memory, and the storage key never reaches
	// the browser (the bytes come from SeDoc via the BFF's authenticated call).
	_, _ = io.Copy(w, rc)
}

func (b *BFF) restore(w http.ResponseWriter, r *http.Request) {
	doc := b.resolveDocCustomer(w, r, r.PathValue("id"))
	if doc == nil {
		return
	}
	if err := b.doc.RestoreVersion(r.Context(), "bff-restore:"+uuid.NewString(), doc.ID, r.PathValue("vid")); err != nil {
		b.proxyErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- admin handlers (review queue + sync) ----------------------------------

func (b *BFF) adminReviewList(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	q := r.URL.Query()
	raw, err := b.doc.ReviewQueueList(r.Context(), q.Get("status"), q.Get("cursor"), 50)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeRaw(w, http.StatusOK, raw)
}

func (b *BFF) adminReviewGet(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	raw, err := b.doc.ReviewQueueGet(r.Context(), r.PathValue("id"))
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeRaw(w, http.StatusOK, raw)
}

func (b *BFF) adminReviewResolve(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	raw, err := b.doc.ReviewQueueResolve(r.Context(), "bff-resolve:"+uuid.NewString(), r.PathValue("id"), body)
	if err != nil {
		b.proxyErr(w, err)
		return
	}
	writeRaw(w, http.StatusOK, raw)
}

func (b *BFF) adminSyncLog(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	rows, err := b.st.ListSyncLog(r.Context(), r.URL.Query().Get("status"), 100)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": rows})
}

func (b *BFF) adminSyncRetry(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad id", "")
		return
	}
	if err := b.st.RetryFailed(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (b *BFF) requireAdmin(w http.ResponseWriter, r *http.Request) bool {
	if erpUser(r) == "" {
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return false
	}
	if !isAdmin(r) {
		writeErr(w, http.StatusForbidden, "admin only", "")
		return false
	}
	return true
}

// ---- helpers ---------------------------------------------------------------

// scopeSearchToCustomer drops hits whose folder_id isn't in the customer subtree.
func scopeSearchToCustomer(raw sedoc.RawJSON, m *store.CustomerMapping) map[string]any {
	var parsed struct {
		Results    []map[string]any `json:"results"`
		TotalCount int64            `json:"total_count"`
		PageToken  string           `json:"page_token"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return map[string]any{"results": []any{}}
	}
	kept := make([]map[string]any, 0, len(parsed.Results))
	for _, h := range parsed.Results {
		if fid, _ := h["folder_id"].(string); folderInCustomer(m, fid) {
			kept = append(kept, h)
		}
	}
	return map[string]any{"results": kept, "page_token": parsed.PageToken, "scoped": true}
}

// proxyErr renders a SeDoc error, surfacing its correlation_id for support.
func (b *BFF) proxyErr(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if ae, ok := err.(*sedoc.APIError); ok && ae.StatusCode >= 400 {
		status = ae.StatusCode
	}
	writeErr(w, status, err.Error(), sedoc.CorrelationID(err))
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeRaw(w http.ResponseWriter, code int, raw []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_, _ = w.Write(raw)
}

func writeErr(w http.ResponseWriter, code int, msg, correlationID string) {
	writeJSON(w, code, map[string]any{"error": msg, "correlation_id": correlationID})
}
