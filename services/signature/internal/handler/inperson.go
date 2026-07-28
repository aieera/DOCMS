// ADR 0073 — in-person signing handler.
//
// POST /api/v1/signatures/requests/{id}/in-person/sign
// Body:
//
//	{ "signer_id": "...", "svg_path": "...", "device_kind": "tablet",
//	  "doc_hash_sha256": "..." }
//
// 200 → recorded; 409 with { expected_signer_id } when the caller
// tries to sign out of order so the UI can fast-forward.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/signature/internal/service"
)

func (h *Handler) RegisterInPerson(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/signatures/requests/{id}/in-person/sign", h.inPersonSign)
}

type inPersonBody struct {
	SignerID   string `json:"signer_id"`
	SVGPath    string `json:"svg_path"`
	DeviceKind string `json:"device_kind"`
	DocHashHex string `json:"doc_hash_sha256"`
}

func (h *Handler) inPersonSign(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	reqID := r.PathValue("id")
	if tenantID == "" || reqID == "" {
		writeError(w, http.StatusBadRequest, "tenant + request id required")
		return
	}
	var body inPersonBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.SignerID == "" {
		writeError(w, http.StatusBadRequest, "signer_id required")
		return
	}
	ip := r.RemoteAddr
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		ip = strings.Split(fwd, ",")[0]
	}
	isAdmin := auth.RoleString(r) == "owner" || auth.RoleString(r) == "admin"
	err := h.svc.SignInPerson(r.Context(), service.InPersonSignInput{
		TenantID:        tenantID,
		RequestID:       reqID,
		SignerID:        body.SignerID,
		IPAddress:       ip,
		SVGPath:         body.SVGPath,
		DeviceKind:      body.DeviceKind,
		DocHashHex:      body.DocHashHex,
		OperatorUserID:  auth.UserIDString(r),
		OperatorIsAdmin: isAdmin,
	})
	if err != nil {
		var ooo *service.OutOfOrderError
		if errors.As(err, &ooo) {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error":              "out of order",
				"expected_signer_id": ooo.ExpectedSignerID,
			})
			return
		}
		if strings.Contains(err.Error(), "forbidden") {
			writeError(w, http.StatusForbidden, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "signed"})
}
