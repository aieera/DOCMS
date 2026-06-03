// workspace_transfer_handler — owner-transfer endpoint.
//
// POST /api/v1/workspaces/{workspace_id}/transfer-ownership
// body: { "new_owner_id": "<uuid>" }
//
// Sits outside the grpc-gateway surface because it's an explicit
// owner-only action that doesn't map cleanly to the proto's
// UpdateWorkspace (which is admin-capability + name/description only).
// Mounted in cmd/server/main.go with SessionAuth + CorrelationHTTP.
package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/service"
)

// WorkspaceTransferHandler exposes the owner-transfer endpoint.
type WorkspaceTransferHandler struct {
	svc *service.DocumentService
}

// NewWorkspaceTransferHandler constructs the handler.
func NewWorkspaceTransferHandler(svc *service.DocumentService) *WorkspaceTransferHandler {
	return &WorkspaceTransferHandler{svc: svc}
}

// Register mounts the route on the supplied mux.
func (h *WorkspaceTransferHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/workspaces/{workspace_id}/transfer-ownership", h.transfer)
}

type transferOwnershipResponse struct {
	WorkspaceID string `json:"workspace_id"`
	CreatedBy   string `json:"created_by"`
}

func (h *WorkspaceTransferHandler) transfer(w http.ResponseWriter, r *http.Request) {
	if _, err := auth.User(r.Context()); err != nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return
	}
	wsID, err := uuid.Parse(r.PathValue("workspace_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
		return
	}

	var body struct {
		NewOwnerID string `json:"new_owner_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid JSON"))
		return
	}
	newOwner, err := uuid.Parse(body.NewOwnerID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("new_owner_id", "not a uuid"))
		return
	}

	updated, err := h.svc.TransferWorkspaceOwnership(r.Context(), &service.TransferWorkspaceOwnershipInput{
		WorkspaceID: wsID,
		NewOwnerID:  newOwner,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(transferOwnershipResponse{
		WorkspaceID: updated.ID.String(),
		CreatedBy:   updated.CreatedBy.String(),
	})
}
