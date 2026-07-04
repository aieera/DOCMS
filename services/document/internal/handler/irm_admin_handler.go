// IRM protected-export admin API (§5/§8) — the license dashboard.
//
//	GET  /api/v1/admin/irm/containers
//	GET  /api/v1/admin/irm/containers/{id}/licenses
//	POST /api/v1/admin/irm/licenses/{license_id}/revoke
//
// Mounted behind SessionAuth; each handler re-checks owner/admin.
package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type IRMAdminHandler struct {
	svc *service.IRMService
	log zerolog.Logger
}

func NewIRMAdminHandler(svc *service.IRMService, log zerolog.Logger) *IRMAdminHandler {
	return &IRMAdminHandler{svc: svc, log: log}
}

func (h *IRMAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/irm/containers", h.listContainers)
	mux.HandleFunc("GET /api/v1/admin/irm/containers/{id}/licenses", h.listLicenses)
	mux.HandleFunc("POST /api/v1/admin/irm/containers/{id}/revoke", h.revokeContainer)
	mux.HandleFunc("POST /api/v1/admin/irm/licenses/{license_id}/revoke", h.revoke)
}

func (h *IRMAdminHandler) listContainers(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	cs, err := h.svc.ListContainers(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(cs))
	for _, c := range cs {
		out = append(out, map[string]any{
			"id":              c.ID.String(),
			"document_id":     c.DocumentID.String(),
			"document_title":  c.Title,
			"created_at":      c.CreatedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
			"expires_at":      c.ExpiresAt.UTC().Format("2006-01-02T15:04:05.999Z07:00"),
			"recipient_count": c.RecipientCount,
			"revoked":         c.RevokedAt != nil,
		})
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"containers": out})
}

func (h *IRMAdminHandler) listLicenses(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	containerID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ls, err := h.svc.ListLicenses(r.Context(), containerID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(ls))
	for _, l := range ls {
		row := map[string]any{
			"license_id":     l.ID.String(),
			"recipient_type": l.RecipientType,
			"recipient_ref":  l.RecipientRef,
			"open_count":     l.OpenCount,
			"revoked_at":     nil,
			"last_opened_at": nil,
		}
		if l.RevokedAt != nil {
			row["revoked_at"] = l.RevokedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		}
		if l.LastOpenedAt != nil {
			row["last_opened_at"] = l.LastOpenedAt.UTC().Format("2006-01-02T15:04:05.999Z07:00")
		}
		out = append(out, row)
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"licenses": out})
}

func (h *IRMAdminHandler) revokeContainer(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	containerID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.RevokeContainer(r.Context(), containerID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "revoked"})
}

func (h *IRMAdminHandler) revoke(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	if !requireRole(w, r, "owner", "admin") {
		return
	}
	licenseID, err := uuid.Parse(r.PathValue("license_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("license_id", "invalid uuid"))
		return
	}
	if err := h.svc.RevokeLicense(r.Context(), licenseID); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "revoked"})
}
