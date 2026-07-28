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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	zlog "github.com/rs/zerolog/log"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	vdmsmw "github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/document/internal/compliance"
)

// HoldsHandler mounts legal-hold endpoints on a ServeMux.
type HoldsHandler struct {
	svc  *compliance.HoldsService
	pool *pgxpool.Pool // nil disables ADR 0061 step-up gate on /release
	log  zerolog.Logger
}

// NewHoldsHandler constructs the handler.
func NewHoldsHandler(svc *compliance.HoldsService, log zerolog.Logger) *HoldsHandler {
	return &HoldsHandler{svc: svc, log: log}
}

// SetStepUpPool wires the DB pool used by the ADR 0061 step-up
// check on /release. main.go calls this once at startup. When pool
// is nil (e.g. dev deploy without WebAuthn configured), the
// endpoint works as before — role gate only.
func (h *HoldsHandler) SetStepUpPool(p *pgxpool.Pool) { h.pool = p }

// Register mounts:
//
//	POST   /api/v1/compliance/holds
//	GET    /api/v1/compliance/holds
//	GET    /api/v1/compliance/holds/{id}
//	PATCH  /api/v1/compliance/holds/{id}
//	POST   /api/v1/compliance/holds/{id}/release
func (h *HoldsHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/compliance/holds", h.create)
	mux.HandleFunc("GET /api/v1/compliance/holds", h.list)
	mux.HandleFunc("GET /api/v1/compliance/holds/{id}", h.get)
	mux.HandleFunc("PATCH /api/v1/compliance/holds/{id}", h.update)
	mux.HandleFunc("POST /api/v1/compliance/holds/{id}/release", h.release)
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
	// Legal-hold existence + matter references are privileged e-discovery info
	// (knowing a doc is under hold can tip off a custodian; matter refs leak
	// active-litigation identifiers). Gate reads to the same roles as the
	// mutating endpoints.
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
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
	if !requireRole(w, r, "compliance_officer", "admin", "owner") {
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
	// ADR 0061 — releasing a legal hold is irreversible + auditable;
	// require fresh passkey presence within the last 5 minutes. When
	// the deploy hasn't wired WebAuthn (pool nil), the legacy role
	// check is the only gate — a deploy-time choice the runbook
	// documents. Uses EnforceStepUpExplicit because the document
	// service's `callers()` helper reads identity from headers
	// directly, not from pkg/auth's request context.
	if h.pool != nil && !vdmsmw.EnforceStepUpExplicit(w, r, h.pool, tenantID, userID, "legal_hold:release", h.log) {
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

// callers extracts tenant + user from the authenticated context that
// pkg/middleware.SessionAuth populated by validating the session cookie
// against the DB. Returns zeros and writes 401 on failure.
//
// Security note (FIX-1, 2026-05-31): a previous version of this helper
// read uuid values from inbound X-Auth-Tenant-ID / X-User-ID request
// headers, which were *not* set by Kong upstream — they were always
// client-controllable, giving any caller free choice of tenant + user
// at every handler reachable via these helpers. The audit logged this
// as a CISO-blocker (cross-tenant takeover + trash-purge IDOR). The
// fix is to read exclusively from the request context, which
// SessionAuth populated from the authoritative `sessions` table. The
// mux wiring in cmd/server/main.go must therefore guarantee that
// every mux using this helper is wrapped with SessionAuth (or
// SessionAuthOptional + a no-anonymous policy at the handler level).
func callers(w http.ResponseWriter, r *http.Request) (uuid.UUID, uuid.UUID, bool) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	if u.TenantID == uuid.Nil || u.ID == uuid.Nil {
		writeErr(w, r, vdmserr.ErrUnauthorized)
		return uuid.Nil, uuid.Nil, false
	}
	return u.TenantID, u.ID, true
}

// authedContext returns the SessionAuth-populated request context plus
// the resolved (tenantID, userID). The ctx already carries the trusted
// auth.UserInfo (including role + groups) so downstream service-layer
// calls — mustCaller, requireDocPermission, OPA input.context — see
// the right values without any header dependency.
//
// Behavior change from FIX-1: this no longer re-creates the ctx from
// inbound headers. The role placed on ctx is now the DB-trusted
// `users.role` value SessionAuth wrote there, not the attacker-
// controllable X-User-Role.
func authedContext(w http.ResponseWriter, r *http.Request) (context.Context, uuid.UUID, uuid.UUID, bool) {
	tenantID, userID, ok := callers(w, r)
	if !ok {
		return nil, uuid.Nil, uuid.Nil, false
	}
	return r.Context(), tenantID, userID, true
}

// requireRole gates mutating endpoints (legal-hold create/release,
// trash purge, retention policy edit, etc.) to the listed roles. Role
// is read from the SessionAuth-populated ctx — never from headers
// (see FIX-1).
//
// Returns true if the caller's role is in `allowed`; otherwise writes
// 403 and returns false.
func requireRole(w http.ResponseWriter, r *http.Request, allowed ...string) bool {
	role := auth.GetUserRole(r.Context())
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
	// 5xx responses must never be silent — the client sees "internal
	// error" with no cause, so log the real error server-side with the
	// route + correlation id for triage.
	if httpErr.Code >= http.StatusInternalServerError {
		zlog.Error().Err(err).
			Str("method", r.Method).Str("path", r.URL.Path).
			Str("correlation_id", corr).Int("status", httpErr.Code).
			Msg("document handler 5xx")
	}
	writeJSONStatus(w, httpErr.Code, httpErr)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
