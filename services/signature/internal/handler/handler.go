// Package handler exposes REST endpoints for the signature service.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/services/signature/internal/model"
	"github.com/vaultdms/vaultdms/services/signature/internal/service"
)

type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/signatures/requests", h.createRequest)
	mux.HandleFunc("GET /api/v1/signatures/requests/{id}", h.getRequest)
	mux.HandleFunc("GET /api/v1/signatures/document/{documentId}", h.listByDocument)
	mux.HandleFunc("POST /api/v1/signatures/requests/{id}/sign/{signerId}", h.recordSignature)
	mux.HandleFunc("POST /api/v1/signatures/requests/{id}/cancel", h.cancelRequest)
	mux.HandleFunc("GET /api/v1/signatures/verify/{documentId}", h.verify)
	// ADR 0072 — PAdES-LTV validator. Body is the raw PDF bytes;
	// content-type is application/pdf. Used by the "Re-validate"
	// UX in the signature panel.
	mux.HandleFunc("POST /api/v1/signatures/verify-bytes", h.verifyBytes)
}

func (h *Handler) verifyBytes(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 50<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "read body")
		return
	}
	rep, err := h.svc.VerifyPDF(r.Context(), body)
	if err != nil && rep == nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

type createBody struct {
	DocumentID  string         `json:"document_id"`
	VersionID   string         `json:"version_id"`
	Provider    string         `json:"provider"`
	SigningMode string         `json:"signing_mode,omitempty"`
	Signers     []model.Signer `json:"signers"`
}

func (h *Handler) createRequest(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	var body createBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Provider == "" {
		body.Provider = "internal"
	}
	if strings.TrimSpace(body.DocumentID) == "" {
		writeError(w, http.StatusBadRequest, "document_id is required")
		return
	}
	if strings.TrimSpace(body.VersionID) == "" {
		writeError(w, http.StatusBadRequest, "version_id is required")
		return
	}
	if len(body.Signers) == 0 {
		writeError(w, http.StatusBadRequest, "at least one signer is required")
		return
	}
	req, err := h.svc.CreateRequest(r.Context(), tenantID, body.DocumentID, body.VersionID, userID, body.Provider, body.SigningMode, body.Signers)
	if err != nil {
		// 23503 = foreign_key_violation. The signature_requests table
		// FKs onto (tenant_id, version_id) and (tenant_id, document_id);
		// either pointing at a row that doesn't exist is a client error,
		// not an internal one. Surface it as 400 so the user sees what
		// they sent that was wrong instead of a generic 500.
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			h.log.Warn().Err(err).Msg("create signature request: fk violation")
			writeError(w, http.StatusBadRequest, "document_id or version_id does not exist for this tenant")
			return
		}
		h.log.Error().Err(err).Msg("create signature request")
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, req)
}

func (h *Handler) getRequest(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	req, err := h.svc.GetRequest(r.Context(), tenantID, id)
	if err != nil || req == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *Handler) listByDocument(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	docID := r.PathValue("documentId")
	reqs, err := h.svc.ListByDocument(r.Context(), tenantID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, reqs)
}

// recordSignatureBody carries the optional ADR 0073 evidence fields.
// All fields are optional; callers that pre-date ADR 0073 (the
// click-to-sign + typed flows) send an empty body and the recorded
// row keeps signature_svg_path / device_kind / signed_doc_hash NULL.
type recordSignatureBody struct {
	SVGPath    string `json:"svg_path,omitempty"`
	DeviceKind string `json:"device_kind,omitempty"`
	DocHashHex string `json:"doc_hash_sha256,omitempty"`
}

func (h *Handler) recordSignature(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	reqID := r.PathValue("id")
	signerID := r.PathValue("signerId")
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
	}
	var body recordSignatureBody
	// Decode best-effort: an empty/absent body is valid for the
	// click-to-sign and typed flows that don't capture biometrics.
	if r.ContentLength > 0 {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, "invalid json")
			return
		}
	}
	if err := h.svc.RecordSignature(r.Context(), service.RecordSignatureInput{
		TenantID:   tenantID,
		RequestID:  reqID,
		SignerID:   signerID,
		IPAddress:  ip,
		SVGPath:    body.SVGPath,
		DeviceKind: body.DeviceKind,
		DocHashHex: body.DocHashHex,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed"})
}

func (h *Handler) cancelRequest(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	id := r.PathValue("id")
	if err := h.svc.CancelRequest(r.Context(), tenantID, id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	docID := r.PathValue("documentId")
	result, err := h.svc.Verify(r.Context(), tenantID, docID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
