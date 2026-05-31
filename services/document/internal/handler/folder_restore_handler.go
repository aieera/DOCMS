// folder_restore_handler — recycle-bin restore for cascade-deleted
// folders.
//
//	POST /api/v1/folders/{folder_id}/restore
//
// Mounted on a dedicated side mux to avoid a proto regen for what is
// essentially a one-line action. SessionAuth populates ctx; the
// service layer's requirePermission gate ("admin" on the folder)
// rejects unauthorised callers with 403.
//
// FIX-5 (audit Section 11). Mirrors the cascade-delete that lives on
// the gRPC DeleteFolder rpc; both share repository.RestoreSubtree /
// SoftDeleteSubtree by cohort id.
package handler

import (
	"net/http"

	"github.com/google/uuid"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/service"
)

type FolderRestoreHandler struct {
	svc *service.DocumentService
}

func NewFolderRestoreHandler(svc *service.DocumentService) *FolderRestoreHandler {
	return &FolderRestoreHandler{svc: svc}
}

func (h *FolderRestoreHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/folders/{folder_id}/restore", h.restore)
}

func (h *FolderRestoreHandler) restore(w http.ResponseWriter, r *http.Request) {
	ctx, _, _, ok := authedContext(w, r)
	if !ok {
		return
	}
	folderID, err := uuid.Parse(r.PathValue("folder_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("folder_id", "not a uuid"))
		return
	}
	if err := h.svc.RestoreFolder(ctx, folderID); err != nil {
		writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
