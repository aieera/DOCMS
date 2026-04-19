// Admin HTTP handlers — list/invite/suspend/reset-MFA.
// All routes are mounted at /api/v1/admin/users and wrapped in
// h.AuthMiddleware + h.RequireRole("admin", "owner").
package handler

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/auth/internal/model"
	"github.com/vaultdms/vaultdms/services/auth/internal/service"
)

// ---- GET /api/v1/admin/users ----------------------------------------------

type adminUserDTO struct {
	ID          uuid.UUID  `json:"id"`
	Email       string     `json:"email"`
	DisplayName string     `json:"display_name"`
	Role        string     `json:"role"`
	Status      string     `json:"status"`
	MFAEnabled  bool       `json:"mfa_enabled"`
	CreatedAt   time.Time  `json:"created_at"`
	LastLoginAt *time.Time `json:"last_login_at,omitempty"`
}

type listUsersResponse struct {
	Users      []adminUserDTO `json:"users"`
	NextCursor string         `json:"next_cursor,omitempty"`
}

// ListUsersAdmin handles GET /api/v1/admin/users.
func (h *Handler) ListUsersAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	f := service.ListUsersFilter{
		Status: r.URL.Query().Get("status"),
		Role:   r.URL.Query().Get("role"),
		Q:      r.URL.Query().Get("q"),
		Cursor: r.URL.Query().Get("cursor"),
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := parsePositiveInt(s); err == nil {
			f.Limit = n
		}
	}
	result, err := h.svc.ListUsers(r.Context(), tenantID, f)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := listUsersResponse{
		Users:      make([]adminUserDTO, 0, len(result.Users)),
		NextCursor: result.NextCursor,
	}
	for _, u := range result.Users {
		out.Users = append(out.Users, toAdminUserDTO(u))
	}
	h.writeJSON(w, http.StatusOK, out)
}

// ---- POST /api/v1/admin/users/invite --------------------------------------

type inviteUserRequest struct {
	Email       string `json:"email"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type inviteUserResponse struct {
	User        adminUserDTO `json:"user"`
	InviteToken string       `json:"invite_token"` // plaintext; caller forwards to email template
}

// InviteUserAdmin handles POST /api/v1/admin/users/invite.
func (h *Handler) InviteUserAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req inviteUserRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	u, token, err := h.svc.InviteUser(r.Context(), service.InviteInput{
		Email:       req.Email,
		DisplayName: req.DisplayName,
		Role:        req.Role,
		TenantID:    tenantID,
		InvitedBy:   actorID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, inviteUserResponse{
		User:        toAdminUserDTO(u.ToPublic()),
		InviteToken: token,
	})
}

// ---- POST /api/v1/admin/users/:id/suspend ---------------------------------

// SuspendUserAdmin handles POST /api/v1/admin/users/{id}/suspend.
func (h *Handler) SuspendUserAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	userID, err := parseUserID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.SuspendUser(r.Context(), tenantID, actorID, userID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- POST /api/v1/admin/users/:id/reset-mfa -------------------------------

// ResetUserMFAAdmin handles POST /api/v1/admin/users/{id}/reset-mfa.
func (h *Handler) ResetUserMFAAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	userID, err := parseUserID(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if err := h.svc.ResetUserMFA(r.Context(), tenantID, actorID, userID); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers --------------------------------------------------------------

func parseUserID(r *http.Request) (uuid.UUID, error) {
	raw := chi.URLParam(r, "id")
	id, err := uuid.Parse(raw)
	if err != nil {
		return uuid.Nil, vdmserr.Validation("id", "not a uuid")
	}
	return id, nil
}

func parsePositiveInt(s string) (int, error) {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, vdmserr.Validation("limit", "must be an integer")
		}
		n = n*10 + int(c-'0')
		if n > 1_000_000 {
			return 0, vdmserr.Validation("limit", "too large")
		}
	}
	return n, nil
}

func toAdminUserDTO(u model.PublicView) adminUserDTO {
	return adminUserDTO{
		ID:          u.ID,
		Email:       u.Email,
		DisplayName: u.DisplayName,
		Role:        string(u.Role),
		Status:      string(u.Status),
		MFAEnabled:  u.MFAEnabled,
		CreatedAt:   u.CreatedAt,
		LastLoginAt: u.LastLoginAt,
	}
}
