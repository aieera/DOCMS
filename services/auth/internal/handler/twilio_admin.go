// Admin endpoints for per-tenant Twilio (SMS MFA) credentials.
// Mounted at /api/v1/admin/notifications/twilio. Same shape as the
// eSign per-tenant config endpoints — secret never travels back to
// the browser; GET returns has_token as a presence flag only.
package handler

import (
	"net/http"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

type putTwilioBody struct {
	AccountSID       string `json:"account_sid"`
	AuthToken        string `json:"auth_token"`
	VerifyServiceSID string `json:"verify_service_sid"`
}

type testTwilioBody struct {
	PhoneE164 string `json:"phone_e164"`
}

func (h *Handler) GetTwilioConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	cfg, err := h.svc.GetTwilioConfigForAdmin(r.Context(), tenantID.String())
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

func (h *Handler) PutTwilioConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var b putTwilioBody
	if err := h.readJSON(r, &b); err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.SaveTwilioConfig(r.Context(), tenantID.String(), userID.String(),
		b.AccountSID, b.AuthToken, b.VerifyServiceSID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) DeleteTwilioConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.DeleteTwilioConfig(r.Context(), tenantID.String()); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// TestTwilioConfig fires a real Twilio Verify start against the saved
// row (or the body's creds if provided) and reports the vendor's
// response. Used by the admin modal's "Send test SMS" button.
func (h *Handler) TestTwilioConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var b testTwilioBody
	if err := h.readJSON(r, &b); err != nil {
		h.writeError(w, r, err)
		return
	}
	if b.PhoneE164 == "" {
		h.writeError(w, r, vdmserr.Validation("phone_e164", "required"))
		return
	}
	if err := h.svc.TestTwilioSend(r.Context(), tenantID.String(), b.PhoneE164); err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})
}
