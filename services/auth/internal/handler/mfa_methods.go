// ADR 0063 — multi-method MFA HTTP surface.
//
//	Public (post-password, with mfa_session_token):
//	  POST /auth/mfa/methods                        — list enrolled methods
//	  POST /auth/mfa/email/start
//	  POST /auth/mfa/email/verify   {code}
//	  POST /auth/mfa/sms/start
//	  POST /auth/mfa/sms/verify     {code}
//	  POST /auth/mfa/push/start
//	  POST /auth/mfa/push/verify    {challenge_id, ack}
//
//	Authenticated (settings page):
//	  GET    /auth/mfa/methods
//	  POST   /auth/mfa/email/enroll  {email}
//	  POST   /auth/mfa/sms/enroll    {phone}
//	  POST   /auth/mfa/push/devices  {platform, token, label}
//	  DELETE /auth/mfa/methods/{method}
//
//	Admin (owner-only):
//	  GET    /api/v1/admin/mfa/policy
//	  PUT    /api/v1/admin/mfa/policy
package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/notifications"
	"github.com/aieera/sedoc/services/auth/internal/service"
)

// ---- Public, mfa-session-token gated -------------------------------------

type mfaMethodsListBody struct {
	MFASessionToken string `json:"mfa_session_token"`
}

// MFAListMethods is the picker query the login page calls right
// after the password step lands `mfa_required: true`.
func (h *Handler) MFAListMethods(w http.ResponseWriter, r *http.Request) {
	var body mfaMethodsListBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	tenantID, userID, err := h.svc.MFASessionPeek(r.Context(), body.MFASessionToken)
	if err != nil {
		h.writeError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	out, err := h.svc.ListEnrolledMethods(r.Context(), tenantID, userID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"methods": out})
}

type otpStartBody struct {
	MFASessionToken string `json:"mfa_session_token"`
}

type otpVerifyBody struct {
	MFASessionToken string `json:"mfa_session_token"`
	Code            string `json:"code"`
}

func (h *Handler) MFAEmailStart(w http.ResponseWriter, r *http.Request) {
	var body otpStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.StartEmailOTP(r.Context(), body.MFASessionToken); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) MFAEmailVerify(w http.ResponseWriter, r *http.Request) {
	h.verifyOTP(w, r, true)
}

func (h *Handler) MFASMSStart(w http.ResponseWriter, r *http.Request) {
	var body otpStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.StartSMSOTP(r.Context(), body.MFASessionToken); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) MFASMSVerify(w http.ResponseWriter, r *http.Request) {
	h.verifyOTP(w, r, false)
}

// verifyOTP factors the email/sms verify path. `email=true` selects
// the Redis-stored OTP path; otherwise we hit Twilio Verify.
func (h *Handler) verifyOTP(w http.ResponseWriter, r *http.Request, email bool) {
	var body otpVerifyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ip, ua := clientMeta(r)
	var (
		sess *service.CreatedSession
		err  error
	)
	if email {
		sess, err = h.svc.VerifyEmailOTP(r.Context(), body.MFASessionToken, body.Code, ip, ua)
	} else {
		sess, err = h.svc.VerifySMSOTP(r.Context(), body.MFASessionToken, body.Code, ip, ua)
	}
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeSessionResponse(w, sess)
}

type pushStartBody struct {
	MFASessionToken string `json:"mfa_session_token"`
}

func (h *Handler) MFAPushStart(w http.ResponseWriter, r *http.Request) {
	var body pushStartBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ip, ua := clientMeta(r)
	id, err := h.svc.StartPushChallenge(r.Context(), body.MFASessionToken, ip, ua)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]string{"challenge_id": id})
}

type pushVerifyBody struct {
	MFASessionToken string `json:"mfa_session_token"`
	ChallengeID     string `json:"challenge_id"`
	Ack             string `json:"ack"`
}

func (h *Handler) MFAPushVerify(w http.ResponseWriter, r *http.Request) {
	var body pushVerifyBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ip, ua := clientMeta(r)
	sess, err := h.svc.VerifyPushChallenge(r.Context(), body.MFASessionToken, body.ChallengeID, body.Ack, ip, ua)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeSessionResponse(w, sess)
}

// ---- Authenticated enrollment routes -------------------------------------

func (h *Handler) MFAListMine(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out, err := h.svc.ListEnrolledMethods(r.Context(), tenantID, userID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"methods": out})
}

type enrollEmailBody struct {
	Email string `json:"email"`
}

func (h *Handler) MFAEnrollEmail(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var body enrollEmailBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.EnrollEmail(r.Context(), tenantID, userID, body.Email); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type enrollSMSBody struct {
	Phone string `json:"phone"`
}

func (h *Handler) MFAEnrollSMS(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var body enrollSMSBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.EnrollSMS(r.Context(), tenantID, userID, body.Phone); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type registerPushBody struct {
	Platform string `json:"platform"`
	Token    string `json:"token"`
	Label    string `json:"label"`
}

func (h *Handler) MFARegisterPushDevice(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var body registerPushBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	out, err := h.svc.RegisterPushDevice(r.Context(), tenantID, userID, notifications.PushDevice{
		Platform: notifications.PushPlatform(body.Platform),
		Token:    body.Token,
		Label:    body.Label,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, out)
}

func (h *Handler) MFADisableMethod(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	method := strings.ToLower(chi.URLParam(r, "method"))
	if method == "" {
		h.writeError(w, r, vdmserr.Validation("method", "required"))
		return
	}
	if err := h.svc.DisableMethod(r.Context(), tenantID, userID, service.MFAMethod(method)); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Tenant policy (admin) ----------------------------------------------

func (h *Handler) MFAGetPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	p, err := h.svc.LoadMFAPolicy(r.Context(), tenantID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, p)
}

func (h *Handler) MFAPutPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var p service.TenantMFAPolicy
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SaveMFAPolicy(r.Context(), tenantID, userID, p); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// writeSessionResponse mirrors the existing MFAVerify handler:
// drops the session + CSRF cookies and emits the standard
// loginResponse envelope so the frontend can finalize the login.
func (h *Handler) writeSessionResponse(w http.ResponseWriter, sess *service.CreatedSession) {
	h.issueSessionCookies(w, sess.Token, sess.Session.ExpiresAt)
	h.writeJSON(w, http.StatusOK, loginResponse{
		SessionToken: sess.Token,
		ExpiresAt:    &sess.Session.ExpiresAt,
		User:         sess.UserView,
	})
}
