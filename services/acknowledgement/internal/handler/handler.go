// Package handler exposes the acknowledgement service's HTTP surface.
// Routes land under /api/v1/acknowledgement/*. Callers mount via
// Handler.Register on a chi router (auth + tenant middleware applied
// upstream at cmd/server/main.go).
package handler

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/model"
	"github.com/vaultdms/vaultdms/services/acknowledgement/internal/service"
)

// Handler is the REST adapter.
type Handler struct {
	svc *service.Service
}

// New constructs a Handler.
func New(svc *service.Service) *Handler { return &Handler{svc: svc} }

// RegisterInternal mounts worker-only endpoints (no session cookie).
// Callers wrap with middleware.RequireGatewaySignature + TenantHTTP
// only — these routes run inside the trust boundary and are called
// by the Temporal worker with a synthesised tenant header.
func (h *Handler) RegisterInternal(r chi.Router) {
	r.Post("/internal/v1/acknowledgement/sweep-reminders", h.SweepReminders)
}

// RegisterPublic mounts user-facing endpoints. Callers wrap with
// SessionAuth upstream so `auth.User(ctx)` resolves; TenantHTTP is
// redundant because SessionAuth populates tenant from the session
// row, but keeping it costs nothing.
func (h *Handler) RegisterPublic(r chi.Router) {
	r.Route("/api/v1/acknowledgement", func(r chi.Router) {
		r.Route("/campaigns", func(r chi.Router) {
			r.Post("/", h.CreateCampaign)
			r.Get("/", h.ListCampaigns)
			r.Get("/{id}", h.GetCampaign)
			r.Post("/{id}/close", h.CloseCampaign)
			r.Get("/{id}/assignments", h.ListAssignments)
			r.Get("/{id}/report", h.Report)
		})
		r.Get("/my", h.MyPending)
		r.Post("/assignments/{id}/ack", h.Acknowledge)
	})
}

// Register is the legacy single-call wrapper. Kept for backward
// compatibility with tests; new services should call
// RegisterInternal + RegisterPublic explicitly.
func (h *Handler) Register(r chi.Router) {
	h.RegisterInternal(r)
	h.RegisterPublic(r)
}

// ---- create ---------------------------------------------------------------

type createReq struct {
	DocumentID      string                 `json:"document_id"`
	VersionID       string                 `json:"version_id"`
	Title           string                 `json:"title"`
	BodyMD          string                 `json:"body_md"`
	DueAt           time.Time              `json:"due_at"`
	RecipientPolicy model.RecipientPolicy  `json:"recipient_policy"`
	Activate        bool                   `json:"activate"`
}

// CreateCampaign handles POST /acknowledgement/campaigns. Caller
// role is checked upstream by the OPA-backed permission middleware;
// this handler is already behind RequireRole("compliance_officer","admin","owner")
// at mount time.
func (h *Handler) CreateCampaign(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	var body createReq
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, vdmserr.Validation("body", "invalid json"))
		return
	}
	docID, err := uuid.Parse(body.DocumentID)
	if err != nil {
		writeErr(w, vdmserr.Validation("document_id", "not a uuid"))
		return
	}
	in := service.CreateCampaignInput{
		TenantID:        u.TenantID,
		ActorID:         u.ID,
		DocumentID:      docID,
		Title:           strings.TrimSpace(body.Title),
		BodyMD:          body.BodyMD,
		DueAt:           body.DueAt,
		RecipientPolicy: body.RecipientPolicy,
		Activate:        body.Activate,
	}
	if body.VersionID != "" {
		vid, err := uuid.Parse(body.VersionID)
		if err != nil {
			writeErr(w, vdmserr.Validation("version_id", "not a uuid"))
			return
		}
		in.VersionID = &vid
	}
	c, _, err := h.svc.CreateCampaign(r.Context(), in)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, campaignDTO(*c))
}

// ---- list / get -----------------------------------------------------------

func (h *Handler) ListCampaigns(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	status := model.Status(r.URL.Query().Get("status"))
	list, err := h.svc.ListCampaigns(r.Context(), u.TenantID, status)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, c := range list {
		out = append(out, campaignDTO(c))
	}
	writeJSON(w, http.StatusOK, map[string]any{"campaigns": out})
}

func (h *Handler) GetCampaign(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	c, err := h.svc.GetCampaign(r.Context(), u.TenantID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, campaignDTO(*c))
}

func (h *Handler) CloseCampaign(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.CloseCampaign(r.Context(), u.TenantID, u.ID, id); err != nil {
		writeErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) ListAssignments(w http.ResponseWriter, r *http.Request) {
	// Implementation lives in the service via repo.ListAssignmentsByCampaign,
	// but the public surface is deferred to avoid exposing PII
	// (ip_address, user_agent) unless the caller is admin/compliance.
	// Tracked in WAVE_15_PROGRESS.md.
	writeErr(w, vdmserr.Forbidden("list-assignments deferred"))
}

// ---- my inbox -------------------------------------------------------------

func (h *Handler) MyPending(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	list, err := h.svc.MyPending(r.Context(), u.TenantID, u.ID)
	if err != nil {
		writeErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, a := range list {
		out = append(out, assignmentDTO(a))
	}
	writeJSON(w, http.StatusOK, map[string]any{"pending": out})
}

// ---- acknowledge ----------------------------------------------------------

type ackReq struct {
	Comment string `json:"comment"`
}

func (h *Handler) Acknowledge(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body ackReq
	_ = json.NewDecoder(r.Body).Decode(&body) // body is optional
	ip, ua := clientMeta(r)
	a, err := h.svc.Acknowledge(r.Context(), service.AcknowledgeInput{
		TenantID:     u.TenantID,
		ActorID:      u.ID,
		AssignmentID: id,
		IPAddress:    ip,
		UserAgent:    ua,
		Comment:      body.Comment,
	})
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, assignmentDTO(*a))
}

// ---- reminders sweep (internal / Temporal worker) ------------------------

// SweepReminders is invoked by the workflow worker's daily schedule.
// Tenant is resolved from the upstream TenantHTTP middleware; this
// endpoint has NO interactive auth — it assumes the worker runs
// inside the trust boundary, matches pkg/database/outbox_publisher
// pattern.
func (h *Handler) SweepReminders(w http.ResponseWriter, r *http.Request) {
	tid, err := auth.GetTenantID(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	res, err := h.svc.SweepReminders(r.Context(), tid)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"reminded":  res.Reminded,
		"escalated": res.Escalated,
	})
}

// ---- report ---------------------------------------------------------------

func (h *Handler) Report(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, vdmserr.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, vdmserr.Validation("id", "not a uuid"))
		return
	}
	rep, err := h.svc.Report(r.Context(), u.TenantID, id)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, rep)
}

// ---- DTO helpers ----------------------------------------------------------

func campaignDTO(c model.Campaign) map[string]any {
	out := map[string]any{
		"id":         c.ID.String(),
		"tenant_id":  c.TenantID.String(),
		"document_id": c.DocumentID.String(),
		"title":      c.Title,
		"body_md":    c.BodyMD,
		"due_at":     c.DueAt,
		"status":     string(c.Status),
		"created_by": c.CreatedByUserID.String(),
		"created_at": c.CreatedAt,
		"updated_at": c.UpdatedAt,
	}
	if c.VersionID != nil {
		out["version_id"] = c.VersionID.String()
	}
	if c.ClosedAt != nil {
		out["closed_at"] = c.ClosedAt
	}
	return out
}

// assignmentDTO redacts ip_address + attestation_hash on the wire
// (both PII-adjacent). Full detail is only exposed via the audit
// export path, which is gated separately.
func assignmentDTO(a model.Assignment) map[string]any {
	out := map[string]any{
		"id":                a.ID.String(),
		"campaign_id":       a.CampaignID.String(),
		"assignee_user_id":  a.AssigneeUserID.String(),
		"assigned_at":       a.AssignedAt,
		"reminded_count":    a.RemindedCount,
	}
	if a.AcknowledgedAt != nil {
		out["acknowledged_at"] = a.AcknowledgedAt
	}
	if a.RemindedAt != nil {
		out["reminded_at"] = a.RemindedAt
	}
	if a.EscalatedAt != nil {
		out["escalated_at"] = a.EscalatedAt
	}
	return out
}

// ---- error/json helpers ---------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, err error) {
	var verr *vdmserr.Error
	if !errors.As(err, &verr) {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"error": map[string]any{"code": "INTERNAL", "message": "internal error"}})
		return
	}
	code := http.StatusInternalServerError
	switch verr.Kind {
	case vdmserr.KindValidation:
		code = http.StatusBadRequest
	case vdmserr.KindUnauthorized:
		code = http.StatusUnauthorized
	case vdmserr.KindForbidden:
		code = http.StatusForbidden
	case vdmserr.KindNotFound:
		code = http.StatusNotFound
	case vdmserr.KindConflict, vdmserr.KindAlreadyExists:
		code = http.StatusConflict
	}
	writeJSON(w, code, map[string]any{"error": map[string]any{"code": verr.Code, "message": verr.Message}})
}

// clientMeta returns (ip, user-agent). Matches the pattern used by
// the auth service's handler.go#clientMeta.
func clientMeta(r *http.Request) (string, string) {
	ip := ""
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Right-most hop after trusted proxy.
		parts := strings.Split(xff, ",")
		ip = strings.TrimSpace(parts[len(parts)-1])
	}
	if ip == "" {
		if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
			ip = host
		} else {
			ip = r.RemoteAddr
		}
	}
	return ip, r.UserAgent()
}
