// Package handler exposes REST endpoints for the signature service.
package handler

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/signature/internal/model"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

type Handler struct {
	svc *service.Service
	log zerolog.Logger
}

func New(svc *service.Service, log zerolog.Logger) *Handler {
	return &Handler{svc: svc, log: log}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/signatures/requests", limitBody(jsonBodyLimit, h.createRequest))
	mux.HandleFunc("GET /api/v1/signatures/requests/{id}", h.getRequest)
	mux.HandleFunc("GET /api/v1/signatures/document/{documentId}", h.listByDocument)
	mux.HandleFunc("POST /api/v1/signatures/requests/{id}/sign/{signerId}", limitBody(jsonBodyLimit, h.recordSignature))
	mux.HandleFunc("POST /api/v1/signatures/requests/{id}/cancel", limitBody(jsonBodyLimit, h.cancelRequest))
	mux.HandleFunc("GET /api/v1/signatures/verify/{documentId}", h.verify)
	// ADR 0072 — PAdES-LTV validator. Body is the raw PDF bytes;
	// content-type is application/pdf. Used by the "Re-validate"
	// UX in the signature panel.
	mux.HandleFunc("POST /api/v1/signatures/verify-bytes", limitBody(documentBodyLimit, h.verifyBytes))
}

func (h *Handler) verifyBytes(w http.ResponseWriter, r *http.Request) {
	// MaxBytesReader (limitBody) refuses an oversized PDF with 413; the old
	// io.LimitReader silently truncated it and validated the prefix.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		if isTooLarge(err) {
			writeTooLarge(w)
			return
		}
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
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	var body createBody
	if !decodeJSON(w, r, &body) {
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
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	req, err := h.svc.GetRequest(r.Context(), tenantID, id)
	if err != nil || req == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (h *Handler) listByDocument(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
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
	tenantID := auth.TenantIDString(r)
	reqID := r.PathValue("id")
	signerID := r.PathValue("signerId")
	ip := clientIP(r)
	var body recordSignatureBody
	// Decode best-effort: an empty/absent body is valid for the
	// click-to-sign and typed flows that don't capture biometrics.
	if r.ContentLength > 0 {
		if !decodeJSON(w, r, &body) {
			return
		}
	}
	if err := h.svc.RecordSignature(r.Context(), service.RecordSignatureInput{
		TenantID:  tenantID,
		RequestID: reqID,
		SignerID:  signerID,
		// The per-signer token from the signing URL proves the caller is the
		// signer; the service rejects a missing/mismatched token.
		Token:      r.URL.Query().Get("token"),
		IPAddress:  ip,
		SVGPath:    body.SVGPath,
		DeviceKind: body.DeviceKind,
		DocHashHex: body.DocHashHex,
	}); err != nil {
		h.writeServiceError(w, err, "record signature failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed"})
}

// writeServiceError maps the service's sentinel errors to HTTP statuses.
// 4xx bodies carry the sentinel's message; anything unclassified is logged
// and answered with a generic 500 so driver errors never reach the client.
func (h *Handler) writeServiceError(w http.ResponseWriter, err error, fallback string) {
	switch {
	case errors.Is(err, service.ErrInvalidSigningToken):
		writeError(w, http.StatusUnauthorized, err.Error())
	case errors.Is(err, service.ErrForbidden):
		writeError(w, http.StatusForbidden, err.Error())
	case errors.Is(err, service.ErrRequestNotFound), errors.Is(err, service.ErrSignerNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, service.ErrRequestNotSignable), errors.Is(err, service.ErrAlreadySigned):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrInvalidRequest):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, service.ErrUpstream):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		h.log.Error().Err(err).Msg(fallback)
		writeError(w, http.StatusInternalServerError, fallback)
	}
}

func (h *Handler) cancelRequest(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	id := r.PathValue("id")
	isAdmin := auth.RoleString(r) == "owner" || auth.RoleString(r) == "admin"
	if err := h.svc.CancelRequest(r.Context(), tenantID, id, auth.UserIDString(r), isAdmin); err != nil {
		h.writeServiceError(w, err, "cancel failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "cancelled"})
}

func (h *Handler) verify(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
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

// clientIP extracts a BARE IP for the audit columns, which are Postgres
// `inet` — that type rejects "host:port", so writing r.RemoteAddr
// verbatim ("172.18.0.1:59222") failed every in-person signature with
// a 500 (SQLSTATE 22P02). Prefers the left-most X-Forwarded-For hop
// when present (we sit behind Kong / the dev proxy). Returns "" when
// nothing parses, and the service layer stores NULL rather than junk.
func clientIP(r *http.Request) string {
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		cand := strings.TrimSpace(strings.Split(fwd, ",")[0])
		if ip := net.ParseIP(cand); ip != nil {
			return ip.String()
		}
		// XFF hop may itself carry a port.
		if host, _, err := net.SplitHostPort(cand); err == nil {
			if ip := net.ParseIP(host); ip != nil {
				return ip.String()
			}
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		if ip := net.ParseIP(host); ip != nil {
			return ip.String()
		}
	}
	if ip := net.ParseIP(strings.TrimSpace(r.RemoteAddr)); ip != nil {
		return ip.String()
	}
	return ""
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
