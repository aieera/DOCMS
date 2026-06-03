// Smart folder HTTP handlers — ADR 0100.
//
// Routes (mounted in handler.go):
//   GET  /api/v1/saved-searches/smart-folders   — list visible smart folders
//   POST /api/v1/saved-searches/{id}/promote    — flip saved search → smart folder
//   POST /api/v1/saved-searches/{id}/demote     — flip back
//
// Workspace membership is read from the optional X-Workspace-IDs
// header (CSV) — the document service forwards it when present.
// Empty membership means workspace-scoped folders are hidden from the
// caller, which is the safe default.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

func (h *Handler) listSmartFolders(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID headers required")
		return
	}
	workspaceIDs := parseCSV(r.Header.Get("X-Workspace-IDs"))
	list, err := h.svc.ListSmartFolders(r.Context(), tenantID, userID, workspaceIDs)
	if err != nil {
		h.log.Error().Err(err).Msg("list smart folders failed")
		writeError(w, http.StatusInternalServerError, "list failed")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type promoteBody struct {
	TreeVisibility string  `json:"tree_visibility"`
	WorkspaceID    *string `json:"workspace_id,omitempty"`
	Icon           string  `json:"icon,omitempty"`
}

func (h *Handler) promoteSmartFolder(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	id := r.PathValue("id")
	if tenantID == "" || userID == "" || id == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID, X-User-ID, id required")
		return
	}
	var body promoteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.TreeVisibility == "" {
		body.TreeVisibility = "private"
	}
	if body.Icon == "" {
		body.Icon = "sparkles"
	}
	// Workspace permission gate: if a workspace_id is supplied, it
	// MUST be in the caller's X-Workspace-IDs list (populated by the
	// gateway from workspace_members for the authenticated user).
	// Without this, a member could pin a smart folder against any
	// workspace UUID they guessed, surfacing in the sidebar of users
	// who actually belong to that workspace.
	if body.WorkspaceID != nil && *body.WorkspaceID != "" {
		memberships := parseCSV(r.Header.Get("X-Workspace-IDs"))
		found := false
		for _, ws := range memberships {
			if ws == *body.WorkspaceID {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusForbidden, "not a member of the target workspace")
			return
		}
	}
	ss, err := h.svc.PromoteSmartFolder(r.Context(),
		tenantID, userID, id, body.TreeVisibility, body.Icon, body.WorkspaceID)
	if err != nil {
		if errors.Is(err, vdmserr.ErrNotFound) {
			writeError(w, http.StatusNotFound, "saved search not found")
			return
		}
		// Validation errors from the service layer come back as plain
		// strings; surface them as 400 so the FE can render the
		// reason without a generic 500.
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, ss)
}

func (h *Handler) demoteSmartFolder(w http.ResponseWriter, r *http.Request) {
	tenantID := r.Header.Get("X-Auth-Tenant-ID")
	userID := r.Header.Get("X-User-ID")
	id := r.PathValue("id")
	if tenantID == "" || userID == "" || id == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID, X-User-ID, id required")
		return
	}
	if err := h.svc.DemoteSmartFolder(r.Context(), tenantID, userID, id); err != nil {
		if errors.Is(err, vdmserr.ErrNotFound) {
			writeError(w, http.StatusNotFound, "saved search not found")
			return
		}
		h.log.Error().Err(err).Msg("demote smart folder failed")
		writeError(w, http.StatusInternalServerError, "demote failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseCSV(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
