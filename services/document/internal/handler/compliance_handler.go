// Wave 8 Prompt 8.2 — compliance REST endpoints (legal holds).
//
// Mounted under /api/v1/compliance/... The document service is
// otherwise gRPC-first with a grpc-gateway REST façade; holds are
// expressed directly in the HTTP layer because:
//
//  1. The hold surface is intrinsically tenant-admin facing — no
//     client-library needs it, so the gRPC<->proto ceremony is
//     overhead.
//  2. RFC 4918 status code 423 Locked is part of the DoD; setting it
//     directly here is cleaner than teaching the proto/grpc-gateway
//     layer to emit it.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/document/internal/compliance"
)

// HoldsHandler mounts legal-hold endpoints on a ServeMux.
type HoldsHandler struct {
	svc *compliance.HoldsService
	log zerolog.Logger
}

// NewHoldsHandler constructs the handler.
func NewHoldsHandler(svc *compliance.HoldsService, log zerolog.Logger) *HoldsHandler {
	return &HoldsHandler{svc: svc, log: log}
}

// Register mounts the compliance/holds surface. The §9.3 closure
// (Wave 17) added /custodians, /my, /verify-chain alongside the
// original Wave 8.2 routes.
//
//	POST   /api/v1/compliance/holds
//	GET    /api/v1/compliance/holds
//	GET    /api/v1/compliance/holds/my              (Wave 17 — custodian inbox)
//	GET    /api/v1/compliance/holds/{id}
//	PATCH  /api/v1/compliance/holds/{id}
//	POST   /api/v1/compliance/holds/{id}/release
//	GET    /api/v1/compliance/holds/{id}/custodians
//	POST   /api/v1/compliance/holds/{id}/custodians
//	POST   /api/v1/compliance/holds/{id}/custodians/{user_id}/acknowledge
//	GET    /api/v1/compliance/holds/{id}/events
//	GET    /api/v1/compliance/holds/{id}/verify-chain
func (h *HoldsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/compliance/holds", h.create)
	mux.HandleFunc("GET /api/v1/compliance/holds", h.list)
	// /my must be registered BEFORE /{id} or the path-value pattern
	// will match "my" as an id and 400 on the UUID parse.
	mux.HandleFunc("GET /api/v1/compliance/holds/my", h.listMy)
	mux.HandleFunc("GET /api/v1/compliance/holds/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/compliance/holds/{id}", h.update)
	mux.HandleFunc("POST /api/v1/compliance/holds/{id}/release", h.release)
	mux.HandleFunc("GET /api/v1/compliance/holds/{id}/custodians", h.listCustodians)
	mux.HandleFunc("POST /api/v1/compliance/holds/{id}/custodians", h.addCustodians)
	mux.HandleFunc("POST /api/v1/compliance/holds/{id}/custodians/{user_id}/acknowledge", h.acknowledgeCustodian)
	mux.HandleFunc("GET /api/v1/compliance/holds/{id}/events", h.listEvents)
	mux.HandleFunc("GET /api/v1/compliance/holds/{id}/verify-chain", h.verifyChain)
}

func (h *HoldsHandler) listMy(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	rows, err := h.svc.ListMyHolds(r.Context(), tenantID, userID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"holds": rows})
}

func (h *HoldsHandler) listCustodians(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	out, err := h.svc.ListCustodians(r.Context(), tenantID, holdID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"custodians": out})
}

func (h *HoldsHandler) addCustodians(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body struct {
		UserIDs []string `json:"user_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	parsed := make([]uuid.UUID, 0, len(body.UserIDs))
	for _, s := range body.UserIDs {
		u, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("user_ids", "invalid uuid: "+s))
			return
		}
		parsed = append(parsed, u)
	}
	out, err := h.svc.AddCustodians(r.Context(), tenantID, holdID, userID, parsed)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, map[string]any{"custodians": out})
}

func (h *HoldsHandler) acknowledgeCustodian(w http.ResponseWriter, r *http.Request) {
	tenantID, callerID, ok := callers(w, r)
	if !ok {
		return
	}
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	target, err := uuid.Parse(r.PathValue("user_id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("user_id", "not a uuid"))
		return
	}
	// A custodian may only acknowledge for themselves. The hold owner
	// can list custodians and chase, but never click ack on a user's
	// behalf — ack is a sworn statement.
	if target != callerID {
		writeErr(w, r, vdmserr.Forbidden("custodians may only acknowledge for themselves"))
		return
	}
	c, err := h.svc.AcknowledgeCustodian(r.Context(), tenantID, holdID, callerID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, c)
}

func (h *HoldsHandler) listEvents(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	events, err := h.svc.ListEvents(r.Context(), tenantID, holdID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, map[string]any{"events": events})
}

func (h *HoldsHandler) verifyChain(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	holdID, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	res, err := h.svc.VerifyChain(r.Context(), tenantID, holdID)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, res)
}

func (h *HoldsHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	var body struct {
		Name            string   `json:"name"`
		Description     string   `json:"description"`
		MatterReference string   `json:"matter_reference"`
		DocumentIDs     []string `json:"document_ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	docIDs, err := parseUUIDs(body.DocumentIDs)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("document_ids", err.Error()))
		return
	}
	hold, err := h.svc.Create(r.Context(), tenantID, userID, compliance.CreateInput{
		Name:            body.Name,
		Description:     body.Description,
		MatterReference: body.MatterReference,
		DocumentIDs:     docIDs,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusCreated, hold)
}

func (h *HoldsHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	f := compliance.ListFilter{Status: q.Get("status")}
	if s := q.Get("document_id"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("document_id", "invalid uuid"))
			return
		}
		f.DocumentID = id
	}
	if s := q.Get("custodian"); s != "" {
		id, err := uuid.Parse(s)
		if err != nil {
			writeErr(w, r, vdmserr.Validation("custodian", "invalid uuid"))
			return
		}
		f.Custodian = id
	}
	switch f.Status {
	case "", "active", "released":
	default:
		writeErr(w, r, vdmserr.Validation("status", "must be active or released"))
		return
	}
	holds, err := h.svc.List(r.Context(), tenantID, f)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, holds)
}

func (h *HoldsHandler) get(w http.ResponseWriter, r *http.Request) {
	tenantID, _, ok := callers(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	hold, err := h.svc.Get(r.Context(), tenantID, id)
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, hold)
}

func (h *HoldsHandler) update(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		Name        *string  `json:"name,omitempty"`
		Description *string  `json:"description,omitempty"`
		AddDocs     []string `json:"add_document_ids,omitempty"`
		RemoveDocs  []string `json:"remove_document_ids,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	add, err := parseUUIDs(body.AddDocs)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("add_document_ids", err.Error()))
		return
	}
	rm, err := parseUUIDs(body.RemoveDocs)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("remove_document_ids", err.Error()))
		return
	}
	hold, err := h.svc.Update(r.Context(), tenantID, id, userID, compliance.UpdateInput{
		Name: body.Name, Description: body.Description, AddDocs: add, RemoveDocs: rm,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, hold)
}

func (h *HoldsHandler) release(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return
	}
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeErr(w, r, vdmserr.Validation("id", "invalid uuid"))
		return
	}
	var body struct {
		Reason     string `json:"reason"`
		ApproverID string `json:"approver_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	approver, err := uuid.Parse(body.ApproverID)
	if err != nil {
		writeErr(w, r, vdmserr.Validation("approver_id", "invalid uuid"))
		return
	}
	hold, err := h.svc.Release(r.Context(), tenantID, id, userID, compliance.ReleaseInput{
		Reason: body.Reason, Approver: approver,
	})
	if err != nil {
		writeErr(w, r, err)
		return
	}
	writeJSONStatus(w, http.StatusOK, hold)
}

// ---- helpers --------------------------------------------------------------

// callers extracts tenant + user from the X-Tenant-ID / X-User-ID
// request headers (same pattern as storage_proxy). Returns zeros and
// writes 401 on failure. This is plain HTTP; the grpc-gateway /
// middleware chain for /api/v1/* isn't applied here because the
// compliance routes mount on a dedicated mux.
func callers(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	tenantID, err := uuid.Parse(r.Header.Get("X-Auth-Tenant-ID"))
	if err != nil || tenantID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	userID, err := uuid.Parse(r.Header.Get("X-User-ID"))
	if err != nil || userID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	return tenantID, userID, true
}

// requireRole is the Wave 11.2 OPA gate on mutating compliance
// endpoints. Spec §7.2 says only compliance_officer can
// create/release holds; we also accept org admin / owner since both
// inherit admin capability through policy.rego rule 6.
//
// Reads the role from X-User-Role — the auth service populates this
// header on session-authenticated requests (same header the policy
// service's session auth middleware emits). Missing header → 403.
func requireRole(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	role := r.Header.Get("X-User-Role")
	for _, a := range allowed {
		if role == a {
			return true
		}
	}
	writeErr(w, r, vdmserr.Forbidden("role "+role+" cannot perform this action; required: "+strings.Join(allowed, " or ")))
	return false
}

func parseUUIDs(in []string) ([]uuid.UUID, error) {
	out := make([]uuid.UUID, 0, len(in))
	for _, s := range in {
		id, err := uuid.Parse(s)
		if err != nil {
			return nil, fmt.Errorf("invalid uuid %q", s)
		}
		out = append(out, id)
	}
	return out, nil
}

func writeErr(w http.ResponseWriter, r *http.Request, err error) {
	corr := r.Header.Get("X-Correlation-ID")
	httpErr := vdmserr.ToHTTPError(err, corr)
	// Defense in depth: any non-domain error must not leak a 0 code.
	if httpErr.Code == 0 || httpErr.Code == http.StatusOK {
		var e *vdmserr.Error
		if errors.As(err, &e) {
			httpErr.Code = http.StatusInternalServerError
		} else {
			httpErr.Code = http.StatusInternalServerError
			httpErr.Message = "internal error"
		}
	}
	// 5xx responses get a generic "internal error" body on the wire to
	// avoid leaking internals; without server-side logging that turns
	// every 500 into an undebuggable mystery. Log the underlying error
	// here so the document service log has the actual cause. 4xx is
	// caller-fault and stays quiet.
	if httpErr.Code >= 500 {
		zerolog.Ctx(r.Context()).Error().
			Err(err).
			Str("path", r.URL.Path).
			Str("method", r.Method).
			Str("correlation_id", corr).
			Int("status", httpErr.Code).
			Msg("request failed")
	}
	writeJSONStatus(w, httpErr.Code, httpErr)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
