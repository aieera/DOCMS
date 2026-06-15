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
	"strconv"
	"strings"
	"time"

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
	workerURL   string // base URL of the integration worker, for the metrics passthrough
	log         zerolog.Logger
}

// New constructs the BFF.
func New(st *store.Store, doc *sedoc.Client, authz erp.Authorizer, workspaceID string, log zerolog.Logger) *BFF {
	return &BFF{st: st, doc: doc, authz: authz, workspaceID: workspaceID, log: log}
}

// WithWorkerURL sets the integration worker's base URL so GET /files/sync/metrics
// can proxy the worker's runtime metrics (throughput + limiter state). Without
// it, that route returns 503. Returns the BFF for chaining.
func (b *BFF) WithWorkerURL(u string) *BFF {
	b.workerURL = strings.TrimRight(u, "/")
	return b
}

// Register mounts the routes.
func (b *BFF) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	// Per-customer (customer-scoped) routes.
	mux.HandleFunc("GET /files/customers/{ref}/tree", b.tree)
	mux.HandleFunc("GET /files/customers/{ref}/folders/{folder_id}/documents", b.folderDocuments)
	mux.HandleFunc("POST /files/customers/{ref}/upload", b.upload)
	// POST (not GET): the free-text query travels in the body, not the URL, so it
	// never lands in access logs.
	mux.HandleFunc("POST /files/search", b.search)
	// Document-id routes (authz resolved via the document's folder → customer).
	mux.HandleFunc("GET /files/documents/{id}", b.documentDetail)
	mux.HandleFunc("GET /files/documents/{id}/download", b.download)
	mux.HandleFunc("POST /files/documents/{id}/versions/{vid}/restore", b.restore)
	// Admin routes (review queue + sync ops).
	mux.HandleFunc("GET /files/review-queue", b.adminReviewList)
	mux.HandleFunc("GET /files/review-queue/{id}", b.adminReviewGet)
	mux.HandleFunc("POST /files/review-queue/{id}/resolve", b.adminReviewResolve)
	mux.HandleFunc("GET /files/sync/log", b.adminSyncLog)
	mux.HandleFunc("GET /files/sync/metrics", b.adminSyncMetrics)
	mux.HandleFunc("POST /files/sync/retry/{id}", b.adminSyncRetry)
	mux.HandleFunc("POST /files/sync/backfill", b.adminBackfillStart)
	mux.HandleFunc("GET /files/sync/backfill", b.adminBackfillList)
	mux.HandleFunc("GET /files/sync/backfill/{run_id}", b.adminBackfillGet)
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

// upload streams a multipart file straight to SeDoc's presigned PUT, then fires
// /ingest. It reads parts incrementally (no ParseMultipartForm, no size cap), so
// a multi-hundred-MB attachment holds flat memory. The browser sends, IN ORDER,
// a "sha256" field, a "size" field, then the "file" part: the hash lets initiate
// dedup and the size sets the PUT Content-Length, enabling the zero-copy path. If
// either is absent (non-browser client), UploadStream spools to a temp file.
func (b *BFF) upload(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	m := b.authorizeCustomer(w, r, ref)
	if m == nil {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data", "")
		return
	}
	var clientSHA string
	var declaredSize int64 = -1
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read multipart: "+err.Error(), "")
			return
		}
		switch part.FormName() {
		case "sha256":
			v, _ := io.ReadAll(io.LimitReader(part, 128))
			clientSHA = strings.TrimSpace(string(v))
		case "size":
			v, _ := io.ReadAll(io.LimitReader(part, 32))
			declaredSize, _ = strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
		case "file":
			mime := part.Header.Get("Content-Type")
			if mime == "" {
				mime = "application/octet-stream"
			}
			// A fresh idempotency base per interactive upload.
			base := "bff-upload:" + uuid.NewString()
			up, err := b.doc.UploadStream(r.Context(), base, "us-east-1", part.FileName(), mime, part, declaredSize, clientSHA)
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
			return // the file part is last; nothing more to read
		}
	}
	writeErr(w, http.StatusBadRequest, "missing 'file' part", "")
}

// search runs a customer-scoped query. The query text + customer_ref travel in
// the POST body (never the URL) so they don't leak into access logs. Authz +
// subtree scoping are unchanged from the prior GET form.
func (b *BFF) search(w http.ResponseWriter, r *http.Request) {
	if erpUser(r) == "" {
		// Reject before reading the body — don't even parse for an anon caller.
		writeErr(w, http.StatusUnauthorized, "unauthenticated", "")
		return
	}
	var req struct {
		CustomerRef string `json:"customer_ref"`
		Query       string `json:"q"`
		PageSize    int    `json:"page_size"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body", "")
		return
	}
	if req.CustomerRef == "" {
		writeErr(w, http.StatusBadRequest, "customer_ref required", "")
		return
	}
	m := b.authorizeCustomer(w, r, req.CustomerRef)
	if m == nil {
		return
	}
	pageSize := req.PageSize
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 50
	}
	// Scope: run the query workspace-wide, then drop any hit whose folder isn't
	// in this customer's subtree so results never leak across customers.
	body := map[string]any{
		"query":     req.Query,
		"page_size": pageSize,
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

// adminSyncMetrics proxies the worker's operational metrics (throughput,
// backlog, DLQ, limiter state) to the dashboard. The worker holds the limiter +
// runtime counters in-process, so this is a server-side passthrough.
func (b *BFF) adminSyncMetrics(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	if b.workerURL == "" {
		writeErr(w, http.StatusServiceUnavailable, "worker metrics endpoint not configured", "")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, b.workerURL+"/metrics/sync", nil)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "worker unreachable: "+err.Error(), "")
		return
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	writeRaw(w, resp.StatusCode, body)
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

// adminBackfillStart enqueues a pending backfill run (the worker poller picks it
// up and executes it under the shared rate limiter) and returns its run_id. An
// optional {"customer_refs":[...]} body scopes the run to specific customers.
func (b *BFF) adminBackfillStart(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	var body struct {
		CustomerRefs []string `json:"customer_refs"`
	}
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body)
	id, err := b.st.CreateBackfillRun(r.Context(), "erp", strings.Join(body.CustomerRefs, ","), false)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": id, "status": "pending"})
}

// adminBackfillList returns recent backfill runs with their progress.
func (b *BFF) adminBackfillList(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	runs, err := b.st.ListBackfillRuns(r.Context(), 50)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": runs})
}

// adminBackfillGet returns one run plus its per-item failures.
func (b *BFF) adminBackfillGet(w http.ResponseWriter, r *http.Request) {
	if !b.requireAdmin(w, r) {
		return
	}
	id, err := uuid.Parse(r.PathValue("run_id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad run_id", "")
		return
	}
	run, err := b.st.GetBackfillRun(r.Context(), id)
	if err == store.ErrNotFound {
		writeErr(w, http.StatusNotFound, "not found", "")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	fails, err := b.st.ListBackfillFailures(r.Context(), id, 200)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error(), "")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run, "failures": fails})
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
