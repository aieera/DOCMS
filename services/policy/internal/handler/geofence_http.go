package handler

// Wave 15.2 admin CRUD for /api/v1/admin/geofences.
//
// Auth: pkg/middleware.SessionAuth populates auth.User(ctx) upstream;
// callers must additionally satisfy role=admin|owner which is checked
// at mount time in cmd/server/main.go.

import (
	"encoding/json"
	"net"
	"net/http"
	"net/netip"
	"time"

	"github.com/google/uuid"

	"github.com/vaultdms/vaultdms/pkg/auth"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/services/policy/internal/model"
	"github.com/vaultdms/vaultdms/services/policy/internal/service"
)

// GeofenceHTTP owns the CRUD + dry-run routes.
type GeofenceHTTP struct {
	svc *service.GeofenceService
}

// NewGeofenceHTTP constructs the handler.
func NewGeofenceHTTP(svc *service.GeofenceService) *GeofenceHTTP {
	return &GeofenceHTTP{svc: svc}
}

// Register mounts the admin routes.
func (h *GeofenceHTTP) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET  /api/v1/admin/geofences", h.list)
	mux.HandleFunc("POST /api/v1/admin/geofences", h.create)
	mux.HandleFunc("DELETE /api/v1/admin/geofences/{id}", h.delete)
	mux.HandleFunc("POST /api/v1/admin/geofences/test", h.dryRun)
}

type geofenceDTO struct {
	ID              string     `json:"id"`
	TenantID        string     `json:"tenant_id"`
	Scope           string     `json:"scope"`
	ScopeID         *string    `json:"scope_id,omitempty"`
	Mode            string     `json:"mode"`
	CountryCodes    []string   `json:"country_codes,omitempty"`
	CIDRAllowlist   []string   `json:"cidr_allowlist,omitempty"`
	CIDRDenylist    []string   `json:"cidr_denylist,omitempty"`
	ApplyTo         string     `json:"apply_to"`
	Enabled         bool       `json:"enabled"`
	CreatedByUserID *string    `json:"created_by_user_id,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toDTO(p model.GeofencePolicy) geofenceDTO {
	dto := geofenceDTO{
		ID:           p.ID.String(),
		TenantID:     p.TenantID.String(),
		Scope:        string(p.Scope),
		Mode:         string(p.Mode),
		CountryCodes: p.CountryCodes,
		ApplyTo:      string(p.ApplyTo),
		Enabled:      p.Enabled,
		CreatedAt:    p.CreatedAt,
		UpdatedAt:    p.UpdatedAt,
	}
	if p.ScopeID != nil {
		s := p.ScopeID.String()
		dto.ScopeID = &s
	}
	if p.CreatedByUserID != nil {
		s := p.CreatedByUserID.String()
		dto.CreatedByUserID = &s
	}
	for _, c := range p.CIDRAllowlist {
		dto.CIDRAllowlist = append(dto.CIDRAllowlist, c.String())
	}
	for _, c := range p.CIDRDenylist {
		dto.CIDRDenylist = append(dto.CIDRDenylist, c.String())
	}
	return dto
}

func (h *GeofenceHTTP) list(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	policies, err := h.svc.List(r.Context(), u.TenantID)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	out := make([]geofenceDTO, 0, len(policies))
	for _, p := range policies {
		out = append(out, toDTO(p))
	}
	writeJSON(w, http.StatusOK, map[string]any{"geofences": out})
}

type createRequest struct {
	Scope         string   `json:"scope"`
	ScopeID       string   `json:"scope_id"`
	Mode          string   `json:"mode"`
	CountryCodes  []string `json:"country_codes"`
	CIDRAllowlist []string `json:"cidr_allowlist"`
	CIDRDenylist  []string `json:"cidr_denylist"`
	ApplyTo       string   `json:"apply_to"`
	Enabled       *bool    `json:"enabled"`
}

func (h *GeofenceHTTP) create(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHTTPError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	in, err := buildCreateInput(req, u.TenantID, u.ID)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	created, err := h.svc.Create(r.Context(), in)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toDTO(*created))
}

func (h *GeofenceHTTP) delete(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeHTTPError(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if err := h.svc.Delete(r.Context(), u.TenantID, id); err != nil {
		writeHTTPError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type dryRunRequest struct {
	WorkspaceID string `json:"workspace_id"`
	DocumentID  string `json:"document_id"`
	Action      string `json:"action"`
	IP          string `json:"ip"`
}

// dryRun lets the admin UI paste an IP + action and see what decision
// the live policy set returns without triggering any enforcement.
func (h *GeofenceHTTP) dryRun(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var req dryRunRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeHTTPError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	ip := net.ParseIP(req.IP)
	if ip == nil {
		writeHTTPError(w, r, vdmserr.Validation("ip", "not a valid IP address"))
		return
	}
	in := service.GeofenceDecisionInput{
		TenantID: u.TenantID,
		Action:   model.GeofenceAction(req.Action),
		SourceIP: ip,
	}
	if req.WorkspaceID != "" {
		id, err := uuid.Parse(req.WorkspaceID)
		if err != nil {
			writeHTTPError(w, r, vdmserr.Validation("workspace_id", "not a uuid"))
			return
		}
		in.WorkspaceID = &id
	}
	if req.DocumentID != "" {
		id, err := uuid.Parse(req.DocumentID)
		if err != nil {
			writeHTTPError(w, r, vdmserr.Validation("document_id", "not a uuid"))
			return
		}
		in.DocumentID = &id
	}
	if in.Action == "" {
		in.Action = model.ActionAny
	}
	decision, err := h.svc.Decide(r.Context(), in)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	resp := map[string]any{
		"allow":            decision.Allow,
		"require_step_up":  decision.RequireStepUp,
		"reason":           decision.Reason,
	}
	if decision.MatchedPolicyID != uuid.Nil {
		resp["matched_policy_id"] = decision.MatchedPolicyID.String()
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- helpers --------------------------------------------------------------

func buildCreateInput(req createRequest, tenantID, userID uuid.UUID) (service.CreateGeofenceInput, error) {
	in := service.CreateGeofenceInput{
		TenantID:        tenantID,
		Scope:           model.GeofenceScope(req.Scope),
		Mode:            model.GeofenceMode(req.Mode),
		CountryCodes:    req.CountryCodes,
		ApplyTo:         model.GeofenceAction(req.ApplyTo),
		Enabled:         true,
		CreatedByUserID: userID,
	}
	if in.ApplyTo == "" {
		in.ApplyTo = model.ActionAny
	}
	if req.Enabled != nil {
		in.Enabled = *req.Enabled
	}
	if req.ScopeID != "" {
		id, err := uuid.Parse(req.ScopeID)
		if err != nil {
			return in, vdmserr.Validation("scope_id", "not a uuid")
		}
		in.ScopeID = &id
	}
	for _, s := range req.CIDRAllowlist {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return in, vdmserr.Validation("cidr_allowlist", "invalid CIDR: "+s)
		}
		in.CIDRAllowlist = append(in.CIDRAllowlist, p)
	}
	for _, s := range req.CIDRDenylist {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return in, vdmserr.Validation("cidr_denylist", "invalid CIDR: "+s)
		}
		in.CIDRDenylist = append(in.CIDRDenylist, p)
	}
	return in, nil
}

