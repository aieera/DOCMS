// Records-management REST API (paired with services/document/internal/records).
//
// File plan + schedules + declaration + disposition. Writes are gated to
// compliance_officer|admin|owner (records administration is a privileged,
// audited surface); reads to any authenticated tenant user.
//
//	GET    /api/v1/records/schedules
//	POST   /api/v1/records/schedules
//	PATCH  /api/v1/records/schedules/{id}
//	DELETE /api/v1/records/schedules/{id}
//	GET    /api/v1/records/categories            (flat node set; UI builds the tree)
//	POST   /api/v1/records/categories
//	PATCH  /api/v1/records/categories/{id}
//	DELETE /api/v1/records/categories/{id}
//	POST   /api/v1/records/declare               {document_id, category_id}
//	POST   /api/v1/records/{id}/dispose          {certify, reason}
//	GET    /api/v1/records/disposition-queue     ?include_declared=true
//	GET    /api/v1/documents/{id}/record
//	POST   /internal/v1/records/cutoff-sweep     (janitor/cron; proposes dispositions)

package handler

import (
	"net/http"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/document/internal/records"
)

// RecordsHandler mounts the records-management endpoints.
type RecordsHandler struct {
	svc *records.Service
	log zerolog.Logger
}

func NewRecordsHandler(svc *records.Service, log zerolog.Logger) *RecordsHandler {
	return &RecordsHandler{svc: svc, log: log}
}

func (h *RecordsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/records/schedules", h.listSchedules)
	mux.HandleFunc("POST /api/v1/records/schedules", h.createSchedule)
	mux.HandleFunc("PATCH /api/v1/records/schedules/{id}", h.updateSchedule)
	mux.HandleFunc("DELETE /api/v1/records/schedules/{id}", h.deleteSchedule)

	mux.HandleFunc("GET /api/v1/records/categories", h.listCategories)
	mux.HandleFunc("POST /api/v1/records/categories", h.createCategory)
	mux.HandleFunc("PATCH /api/v1/records/categories/{id}", h.updateCategory)
	mux.HandleFunc("DELETE /api/v1/records/categories/{id}", h.deleteCategory)

	mux.HandleFunc("POST /api/v1/records/declare", h.declare)
	mux.HandleFunc("POST /api/v1/records/{id}/dispose", h.dispose)
	mux.HandleFunc("GET /api/v1/records/disposition-queue", h.dispositionQueue)
	mux.HandleFunc("GET /api/v1/documents/{id}/record", h.recordForDocument)

	// E3.2 certification: record gap-closers + standards coverage.
	mux.HandleFunc("POST /api/v1/records/{id}/vital", h.setVital)
	mux.HandleFunc("POST /api/v1/records/{id}/freeze", h.freeze)
	mux.HandleFunc("POST /api/v1/records/{id}/unfreeze", h.unfreeze)
	mux.HandleFunc("PUT /api/v1/records/{id}/metadata", h.setMetadata)
	mux.HandleFunc("GET /api/v1/records/accession-export", h.accessionExport)
	mux.HandleFunc("GET /api/v1/records/standards", h.listStandards)
	mux.HandleFunc("GET /api/v1/records/standards/{sid}/coverage", h.coverage)
}

// RegisterInternal mounts the janitor/cron sweep on the internal mux.
func (h *RecordsHandler) RegisterInternal(mux *http.ServeMux) {
	mux.HandleFunc("POST /internal/v1/records/cutoff-sweep", h.cutoffSweep)
}

var recordsWriteRoles = []string{"compliance_officer", "admin", "owner"}

func (h *RecordsHandler) tenant(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	tenantID, err := auth.GetTenantID(r.Context())
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return uuid.Nil, false
	}
	return tenantID, true
}

// ---- schedules -----------------------------------------------------------

func (h *RecordsHandler) listSchedules(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	out, err := h.svc.ListSchedules(r.Context(), tenantID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"schedules": out})
}

func (h *RecordsHandler) createSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	var in records.ScheduleInput
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.CreateSchedule(r.Context(), tenantID, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}

func (h *RecordsHandler) updateSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var in records.ScheduleInput
	in.RetentionPeriodDays = -1 // sentinel: "unchanged" unless the body sets it
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.UpdateSchedule(r.Context(), tenantID, id, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *RecordsHandler) deleteSchedule(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.DeleteSchedule(r.Context(), tenantID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// ---- categories ----------------------------------------------------------

func (h *RecordsHandler) listCategories(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	out, err := h.svc.ListCategories(r.Context(), tenantID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"categories": out})
}

func (h *RecordsHandler) createCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	var in records.CategoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.CreateCategory(r.Context(), tenantID, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}

func (h *RecordsHandler) updateCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var in records.CategoryInput
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.UpdateCategory(r.Context(), tenantID, id, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *RecordsHandler) deleteCategory(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.DeleteCategory(r.Context(), tenantID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// ---- declare / dispose / queue -------------------------------------------

func (h *RecordsHandler) declare(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	var in records.DeclareInput
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.Declare(r.Context(), tenantID, userID, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, out)
}

func (h *RecordsHandler) dispose(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var in records.DisposeInput
	if !decodeJSON(w, r, &in) {
		return
	}
	out, err := h.svc.Dispose(r.Context(), tenantID, userID, id, in)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *RecordsHandler) dispositionQueue(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	includeDeclared := r.URL.Query().Get("include_declared") == "true"
	out, err := h.svc.DispositionQueue(r.Context(), tenantID, includeDeclared)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"records": out})
}

func (h *RecordsHandler) recordForDocument(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	docID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	out, err := h.svc.GetByDocument(r.Context(), tenantID, docID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	// null record (not declared) is a valid 200 response.
	writeJSONStatus(w, http.StatusOK, map[string]any{"record": out})
}

// ---- E3.2 certification: gap-closers + coverage --------------------------

func (h *RecordsHandler) setVital(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		Vital bool `json:"vital"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.svc.SetVital(r.Context(), tenantID, id, body.Vital); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "ok", "vital_record": body.Vital})
}

func (h *RecordsHandler) freeze(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		Reason string `json:"reason"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	if err := h.svc.Freeze(r.Context(), tenantID, userID, id, body.Reason); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "frozen"})
}

func (h *RecordsHandler) unfreeze(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	if err := h.svc.Unfreeze(r.Context(), tenantID, userID, id); err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"status": "unfrozen"})
}

func (h *RecordsHandler) setMetadata(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		Metadata map[string]any `json:"metadata"`
	}
	if !decodeJSON(w, r, &body) {
		return
	}
	out, err := h.svc.SetMetadata(r.Context(), tenantID, id, body.Metadata)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

func (h *RecordsHandler) accessionExport(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, recordsWriteRoles...) {
		return
	}
	userID, _ := auth.GetUserID(r.Context())
	transferredOnly := r.URL.Query().Get("transferred_only") == "true"
	man, err := h.svc.AccessionExport(r.Context(), tenantID, userID, transferredOnly)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, man)
}

func (h *RecordsHandler) listStandards(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.tenant(w, r); !ok {
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"standards": records.ListStandards()})
}

func (h *RecordsHandler) coverage(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	out, err := h.svc.Coverage(r.Context(), tenantID, r.PathValue("sid"))
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, out)
}

// cutoffSweep is the internal janitor/cron hook: flips declared records past
// their cutoff to cutoff_pending so they surface in the disposition queue.
func (h *RecordsHandler) cutoffSweep(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := h.tenant(w, r)
	if !ok {
		return
	}
	moved, err := h.svc.ProposeDispositionsAtCutoff(r.Context(), tenantID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"proposed": moved})
}
