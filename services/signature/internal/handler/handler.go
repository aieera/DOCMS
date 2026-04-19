// Package handler exposes REST endpoints for the signature service.
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

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
}

type createBody struct {
	DocumentID string         `json:"document_id"`
	VersionID  string         `json:"version_id"`
	Provider   string         `json:"provider"`
	Signers    []model.Signer `json:"signers"`
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
	req, err := h.svc.CreateRequest(r.Context(), tenantID, body.DocumentID, body.VersionID, userID, body.Provider, body.Signers)
	if err != nil {
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

func (h *Handler) recordSignature(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	reqID := r.PathValue("id")
	signerID := r.PathValue("signerId")
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
	}
	if err := h.svc.RecordSignature(r.Context(), tenantID, reqID, signerID, ip); err != nil {
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
