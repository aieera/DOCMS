// workspace_members_handler — per-workspace Members surface.
//
//	GET    /api/v1/workspaces/{workspace_id}/members
//	POST   /api/v1/workspaces/{workspace_id}/members            { user_id, role }
//	PATCH  /api/v1/workspaces/{workspace_id}/members/{user_id}  { role }
//	DELETE /api/v1/workspaces/{workspace_id}/members/{user_id}
//
// Authorization happens in the service layer:
//   - List is permitted for any workspace member (so they can see who
//     else has access) and 403s non-members.
//   - Add / update-role / remove require tenant owner|admin OR the
//     workspace's created_by (the canonical "owner" in our single-
//     owner model). Creator can't be demoted or removed without first
//     transferring ownership.
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type WorkspaceMembersHandler struct {
	svc *service.DocumentService
}

func NewWorkspaceMembersHandler(svc *service.DocumentService) *WorkspaceMembersHandler {
	return &WorkspaceMembersHandler{svc: svc}
}

func (h *WorkspaceMembersHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/workspaces/{workspace_id}/members", h.list)
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/members", h.add)
	mux.HandleFunc("PATCH /api/v1/workspaces/{workspace_id}/members/{user_id}", h.updateRole)
	mux.HandleFunc("DELETE /api/v1/workspaces/{workspace_id}/members/{user_id}", h.remove)
}

type memberEntry struct {
	UserID      string    `json:"user_id"`
	Email       string    `json:"email"`
	DisplayName string    `json:"display_name,omitempty"`
	Role        string    `json:"role"`
	AddedBy     string    `json:"added_by,omitempty"`
	AddedAt     time.Time `json:"added_at"`
}

type memberListResponse struct {
	Items []memberEntry `json:"items"`
}

func (h *WorkspaceMembersHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}
	members, err := h.svc.ListWorkspaceMembers(ctx, wsID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]memberEntry, 0, len(members))
	for i := range members {
		m := &members[i]
		e := memberEntry{
			UserID:      m.UserID.String(),
			Email:       m.Email,
			DisplayName: m.DisplayName,
			Role:        m.Role,
			AddedAt:     m.AddedAt,
		}
		if m.AddedBy != nil {
			e.AddedBy = m.AddedBy.String()
		}
		out = append(out, e)
	}
	writeJSONStatus(w, http.StatusOK, memberListResponse{Items: out})
}

type addMemberBody struct {
	UserID string `json:"user_id"`
	Role   string `json:"role,omitempty"`
}

func (h *WorkspaceMembersHandler) add(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}
	var body addMemberBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	userID, err := uuid.Parse(body.UserID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("user_id", "not a uuid"))
		return
	}
	if err := h.svc.AddWorkspaceMember(ctx, &service.AddWorkspaceMemberInput{
		WorkspaceID: wsID,
		UserID:      userID,
		Role:        body.Role,
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type updateRoleBody struct {
	Role string `json:"role"`
}

func (h *WorkspaceMembersHandler) updateRole(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("user_id", "not a uuid"))
		return
	}
	var body updateRoleBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.UpdateWorkspaceMemberRole(ctx, &service.UpdateWorkspaceMemberRoleInput{
		WorkspaceID: wsID,
		UserID:      userID,
		Role:        body.Role,
	}); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *WorkspaceMembersHandler) remove(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}
	userID, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("user_id", "not a uuid"))
		return
	}
	if err := h.svc.RemoveWorkspaceMember(ctx, wsID, userID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
