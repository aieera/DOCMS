// Self-service profile mutations for the authenticated user
// (ADR 0106 i18n + future tier-0 profile additions).
package handler

import (
	"encoding/json"
	"net/http"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

type updateLocaleRequest struct {
	Locale string `json:"locale"`
}

type updateLocaleResponse struct {
	Locale string `json:"locale"`
}

// UpdateMyLocale handles PATCH /api/v1/auth/me/locale.
//   - Auth: session (chi middleware already requires it on this route).
//   - Body: {"locale": "en" | "ar"}.
//   - 200: {"locale": "ar"}.
//   - 400: invalid or unsupported locale.
//   - 401: unauthenticated (handled by middleware before us).
//
// Why a dedicated endpoint and not a generic PATCH /me?
//   - locale changes happen on a click — the LanguageSelector fires
//     a single field update. A whole-profile PATCH would require
//     the FE to round-trip /me first to avoid stomping fields it
//     didn't mean to write.
//   - The CHECK constraint on `users.locale` is the second gate
//     after the service's SupportedLocales map; both fail closed.
func (h *Handler) UpdateMyLocale(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var body updateLocaleRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	u, err := h.svc.UpdateLocale(r.Context(), tenantID, userID, body.Locale)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, updateLocaleResponse{Locale: u.Locale})
}
