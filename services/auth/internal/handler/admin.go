// Admin HTTP handlers — list/invite/suspend/reset-MFA.
// All routes are mounted at /api/v1/admin/users and wrapped in
// h.AuthMiddleware + h.RequireRole("admin", "owner").
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/services/auth/internal/model"
	"github.com/aieera/sedoc/services/auth/internal/service"
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

// UserDirectory handles GET /api/v1/auth/users/directory — the
// people-picker feed for @mentions and direct shares. Available to any
// authenticated tenant user (unlike ListUsersAdmin); returns only
// active users and only id/display_name/email.
func (h *Handler) UserDirectory(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	f := service.ListUsersFilter{
		Status: "active",
		Q:      r.URL.Query().Get("q"),
		Limit:  200,
	}
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, perr := parsePositiveInt(s); perr == nil && n < f.Limit {
			f.Limit = n
		}
	}
	result, err := h.svc.ListUsers(r.Context(), tenantID, f)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	type dirUser struct {
		ID          string `json:"id"`
		DisplayName string `json:"display_name"`
		Email       string `json:"email"`
	}
	out := struct {
		Users []dirUser `json:"users"`
	}{Users: make([]dirUser, 0, len(result.Users))}
	for _, u := range result.Users {
		out.Users = append(out.Users, dirUser{
			ID:          u.ID.String(),
			DisplayName: u.DisplayName,
			Email:       u.Email,
		})
	}
	h.writeJSON(w, http.StatusOK, out)
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

// ---- GET /api/v1/admin/users/seat-usage ------------------------------------

type seatUsageResponse struct {
	SeatsUsed int  `json:"seats_used"`
	SeatLimit *int `json:"seat_limit,omitempty"` // absent when unlicensed or unlimited plan
}

// SeatUsageAdmin handles GET /api/v1/admin/users/seat-usage — the live
// "seats in use" count for the Admin → License page (ADR 0095 Phase 4).
// Auth owns the users table, so the count is served here rather than by
// the document service's license endpoint, which reports the JWT claims
// but cannot see users. Counts the same active/non-deleted set that
// enforceSeatLimit gates user creation on.
func (h *Handler) SeatUsageAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	used, err := h.svc.SeatUsage(r.Context(), tenantID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	resp := seatUsageResponse{SeatsUsed: used}
	if c := license.Current(); c != nil && c.SeatLimit > 0 {
		resp.SeatLimit = &c.SeatLimit
	}
	h.writeJSON(w, http.StatusOK, resp)
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
	TenantSlug  string       `json:"tenant_slug"`  // so the UI can build the activation link
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
	slug, _ := h.svc.OrgSlugByID(r.Context(), tenantID)
	h.writeJSON(w, http.StatusCreated, inviteUserResponse{
		User:        toAdminUserDTO(u.ToPublic()),
		InviteToken: token,
		TenantSlug:  slug,
	})
}

// ---- POST /api/v1/admin/users ---------------------------------------------

type createUserRequest struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
	Role        string `json:"role"`
}

type createUserResponse struct {
	User adminUserDTO `json:"user"`
}

// CreateUserAdmin handles POST /api/v1/admin/users — sidesteps the
// invite/email round-trip by setting the user's password directly.
func (h *Handler) CreateUserAdmin(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var req createUserRequest
	if err := h.readJSON(r, &req); err != nil {
		h.writeError(w, r, err)
		return
	}
	u, err := h.svc.CreateUserAdmin(r.Context(), service.CreateUserDirectInput{
		Email:       req.Email,
		Password:    req.Password,
		DisplayName: req.DisplayName,
		Role:        req.Role,
		TenantID:    tenantID,
		CreatedBy:   actorID,
	})
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, createUserResponse{
		User: toAdminUserDTO(u.ToPublic()),
	})
}

// ---- PATCH /api/v1/admin/users/:id/role -----------------------------------

type changeRoleBody struct {
	Role string `json:"role"`
}

// ChangeUserRoleAdmin handles PATCH /api/v1/admin/users/{id}/role.
// Body: { "role": "owner" | "admin" | "member" | "viewer" | "compliance_officer" }.
func (h *Handler) ChangeUserRoleAdmin(w http.ResponseWriter, r *http.Request) {
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
	var body changeRoleBody
	if jerr := json.NewDecoder(r.Body).Decode(&body); jerr != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.ChangeUserRole(r.Context(), tenantID, actorID, userID, body.Role); err != nil {
		h.writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
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

// ---- POST /api/v1/admin/users/:id/reactivate ------------------------------

// ReactivateUserAdmin handles POST /api/v1/admin/users/{id}/reactivate.
// Flips status from "suspended" back to "active". Sessions remain revoked
// — the user must sign in again.
func (h *Handler) ReactivateUserAdmin(w http.ResponseWriter, r *http.Request) {
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
	if err := h.svc.ReactivateUser(r.Context(), tenantID, actorID, userID); err != nil {
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
