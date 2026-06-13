// ADR 0064 — HTTP surface for tenant-wide delegations and recall.
//
//	POST   /api/v1/workflows/delegations            — create
//	GET    /api/v1/workflows/delegations            — list mine
//	DELETE /api/v1/workflows/delegations/{id}       — revoke
//	POST   /api/v1/workflows/instances/{id}/recall  — initiator-only,
//	                                                  gated by approver-acted check
//
// The cancel route stays as the admin override and audits as
// "cancel"; recall audits as "recall" with the gate.
package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/aieera/sedoc/pkg/auth"
	"github.com/aieera/sedoc/services/workflow/internal/repository"
	"github.com/aieera/sedoc/services/workflow/internal/service"
)

// RegisterRoutingPatterns mounts the new routes alongside the
// existing Register call. Caller is responsible for chaining this
// after the main mux registration.
func (h *Handler) RegisterRoutingPatterns(mux *http.ServeMux) {
	mux.HandleFunc("POST   /api/v1/workflows/delegations", h.createDelegation)
	mux.HandleFunc("GET    /api/v1/workflows/delegations", h.listDelegations)
	mux.HandleFunc("DELETE /api/v1/workflows/delegations/{id}", h.revokeDelegation)
	mux.HandleFunc("POST   /api/v1/workflows/instances/{id}/recall", h.recallInstance)
}

type createDelegationBody struct {
	DelegatorID string    `json:"delegator_id"`
	DelegateID  string    `json:"delegate_id"`
	StartsAt    time.Time `json:"starts_at"`
	EndsAt      time.Time `json:"ends_at"`
	Reason      string    `json:"reason,omitempty"`
}

func (h *Handler) createDelegation(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	role := auth.RoleString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	var body createDelegationBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Default delegator to the caller when omitted — this is the
	// common shape ("delegate MY tasks to X"). Admin-on-behalf flows
	// pass an explicit delegator_id.
	if body.DelegatorID == "" {
		body.DelegatorID = userID
	}
	row, err := h.svc.CreateDelegation(r.Context(), tenantID, userID, role, repository.DelegationRow{
		DelegatorID: body.DelegatorID, DelegateID: body.DelegateID,
		StartsAt: body.StartsAt, EndsAt: body.EndsAt, Reason: body.Reason,
	})
	if err != nil {
		writeDelegationErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, row)
}

func (h *Handler) listDelegations(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	if tenantID == "" || userID == "" {
		writeError(w, http.StatusBadRequest, "X-Tenant-ID and X-User-ID required")
		return
	}
	rows, err := h.svc.ListMyDelegations(r.Context(), tenantID, userID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rows == nil {
		rows = []repository.DelegationRow{}
	}
	writeJSON(w, http.StatusOK, rows)
}

func (h *Handler) revokeDelegation(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	role := auth.RoleString(r)
	id := r.PathValue("id")
	if tenantID == "" || userID == "" || id == "" {
		writeError(w, http.StatusBadRequest, "missing identity headers or id")
		return
	}
	if err := h.svc.RevokeDelegation(r.Context(), tenantID, userID, role, id); err != nil {
		writeDelegationErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) recallInstance(w http.ResponseWriter, r *http.Request) {
	tenantID := auth.TenantIDString(r)
	userID := auth.UserIDString(r)
	id := r.PathValue("id")
	if tenantID == "" || userID == "" || id == "" {
		writeError(w, http.StatusBadRequest, "missing identity headers or id")
		return
	}
	if err := h.svc.RecallInstance(r.Context(), tenantID, userID, id); err != nil {
		switch {
		case errors.Is(err, service.ErrRecallTooLate):
			writeError(w, http.StatusConflict, err.Error())
		case errors.Is(err, service.ErrDelegationAuthority):
			writeError(w, http.StatusForbidden, err.Error())
		default:
			writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeDelegationErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, service.ErrDelegationCycle):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, service.ErrDelegationAuthority):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		writeError(w, http.StatusBadRequest, err.Error())
	}
}
