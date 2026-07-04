// Push-device registration API (ADR 0117 — mobile app).
//
//	POST   /api/v1/notifications/devices        {token, platform?, label?}
//	GET    /api/v1/notifications/devices
//	DELETE /api/v1/notifications/devices/{id}
//
// Same auth surface as the rest of the notification routes: identity
// comes from the SessionAuth/gateway-populated context. Tokens are
// never echoed back in list responses (they're bearer-ish — enough to
// spam a device with pushes).
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/notification/internal/model"
)

// RegisterDevices mounts the device routes.
func (h *Handler) RegisterDevices(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/notifications/devices", h.registerDevice)
	mux.HandleFunc("GET /api/v1/notifications/devices", h.listDevices)
	mux.HandleFunc("DELETE /api/v1/notifications/devices/{id}", h.deleteDevice)
}

type registerDeviceBody struct {
	Token    string `json:"token"`
	Platform string `json:"platform,omitempty"` // default "expo"
	Label    string `json:"label,omitempty"`
}

func (h *Handler) registerDevice(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	var body registerDeviceBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Token == "" {
		writeError(w, http.StatusBadRequest, "token required")
		return
	}
	if len(body.Token) > 512 || len(body.Label) > 200 {
		writeError(w, http.StatusBadRequest, "token or label too long")
		return
	}
	platform := body.Platform
	if platform == "" {
		platform = "expo"
	}
	switch platform {
	case "expo", "fcm", "apns":
	default:
		writeError(w, http.StatusBadRequest, "platform must be expo, fcm or apns")
		return
	}
	d := &model.PushDevice{
		TenantID: tenantID,
		UserID:   userID,
		Platform: platform,
		Token:    body.Token,
		Label:    body.Label,
	}
	if err := h.svc.RegisterPushDevice(r.Context(), d); err != nil {
		h.log.Error().Err(err).Msg("register push device")
		writeError(w, http.StatusInternalServerError, "register failed")
		return
	}
	d.Token = "" // never echo the token back
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handler) listDevices(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	devices, err := h.svc.ListPushDevices(r.Context(), tenantID, userID)
	if err != nil {
		h.log.Error().Err(err).Msg("list push devices")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	if devices == nil {
		devices = []model.PushDevice{}
	}
	for i := range devices {
		devices[i].Token = ""
	}
	writeJSON(w, http.StatusOK, map[string]any{"devices": devices})
}

func (h *Handler) deleteDevice(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusUnauthorized, "authentication required")
		return
	}
	ok, err := h.svc.DeletePushDevice(r.Context(), tenantID, userID, r.PathValue("id"))
	if err != nil {
		h.log.Error().Err(err).Msg("delete push device")
		writeError(w, http.StatusInternalServerError, "delete failed")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "device not found")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
}
