// shared_with_me_handler — cross-workspace "shared with me" surface.
//
//	GET /api/v1/folders/shared-with-me — every folder in the tenant
//	  that the caller has been granted (directly or via a group) and
//	  does NOT own. Each row carries enough folder + workspace +
//	  grant-provenance metadata for the UI to render the list and
//	  deep-link to /workspaces/{wsId}?folder={folderId}.
//
// SessionAuth populates ctx so the service layer can derive
// (tenantID, userID, userGroups) without out-of-band wiring.
package handler

import (
	"net/http"
	"time"

	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

// SharedWithMeHandler exposes the "shared with me" surface.
type SharedWithMeHandler struct {
	svc *service.DocumentService
}

// NewSharedWithMeHandler constructs the handler.
func NewSharedWithMeHandler(svc *service.DocumentService) *SharedWithMeHandler {
	return &SharedWithMeHandler{svc: svc}
}

// Register mounts the route.
func (h *SharedWithMeHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/folders/shared-with-me", h.list)
}

// sharedFolderEntry is the JSON shape returned to the UI. Flat and
// renderable as a single row without follow-up fetches; matches the
// `SharedFolder` interface in web/src/api/workspaces.ts.
type sharedFolderEntry struct {
	Folder struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		WorkspaceID string `json:"workspace_id"`
		Visibility string `json:"visibility"`
		OwnerID    string `json:"owner_id,omitempty"`
		ParentID   string `json:"parent_folder_id,omitempty"`
		Depth      int    `json:"depth"`
	} `json:"folder"`
	Workspace struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"workspace"`
	GrantedVia string    `json:"granted_via"` // 'user' | 'group'
	GroupID    string    `json:"group_id,omitempty"`
	GrantedAt  time.Time `json:"granted_at"`
}

type sharedListResponse struct {
	Items []sharedFolderEntry `json:"items"`
}

func (h *SharedWithMeHandler) list(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	shared, err := h.svc.ListSharedWithMe(ctx)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	items := make([]sharedFolderEntry, 0, len(shared))
	for i := range shared {
		s := &shared[i]
		var e sharedFolderEntry
		e.Folder.ID = s.Folder.ID.String()
		e.Folder.Name = s.Folder.Name
		e.Folder.WorkspaceID = s.Folder.WorkspaceID.String()
		e.Folder.Visibility = string(s.Folder.Visibility)
		if s.Folder.OwnerID != nil {
			e.Folder.OwnerID = s.Folder.OwnerID.String()
		}
		if s.Folder.ParentFolderID != nil {
			e.Folder.ParentID = s.Folder.ParentFolderID.String()
		}
		e.Folder.Depth = s.Folder.Depth
		e.Workspace.ID = s.Folder.WorkspaceID.String()
		e.Workspace.Name = s.WorkspaceName
		e.GrantedVia = s.GrantedVia
		if s.GroupID != nil {
			e.GroupID = s.GroupID.String()
		}
		e.GrantedAt = s.GrantedAt
		items = append(items, e)
	}
	writeJSONStatus(w, http.StatusOK, sharedListResponse{Items: items})
}
