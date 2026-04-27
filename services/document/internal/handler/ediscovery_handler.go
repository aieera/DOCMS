// §9.5 / G9 — eDiscovery export.
//
//	POST /api/v1/admin/ediscovery/export
//
// Tenant-admin surface; the exported ZIP is streamed directly to the
// response body with a Content-Disposition filename composed from
// case_id + timestamp.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type EDiscoveryHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewEDiscoveryHandler(svc *service.DocumentService, log zerolog.Logger) *EDiscoveryHandler {
	return &EDiscoveryHandler{svc: svc, log: log}
}

func (h *EDiscoveryHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/ediscovery/export", h.export)
	// ADR 0038: per-export integrity verify.
	mux.HandleFunc("POST /api/v1/admin/ediscovery/exports/{id}/verify", h.verify)
}

type ediscoveryExportBody struct {
	CaseID         string   `json:"case_id"`
	CaseName       string   `json:"case_name"`
	CustodianEmail string   `json:"custodian_email"`
	DocumentIDs    []string `json:"document_ids"`
}

func (h *EDiscoveryHandler) export(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	var body ediscoveryExportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ids, err := parseUUIDs(body.DocumentIDs)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("document_ids", err.Error()))
		return
	}

	// Stream ZIP straight to the response; the service method owns
	// the writer, we just wire Content-Disposition.
	safe := sanitizeFilename(body.CaseID)
	filename := fmt.Sprintf("ediscovery-%s-%s.zip", safe, time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))

	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	if _, err := h.svc.ExportForDiscovery(ctx, w, service.DiscoveryExportInput{
		CaseID:         body.CaseID,
		CaseName:       body.CaseName,
		CustodianEmail: body.CustodianEmail,
		DocumentIDs:    ids,
	}); err != nil {
		// Can't change the response code once we've started streaming;
		// log + let the client notice the truncated ZIP.
		h.log.Error().Err(err).Str("case", body.CaseID).Msg("ediscovery export failed")
		return
	}
}

// verify answers POST /admin/ediscovery/exports/{id}/verify. Reads the
// recorded scope_json + manifest_sha256 from ediscovery_exports,
// re-resolves the document set, and reports per-doc / per-manifest
// drift. Strict mode treats missing-doc situations as failures
// (default false because old matters frequently reference docs since
// disposed under retention).
func (h *EDiscoveryHandler) verify(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	strict := r.URL.Query().Get("strict") == "true"

	ctx := auth.WithUser(r.Context(), auth.UserInfo{TenantID: tenantID, ID: userID})
	res, err := h.svc.VerifyEDiscoveryExport(ctx, service.VerifyEDiscoveryExportInput{
		ExportID: id,
		Strict:   strict,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, res)
}

func sanitizeFilename(s string) string {
	// Strip anything that would break a filename header. Worst case
	// an empty case id produces "ediscovery--<ts>.zip".
	out := make([]byte, 0, len(s))
	for _, c := range []byte(s) {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		}
	}
	r := string(out)
	if r == "" {
		return "case"
	}
	return strings.ToLower(r)
}

// Silence unused — kept for the follow-up slice that accepts query
// params instead of JSON body (for presigned-URL use).
var _ = uuid.Nil
