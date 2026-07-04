// Sync delta + device API (§3/§5) for the selective-sync / virtual-drive client.
//
//	GET    /api/v1/sync/delta?workspace_id=&cursor=&limit=
//	POST   /api/v1/sync/devices
//	GET    /api/v1/sync/devices
//	GET    /api/v1/sync/devices/{id}
//	PUT    /api/v1/sync/devices/{id}/cursor
//	PUT    /api/v1/sync/devices/{id}/folders
//	DELETE /api/v1/sync/devices/{id}
//
// Mounted behind SessionOrAPIKey so both the web UI (session) and the headless
// sync agent (Bearer vdms_… key, documents:read/write) can call them.
package handler

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/model"
	"github.com/aieera/sedoc/services/document/internal/service"
)

type SyncHandler struct {
	svc *service.DocumentService
	log zerolog.Logger
}

func NewSyncHandler(svc *service.DocumentService, log zerolog.Logger) *SyncHandler {
	return &SyncHandler{svc: svc, log: log}
}

func (h *SyncHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/sync/delta", h.delta)
	mux.HandleFunc("POST /api/v1/sync/devices", h.register)
	mux.HandleFunc("GET /api/v1/sync/devices", h.list)
	mux.HandleFunc("GET /api/v1/sync/devices/{id}", h.get)
	mux.HandleFunc("PUT /api/v1/sync/devices/{id}/cursor", h.setCursor)
	mux.HandleFunc("PUT /api/v1/sync/devices/{id}/folders", h.setFolders)
	mux.HandleFunc("DELETE /api/v1/sync/devices/{id}", h.revoke)
}

func changeToDTO(c model.SyncChange) map[string]any {
	m := map[string]any{
		"kind":       c.Kind,
		"op":         c.Op,
		"id":         c.ID.String(),
		"name":       c.Name,
		"path":       c.Path,
		"sha256":     c.SHA256,
		"size_bytes": c.SizeBytes,
		"mime":       c.Mime,
		"updated_at": c.UpdatedAt.UTC().Format(time.RFC3339Nano),
	}
	if c.ParentID != nil {
		m["parent_id"] = c.ParentID.String()
	}
	if c.VersionID != nil {
		m["version_id"] = c.VersionID.String()
	}
	return m
}

func (h *SyncHandler) delta(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	q := r.URL.Query()
	var ws *uuid.UUID
	if v := q.Get("workspace_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("workspace_id", "invalid uuid"))
			return
		}
		ws = &id
	}
	var dev *uuid.UUID
	if v := q.Get("device_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("device_id", "invalid uuid"))
			return
		}
		dev = &id
	}
	res, err := h.svc.SyncDelta(r.Context(), ws, dev, q.Get("cursor"), parseInt(q.Get("limit"), 100))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	changes := make([]map[string]any, 0, len(res.Changes))
	for _, c := range res.Changes {
		changes = append(changes, changeToDTO(c))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{
		"changes":     changes,
		"next_cursor": res.NextCursor,
		"has_more":    res.HasMore,
	})
}

func deviceToDTO(d model.SyncDevice) map[string]any {
	m := map[string]any{
		"id":         d.ID.String(),
		"name":       d.Name,
		"platform":   d.Platform,
		"cursor":     d.Cursor,
		"created_at": d.CreatedAt.UTC().Format(time.RFC3339Nano),
		"revoked":    d.RevokedAt != nil,
	}
	if d.WorkspaceID != nil {
		m["workspace_id"] = d.WorkspaceID.String()
	}
	if d.LastSeenAt != nil {
		m["last_seen_at"] = d.LastSeenAt.UTC().Format(time.RFC3339Nano)
	}
	fs := make([]string, 0, len(d.SelectiveFolders))
	for _, f := range d.SelectiveFolders {
		fs = append(fs, f.String())
	}
	m["selective_folders"] = fs
	return m
}

type registerDeviceBody struct {
	Name        string `json:"name"`
	Platform    string `json:"platform"`
	WorkspaceID string `json:"workspace_id"`
}

func (h *SyncHandler) register(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	var body registerDeviceBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	var ws *uuid.UUID
	if body.WorkspaceID != "" {
		id, err := uuid.Parse(body.WorkspaceID)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("workspace_id", "invalid uuid"))
			return
		}
		ws = &id
	}
	d, err := h.svc.RegisterSyncDevice(r.Context(), body.Name, body.Platform, ws)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, deviceToDTO(*d))
}

func (h *SyncHandler) list(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	ds, err := h.svc.ListSyncDevices(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	out := make([]map[string]any, 0, len(ds))
	for _, d := range ds {
		out = append(out, deviceToDTO(d))
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"devices": out})
}

func (h *SyncHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	_ = tenantID
	_ = userID
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	ds, err := h.svc.ListSyncDevices(r.Context())
	if err != nil {
		writeErr(w, r, err)
		return
	}
	for _, d := range ds {
		if d.ID == id {
			writeJSONStatus(w, http.StatusOK, deviceToDTO(d))
			return
		}
	}
	writeErr(w, r, vdmserr.NotFound("device not found"))
}

type cursorBody struct {
	Cursor string `json:"cursor"`
}

func (h *SyncHandler) setCursor(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body cursorBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := h.svc.SaveSyncCursor(r.Context(), id, body.Cursor); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "ok"})
}

type foldersBody struct {
	FolderIDs []string `json:"folder_ids"`
}

func (h *SyncHandler) setFolders(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body foldersBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	folders := make([]uuid.UUID, 0, len(body.FolderIDs))
	for _, f := range body.FolderIDs {
		fid, perr := uuid.Parse(f)
		if perr != nil {
			writeErr(w, r, vdmserr.Validation("folder_ids", "invalid uuid: "+f))
			return
		}
		folders = append(folders, fid)
	}
	if err := h.svc.SetSyncDeviceFolders(r.Context(), id, folders); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "ok"})
}

func (h *SyncHandler) revoke(w http.ResponseWriter, r *http.Request) {
	if _, _, ok := callers(w, r); !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.RevokeSyncDevice(r.Context(), id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "revoked"})
}
