// dms_adapter.go — the "DMS API contract" the CRM's dmsSync module
// (Backend/src/core/dmsSync) was built against, served on the BFF and
// translated onto SeDoc's real API. It lets the CRM stay config-only: point
// external_apis.dms_base_url at this BFF and the existing push path works
// unchanged.
//
// Contract surface the CRM calls (all under dms_base_url):
//   POST   /documents                              multipart upload (Endpoint 1)
//   GET    /folders[?parentId=]                     folder tree, admin picker (Endpoint 2)
//   GET    /health                                  liveness (Endpoint 3)
//   POST   /api/v1/search                           pre-create dedup (dmsApi.js)
//   GET    /api/v1/workspaces/{wid}/folders[?name=] ensure-folder list (dmsApi.js)
//   POST   /api/v1/workspaces/{wid}/folders         ensure-folder create (dmsApi.js)
//
// Mapping onto SeDoc:
//   - upload      → 3-step storage (UploadStream) + documents:upsert keyed on a
//                   stable external_id `erp:{type}:{id}` (idempotent re-push).
//   - dedup search→ documents:byExternalKey on that same external_id.
//   - folders     → the real /workspaces/{wid}/folders API (list + ensure).
//
// Auth: the CRM sends `Authorization: Bearer <dms_api_key>`. That key is a
// shared secret this adapter validates (DMS_ADAPTER_TOKEN) — NOT the SeDoc API
// key, which stays server-side in the shared sedoc.Client. When no token is
// configured the adapter accepts any caller (dev only) and logs a warning once.
package bff

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/sedoc"
)

// DMSAdapter serves the CRM's DMS contract against SeDoc via the shared client.
type DMSAdapter struct {
	doc         *sedoc.Client
	workspaceID string
	token       string
	log         zerolog.Logger
}

// NewDMSAdapter builds the adapter. token is the shared secret the CRM presents
// as a bearer (its external_apis.dms_api_key); empty disables the check (dev).
func NewDMSAdapter(doc *sedoc.Client, workspaceID, token string, log zerolog.Logger) *DMSAdapter {
	a := &DMSAdapter{doc: doc, workspaceID: workspaceID, token: token, log: log.With().Str("component", "dms-adapter").Logger()}
	if token == "" {
		a.log.Warn().Msg("DMS_ADAPTER_TOKEN not set — adapter accepts unauthenticated callers (dev only)")
	}
	return a
}

// Register mounts the contract routes. They never collide with the explorer's
// /files/* or /healthz routes, so both share the BFF mux.
func (a *DMSAdapter) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /health", a.health)
	// Upload. The CRM's worker config (#loadConfig) hardcodes the VaultDMS path
	// /api/v1/documents; /documents is the generic-default alias. Same handler.
	mux.HandleFunc("POST /api/v1/documents", a.pushDocument)
	mux.HandleFunc("POST /documents", a.pushDocument)
	mux.HandleFunc("POST /api/v1/search", a.search)
	mux.HandleFunc("GET /api/v1/workspaces/{wid}/folders", a.listWorkspaceFolders)
	mux.HandleFunc("POST /api/v1/workspaces/{wid}/folders", a.createWorkspaceFolder)
	mux.HandleFunc("GET /folders", a.listFolders)
}

// authed validates the CRM's shared-secret bearer (constant-time). Health is
// exempt (callers probe it without credentials).
func (a *DMSAdapter) authed(w http.ResponseWriter, r *http.Request) bool {
	if a.token == "" {
		return true
	}
	got := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if subtle.ConstantTimeCompare([]byte(got), []byte(a.token)) == 1 {
		return true
	}
	writeErr(w, http.StatusUnauthorized, "invalid or missing bearer token", "")
	return false
}

func (a *DMSAdapter) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// pushDocument is the CRM upload (multipart POST /documents). The CRM sends the
// `file` part FIRST, then the metadata fields — so we stream the file straight
// into SeDoc's presigned PUT (UploadStream spools to a temp file as it computes
// the checksum), then read the remaining fields and upsert.
func (a *DMSAdapter) pushDocument(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r) {
		return
	}
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, http.StatusBadRequest, "expected multipart/form-data", "")
		return
	}
	var (
		up     *sedoc.UploadResult
		mime   string
		fields = map[string]string{}
	)
	for {
		part, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErr(w, http.StatusBadRequest, "read multipart: "+err.Error(), "")
			return
		}
		if part.FormName() == "file" {
			mime = part.Header.Get("Content-Type")
			if mime == "" {
				mime = "application/pdf"
			}
			base := "dms-push:" + uuid.NewString()
			up, err = a.doc.UploadStream(r.Context(), base, "us-east-1", part.FileName(), mime, part, -1, "")
			if err != nil {
				a.proxyErr(w, err)
				return
			}
			continue
		}
		v, _ := io.ReadAll(io.LimitReader(part, 1<<20))
		fields[part.FormName()] = strings.TrimSpace(string(v))
	}
	if up == nil {
		writeErr(w, http.StatusBadRequest, "missing 'file' part", "")
		return
	}

	entityType, entityID := fields["erp_entity_type"], fields["erp_entity_id"]
	if entityType == "" || entityID == "" {
		writeErr(w, http.StatusBadRequest, "erp_entity_type and erp_entity_id are required", "")
		return
	}
	externalID := erpExternalID(entityType, entityID)
	docClass := firstNonEmpty(fields["dms_doc_type"], entityType)

	// SeDoc's upsert requires a folder UUID. The CRM normally resolves one via
	// the folders API and sends `folder_id`; if absent, ensure a per-doc-class
	// default folder so a push never fails for lack of one.
	folderID := firstNonEmpty(fields["folder_id"], fields["folder"])
	if folderID == "" {
		name := defaultFolderName(docClass)
		fid, ferr := a.doc.EnsureFolder(r.Context(), "dms-folder:"+name, a.workspaceID, nil, name)
		if ferr != nil {
			a.proxyErr(w, ferr)
			return
		}
		folderID = fid
	}

	in := sedoc.UpsertInput{
		WorkspaceID:    a.workspaceID,
		ExternalID:     externalID,
		FolderID:       folderID,
		Title:          firstNonEmpty(fields["document_number"], entityType+" "+entityID),
		DocumentClass:  docClass,
		DocType:        fields["dms_doc_type"],
		Tags:           parseJSONStringArray(fields["tags"]),
		CustomMetadata: parseJSONObject(fields["custom_metadata"]),
		BlobChecksum:   up.SHA256,
		BlobRef:        up.ContentBlobID,
		Mime:           mime,
		Size:           up.SizeBytes,
		ChangeSummary:  "ERP push (" + entityType + ":" + entityID + ")",
	}
	res, err := a.doc.UpsertByExternalKey(r.Context(), "dms-upsert:"+externalID+":"+up.SHA256, in)
	if err != nil {
		a.proxyErr(w, err)
		return
	}
	a.log.Info().
		Str("external_id", externalID).Str("document_id", res.DocumentID).
		Bool("created", res.Created).Bool("version_created", res.VersionCreated).
		Msg("erp document pushed")

	status := http.StatusOK
	if res.Created {
		status = http.StatusCreated
	}
	// `id` is what the CRM's #extractDocId reads; the rest are extras.
	writeJSON(w, status, map[string]any{
		"id":              res.DocumentID,
		"document_id":     res.DocumentID,
		"external_id":     externalID,
		"version_id":      res.CurrentVersionID,
		"version_number":  res.CurrentVersionNumber,
		"created":         res.Created,
		"version_created": res.VersionCreated,
	})
}

// search backs the CRM's pre-create dedup (findExistingDocument). The CRM filters
// on `custom_metadata.erp_entity_type` + `custom_metadata.erp_<entity>_id`; we
// reconstruct the external_id from those and resolve it via byExternalKey, so
// dedup works without depending on the search index having the custom fields.
func (a *DMSAdapter) search(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r) {
		return
	}
	var req struct {
		Filter map[string]any `json:"filter"`
		Limit  int            `json:"limit"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json body", "")
		return
	}
	entityType, entityID := erpIdentityFromFilter(req.Filter)
	if entityType == "" || entityID == "" {
		writeJSON(w, http.StatusOK, emptySearch())
		return
	}
	externalID := erpExternalID(entityType, entityID)
	docID, found, err := a.doc.GetByExternalKey(r.Context(), externalID)
	if err != nil {
		a.proxyErr(w, err)
		return
	}
	if !found {
		writeJSON(w, http.StatusOK, emptySearch())
		return
	}
	hit := map[string]any{"id": docID, "document_id": docID, "external_id": externalID}
	writeJSON(w, http.StatusOK, map[string]any{"data": []any{hit}, "results": []any{hit}})
}

// listWorkspaceFolders backs dmsApi.ensureFolder's GET (find by name).
func (a *DMSAdapter) listWorkspaceFolders(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r) {
		return
	}
	wid := firstNonEmpty(r.PathValue("wid"), a.workspaceID)
	res, err := a.doc.ListFolders(r.Context(), wid, r.URL.Query().Get("parent_folder_id"), "", 200)
	if err != nil {
		a.proxyErr(w, err)
		return
	}
	name := r.URL.Query().Get("name")
	out := make([]map[string]any, 0, len(res.Folders))
	for _, f := range res.Folders {
		if name != "" && f.Name != name {
			continue
		}
		out = append(out, map[string]any{"id": f.ID, "name": f.Name, "parent_folder_id": f.ParentFolderID})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out, "data": out})
}

// createWorkspaceFolder backs dmsApi.ensureFolder's POST (idempotent ensure).
func (a *DMSAdapter) createWorkspaceFolder(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r) {
		return
	}
	wid := firstNonEmpty(r.PathValue("wid"), a.workspaceID)
	var req struct {
		Name           string `json:"name"`
		ParentFolderID string `json:"parent_folder_id"`
		Visibility     string `json:"visibility"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "name required", "")
		return
	}
	var parent *string
	if req.ParentFolderID != "" {
		parent = &req.ParentFolderID
	}
	fid, err := a.doc.EnsureFolder(r.Context(), "dms-folder:"+req.Name, wid, parent, req.Name)
	if err != nil {
		a.proxyErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": fid, "folder_id": fid, "name": req.Name})
}

// listFolders backs the CRM admin folder-picker (DMSService.listFolders).
func (a *DMSAdapter) listFolders(w http.ResponseWriter, r *http.Request) {
	if !a.authed(w, r) {
		return
	}
	res, err := a.doc.ListFolders(r.Context(), a.workspaceID, r.URL.Query().Get("parentId"), "", 200)
	if err != nil {
		a.proxyErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(res.Folders))
	for _, f := range res.Folders {
		out = append(out, map[string]any{
			"id":          f.ID,
			"name":        f.Name,
			"parentId":    f.ParentFolderID,
			"hasChildren": f.ChildFolderCount > 0,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"folders": out, "data": out})
}

func (a *DMSAdapter) proxyErr(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if ae, ok := err.(*sedoc.APIError); ok && ae.StatusCode >= 400 {
		status = ae.StatusCode
	}
	writeErr(w, status, err.Error(), sedoc.CorrelationID(err))
}

// ---- helpers ---------------------------------------------------------------

// erpExternalID is the stable dedup key shared by the upload + the search.
func erpExternalID(entityType, entityID string) string { return "erp:" + entityType + ":" + entityID }

func emptySearch() map[string]any { return map[string]any{"data": []any{}, "results": []any{}} }

// erpIdentityFromFilter pulls (entity_type, entity_id) out of the CRM's dedup
// filter: {"custom_metadata.erp_entity_type": "invoice",
//          "custom_metadata.erp_invoice_id": 42}.
func erpIdentityFromFilter(filter map[string]any) (entityType, entityID string) {
	for k, v := range filter {
		key := strings.TrimPrefix(k, "custom_metadata.")
		if key == k {
			continue // not a custom_metadata.* key
		}
		val := stringifyScalar(v)
		switch {
		case key == "erp_entity_type":
			entityType = val
		case strings.HasPrefix(key, "erp_") && strings.HasSuffix(key, "_id"):
			entityID = val
		}
	}
	return
}

func stringifyScalar(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		if x == float64(int64(x)) {
			return strconv.FormatInt(int64(x), 10)
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return ""
	}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func parseJSONStringArray(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

func parseJSONObject(s string) map[string]any {
	if s == "" {
		return nil
	}
	var out map[string]any
	if json.Unmarshal([]byte(s), &out) != nil {
		return nil
	}
	return out
}

// defaultFolderName mirrors the CRM's DEFAULT_DMS_FOLDERS so a fallback folder
// matches what an admin-configured one would be named.
func defaultFolderName(docClass string) string {
	switch docClass {
	case "invoice":
		return "ERP Invoices"
	case "quote":
		return "ERP Quotes"
	case "sales_order":
		return "ERP Sales Orders"
	case "payment_received":
		return "ERP Payments Received"
	default:
		return "ERP Documents"
	}
}
