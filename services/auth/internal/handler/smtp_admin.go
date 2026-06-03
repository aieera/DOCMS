// Admin endpoints for per-tenant SMTP credentials (transactional +
// email OTP). Mounted at /api/v1/admin/notifications/smtp.
package handler

import (
	"net/http"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

type putSMTPBody struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	FromAddr string `json:"from_addr"`
	StartTLS bool   `json:"starttls"`
}

type testSMTPBody struct {
	To string `json:"to"`
}

func (h *Handler) GetSMTPConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	cfg, err := h.svc.GetSMTPConfigForAdmin(r.Context(), tenantID.String())
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if cfg == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	h.writeJSON(w, http.StatusOK, cfg)
}

func (h *Handler) PutSMTPConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var b putSMTPBody
	if err := h.readJSON(r, &b); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.SaveSMTPConfig(r.Context(), tenantID.String(), userID.String(),
		b.Host, b.Port, b.Username, b.Password, b.FromAddr, b.StartTLS); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteSMTPConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.DeleteSMTPConfig(r.Context(), tenantID.String()); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) TestSMTPConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var b testSMTPBody
	if err := h.readJSON(r, &b); err != nil {
		h.writeError(w, r, err)
		return
	}
	if b.To == "" {
		h.writeError(w, r, vdmserr.Validation("to", "required"))
		return
	}
	if err := h.svc.TestSMTPSend(r.Context(), tenantID.String(), b.To); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
