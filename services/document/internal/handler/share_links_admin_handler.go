// Wave 10 — tenant-wide share-link admin surface.
//
//	GET  /api/v1/admin/share-links?status=active  list every link for the tenant
//	POST /api/v1/admin/share-links/{id}/revoke    deactivate one
//	POST /api/v1/admin/documents/{documentId}/share-links/revoke-all
//	                                              deactivate every active link on a doc
//
// The existing grpc-gateway surface at
// POST /api/v1/documents/{id}/share-links (create)
// GET  /api/v1/documents/{id}/share-links (list per doc)
// DELETE /api/v1/share-links/{id}         (delete one)
// stays put — this handler is strictly the admin aggregate view.
//
// Auth: reads X-Tenant-ID / X-User-ID from request headers and
// injects them into the context via pkg/auth setters, so the
// service-layer mustCaller() resolves correctly. Same pattern as
// compliance_handler / privacy_handler.
package handler

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/model"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// ShareLinksAdminHandler mounts tenant-wide admin routes on a
// ServeMux.
type ShareLinksAdminHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

// NewShareLinksAdminHandler constructs the handler.
func NewShareLinksAdminHandler(svc *service.DocumentService, log zerolog.Logger) *ShareLinksAdminHandler {
	return &ShareLinksAdminHandler{svc: svc, log: log}
}

// Register attaches routes.
func (h *ShareLinksAdminHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/admin/share-links", h.list)
	mux.HandleFunc("POST /api/v1/admin/share-links/{id}/revoke", h.revoke)
	mux.HandleFunc("POST /api/v1/admin/documents/{documentId}/share-links/revoke-all", h.revokeAll)
}

type adminShareLinkDTO struct {
	ID                string   `json:"id"`
	DocumentID        string   `json:"document_id"`
	DocumentTitle     string   `json:"document_title,omitempty"`
	PasswordProtected bool     `json:"password_protected"`
	ExpiresAt         *string  `json:"expires_at,omitempty"`
	AccessedAt        *string  `json:"accessed_at,omitempty"`
	MaxViews          int      `json:"max_views"`
	ViewCount         int      `json:"view_count"`
	Permissions       []string `json:"permissions"`
	IsActive          bool     `json:"is_active"`
	CreatedBy         string   `json:"created_by,omitempty"`
	CreatedAt         string   `json:"created_at"`
}

const isoFmt = "2006-01-02T15:04:05Z"

func toAdminShareLinkDTO(l model.ShareLinkAdmin) adminShareLinkDTO {
	var exp, acc *string
	if l.ExpiresAt != nil {
		s := l.ExpiresAt.UTC().Format(isoFmt)
		exp = &s
	}
	if l.AccessedAt != nil {
		s := l.AccessedAt.UTC().Format(isoFmt)
		acc = &s
	}
	return adminShareLinkDTO{
		ID:                l.ID.String(),
		DocumentID:        l.DocumentID.String(),
		DocumentTitle:     l.DocumentTitle,
		PasswordProtected: l.PasswordHash != "",
		ExpiresAt:         exp,
		AccessedAt:        acc,
		MaxViews:          l.MaxViews,
		ViewCount:         l.ViewCount,
		Permissions:       l.Permissions,
		IsActive:          l.IsActive,
		CreatedBy:         l.CreatedBy.String(),
		CreatedAt:         l.CreatedAt.UTC().Format(isoFmt),
	}
}

func (h *ShareLinksAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.enrich(w, r)
	if !ok {
		return
	}
	status := r.URL.Query().Get("status")
	switch status {
	case "", "all", "active":
	default:
		h.writeErr(w, r, vdmserr.Validation("status", "must be active or all"))
		return
	}
	links, err := h.svc.ListShareLinksTenantWide(ctx, status == "active")
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := make([]adminShareLinkDTO, 0, len(links))
	for _, l := range links {
		out = append(out, toAdminShareLinkDTO(l))
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *ShareLinksAdminHandler) revoke(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.enrich(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.DeleteShareLink(ctx, id); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ShareLinksAdminHandler) revokeAll(w http.ResponseWriter, r *http.Request) {
	ctx, ok := h.enrich(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("documentId"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("documentId", "not a uuid"))
		return
	}
	n, err := h.svc.RevokeAllShareLinksForDocument(ctx, docID)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// enrich reads tenant / user headers and returns a new context with
// them attached. Returns a nil context + false and writes 401 on
// missing or malformed headers.
func (h *ShareLinksAdminHandler) enrich(w http.ResponseWriter, r *http.Request) (context.Context, bool) {
	tid, err := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
	if err != nil || tid == uuid.Nil {
		h.writeErr(w, r, vdmserr.ErrUnauthorized)
		return nil, false
	}
	uid, err := uuid.Parse(r.Header.Get("X-User-ID"))
	if err != nil || uid == uuid.Nil {
		h.writeErr(w, r, vdmserr.ErrUnauthorized)
		return nil, false
	}
	// Forward X-User-Role so the OPA Rule 6 (org owner/admin allow)
	// fires. Without this, owner/admin callers get 403 because the
	// service-layer requirePermission asks for "admin" on the
	// tenant-scope workspace and only the role-based rule grants it.
	ctx := auth.WithUser(r.Context(), auth.UserInfo{
		TenantID: tid,
		ID:       uid,
		Role:     r.Header.Get("X-User-Role"),
	})
	return ctx, true
}

func (h *ShareLinksAdminHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *ShareLinksAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	httpErr := vdmserr.ToHTTPError(err, r.Header.Get("X-Correlation-ID"))
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, httpErr.Code, httpErr)
}
