// IRM protected-container export — recipient/creator-facing endpoints (§5/§8).
//
//	POST /api/v1/irm/export             (session; seals + issues licenses)
//	POST /api/v1/irm/licenses/check     (token; the online license-check callback)
//	GET  /api/v1/irm/licenses/content   (token; streams the decrypted payload)
//
// check + content are token-authenticated (the recipient may be external, no
// session); the session cookie, when present, additionally satisfies an
// internal-bound license. The global SessionAuthOptional wrap populates identity
// when a cookie exists, and CorrelationHTTP supplies the client IP for the log.
package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type IRMHandler struct {
	svc *service.IRMService
	log zerolog.Logger
}

func NewIRMHandler(svc *service.IRMService, log zerolog.Logger) *IRMHandler {
	return &IRMHandler{svc: svc, log: log}
}

func (h *IRMHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/irm/export", h.export)
	mux.HandleFunc("POST /api/v1/irm/licenses/check", h.check)
	mux.HandleFunc("GET /api/v1/irm/licenses/content", h.content)
}

// ---- export -------------------------------------------------------------

type irmExportBody struct {
	DocumentID string `json:"document_id"`
	VersionID  string `json:"version_id"`
	Recipients []struct {
		Type string `json:"type"`
		Ref  string `json:"ref"`
	} `json:"recipients"`
	ExpiresAt      string   `json:"expires_at"`
	AllowedActions []string `json:"allowed_actions"`
}

func (h *IRMHandler) export(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var body irmExportBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	docID, err := uuid.Parse(body.DocumentID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("document_id", "invalid uuid"))
		return
	}
	var verID uuid.UUID
	if body.VersionID != "" {
		if verID, err = uuid.Parse(body.VersionID); err != nil {
			writeErr(w, r, vdmserr.Validation("version_id", "invalid uuid"))
			return
		}
	}
	expiresAt, err := time.Parse(time.RFC3339, body.ExpiresAt)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("expires_at", "must be RFC3339"))
		return
	}
	in := service.IRMExportInput{DocumentID: docID, VersionID: verID, ExpiresAt: expiresAt, AllowedActions: body.AllowedActions}
	for _, rc := range body.Recipients {
		in.Recipients = append(in.Recipients, service.IRMRecipientInput{Type: rc.Type, Ref: rc.Ref})
	}
	res, err := h.svc.ExportProtected(r.Context(), in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	lics := make([]map[string]any, 0, len(res.Licenses))
	for _, l := range res.Licenses {
		lics = append(lics, map[string]any{
			"license_id":     l.LicenseID.String(),
			"recipient_type": l.RecipientType,
			"recipient_ref":  l.RecipientRef,
			"open_url":       l.OpenURL,
		})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"container_id":   res.ContainerID.String(),
		"document_title": res.DocumentTitle,
		"licenses":       lics,
	})
}

// ---- license-check callback ---------------------------------------------

type irmCheckBody struct {
	ContainerID  string `json:"container_id"`
	LicenseToken string `json:"license_token"`
}

func (h *IRMHandler) check(w http.ResponseWriter, r *http.Request) {
	var body irmCheckBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "invalid_json"})
		return
	}
	containerID, err := uuid.Parse(body.ContainerID)
	if err != nil {
		writeJSONStatus(w, http.StatusNotFound, map[string]string{"error": "not_found"})
		return
	}
	sessionUserID, _ := auth.GetUserID(r.Context())
	reason, res, err := h.svc.CheckLicense(r.Context(), containerID, body.LicenseToken, sessionUserID, ipHash(r), r.UserAgent())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	if reason != model.LicenseOK {
		writeJSONStatus(w, irmReasonStatus(reason), map[string]string{"error": string(reason)})
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"ok":              true,
		"allowed_actions": res.AllowedActions,
		"expires_at":      res.ExpiresAt.UTC().Format(time.RFC3339),
		"document_title":  res.Title,
		"mime":            res.Mime,
		"sender_email":    res.SenderEmail,
	})
}

// ---- content stream -----------------------------------------------------

func (h *IRMHandler) content(w http.ResponseWriter, r *http.Request) {
	containerID, err := uuid.Parse(r.URL.Query().Get("container_id"))
	if err != nil {
		http.Error(w, "not_found", http.StatusNotFound)
		return
	}
	token := r.URL.Query().Get("lt")
	sessionUserID, _ := auth.GetUserID(r.Context())
	plaintext, mime, reason, err := h.svc.OpenContent(r.Context(), containerID, token, sessionUserID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// Clear the decrypted bytes from process memory once the response is sent
	// (they are per-recipient plaintext).
	defer func() {
		for i := range plaintext {
			plaintext[i] = 0
		}
	}()
	if reason != model.LicenseOK {
		http.Error(w, string(reason), irmReasonStatus(reason))
		return
	}
	w.Header().Set("Content-Type", mime)
	// Same XSS guard as the watermarked download: only render trusted media
	// inline; force attachment for anything else. no-store: per-recipient bytes.
	if inlineSafeMime(mime) {
		w.Header().Set("Content-Disposition", "inline")
	} else {
		w.Header().Set("Content-Disposition", "attachment; filename=\"protected\"")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'")
	w.Header().Set("Cache-Control", "private, no-store, max-age=0")
	w.Header().Set("Content-Length", strconv.Itoa(len(plaintext)))
	_, _ = w.Write(plaintext)
}

// ---- helpers ------------------------------------------------------------

func irmReasonStatus(reason model.LicenseDenyReason) int {
	switch reason {
	case model.LicenseRevoked, model.LicenseExpired:
		return http.StatusGone // 410
	case model.LicenseSessionRequired:
		return http.StatusForbidden // 403
	default:
		return http.StatusNotFound // 404 (not_found / unknown)
	}
}

// ipHash is sha256(clientIP) — the raw IP is never logged.
func ipHash(r *http.Request) string {
	ip := auth.GetClientIP(r.Context())
	if ip == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(ip))
	return hex.EncodeToString(sum[:])
}
