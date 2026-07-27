// REST wrapper around the policy service, exposing the four permission
// endpoints the web app's `web/src/api/permissions.ts` calls. The gRPC
// surface (CheckPermission / BatchCheckPermission) stays as-is for service-
// to-service calls; this file adds a session-authenticated HTTP face for
// the frontend.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/aieera/sedoc/pkg/auth"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/policy/internal/model"
	"github.com/aieera/sedoc/services/policy/internal/service"
)

// HTTPHandler is the REST face of the policy service. It never exposes raw
// permission IDs except in URLs — callers key everything off (resource,
// principal, capability) triples just like the SQL schema.
type HTTPHandler struct {
	svc *service.Service
}

// NewHTTPHandler constructs the REST handler.
func NewHTTPHandler(svc *service.Service) *HTTPHandler { return &HTTPHandler{svc: svc} }

// Register mounts routes on the supplied mux. Callers must wrap the mux with
// middleware.SessionAuth so auth.User(ctx) is populated by the time these
// handlers run.
func (h *HTTPHandler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/permissions/matrix", h.MatrixHandler)
	// Self-scoped: the caller's own grants (direct + via groups). Feeds
	// the "shared with me" documents surface; no admin gate — you can
	// always see what's been shared with YOU. Registered before the
	// {resource_type}/{resource_id} pattern only for readability; the
	// single-segment path can't collide with the two-segment wildcard.
	mux.HandleFunc("GET /api/v1/permissions/mine", h.mine)
	mux.HandleFunc("GET /api/v1/permissions/{resource_type}/{resource_id}", h.list)
	// Effective access — "who can see this and why". More-specific literal
	// segment than the {resource_id} list route, so ServeMux routes it here.
	mux.HandleFunc("GET /api/v1/permissions/{resource_type}/{resource_id}/effective", h.effective)
	mux.HandleFunc("POST /api/v1/permissions/check", h.check)
	mux.HandleFunc("POST /api/v1/permissions/{resource_type}/{resource_id}", h.grant)
	mux.HandleFunc("DELETE /api/v1/permissions/{resource_type}/{resource_id}/{principal_id}", h.revoke)
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

type permissionDTO struct {
	ID            string     `json:"id"`
	ResourceType  string     `json:"resource_type"`
	ResourceID    string     `json:"resource_id"`
	PrincipalType string     `json:"principal_type"`
	PrincipalID   string     `json:"principal_id"`
	Capability    string     `json:"capability"`
	GrantedBy     string     `json:"granted_by"`
	GrantedAt     time.Time  `json:"granted_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
}

// list — GET /permissions/:resource_type/:resource_id
// Requires the caller to have ADMIN or SHARE capability on the resource.
func (h *HTTPHandler) list(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	resourceType, resourceID, httpErr := parseResource(r)
	if httpErr != nil {
		writeHTTPError(w, r, httpErr)
		return
	}
	if !h.callerMayAdminister(r, u, resourceType, resourceID) {
		writeHTTPError(w, r, vdmserr.ErrForbidden)
		return
	}

	perms, err := h.svc.ListByResource(r.Context(), u.TenantID, resourceType, resourceID)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	out := make([]permissionDTO, 0, len(perms))
	for _, p := range perms {
		out = append(out, toPermissionDTO(p))
	}
	writeJSONOK(w, out)
}

// mine — GET /permissions/mine. Grants held by the authenticated caller
// (direct user grants + via group membership). Self-scoped, so no
// callerMayAdminister gate.
func (h *HTTPHandler) mine(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	perms, err := h.svc.ListMine(r.Context(), u.TenantID, u.ID)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	out := make([]permissionDTO, 0, len(perms))
	for _, p := range perms {
		out = append(out, toPermissionDTO(p))
	}
	writeJSONOK(w, out)
}

type effectivePrincipalDTO struct {
	PrincipalType string   `json:"principal_type"`
	PrincipalID   string   `json:"principal_id"`
	Capability    string   `json:"capability"`
	Reasons       []string `json:"reasons"`
}

type effectiveAccessDTO struct {
	Principals                       []effectivePrincipalDTO `json:"principals"`
	OrgAdminsHaveAccess              bool                    `json:"org_admins_have_access"`
	WorkspaceBaselineViewWorkspaceID string                  `json:"workspace_baseline_view_workspace_id,omitempty"`
	PrivateFolder                    bool                    `json:"private_folder"`
	FolderOwnerID                    string                  `json:"folder_owner_id,omitempty"`
}

// effective — GET /permissions/:resource_type/:resource_id/effective
// "Who can see this, and why." Requires ADMIN on the resource (same gate
// as listing the ACL). Optional ?workspace_id= & ?folder_id= supply the
// cascade context the caller already knows.
func (h *HTTPHandler) effective(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	resourceType, resourceID, httpErr := parseResource(r)
	if httpErr != nil {
		writeHTTPError(w, r, httpErr)
		return
	}
	if !h.callerMayAdminister(r, u, resourceType, resourceID) {
		writeHTTPError(w, r, vdmserr.ErrForbidden)
		return
	}
	optUUID := func(q string) *uuid.UUID {
		v := r.URL.Query().Get(q)
		if v == "" {
			return nil
		}
		if id, err := uuid.Parse(v); err == nil {
			return &id
		}
		return nil
	}
	ea, err := h.svc.EffectiveAccess(r.Context(), u.TenantID, resourceType, resourceID, optUUID("workspace_id"), optUUID("folder_id"))
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	dto := effectiveAccessDTO{
		OrgAdminsHaveAccess: ea.OrgAdminsHaveAccess,
		PrivateFolder:       ea.PrivateFolder,
		Principals:          make([]effectivePrincipalDTO, 0, len(ea.Principals)),
	}
	for _, p := range ea.Principals {
		dto.Principals = append(dto.Principals, effectivePrincipalDTO{
			PrincipalType: string(p.PrincipalType),
			PrincipalID:   p.PrincipalID.String(),
			Capability:    string(p.Capability),
			Reasons:       p.Reasons,
		})
	}
	if ea.WorkspaceBaselineView != nil {
		dto.WorkspaceBaselineViewWorkspaceID = ea.WorkspaceBaselineView.String()
	}
	if ea.FolderOwner != nil {
		dto.FolderOwnerID = ea.FolderOwner.String()
	}
	writeJSONOK(w, dto)
}

// check — POST /permissions/check
// Body: {resource_type, resource_id, action}. Subject is the authenticated
// user; callers that need to check for a different subject go through the
// internal gRPC.
func (h *HTTPHandler) check(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	var body struct {
		ResourceType string            `json:"resource_type"`
		ResourceID   string            `json:"resource_id"`
		Action       string            `json:"action"`
		Context      map[string]string `json:"context,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeHTTPError(w, r, err)
		return
	}
	result, err := h.svc.Check(r.Context(), service.CheckInput{
		TenantID:     u.TenantID,
		SubjectType:  "user",
		SubjectID:    u.ID.String(),
		Action:       body.Action,
		ResourceType: body.ResourceType,
		ResourceID:   body.ResourceID,
		Context:      body.Context,
	})
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	writeJSONOK(w, map[string]any{"allowed": result.Allowed, "reason": result.Reason})
}

// grant — POST /permissions/:resource_type/:resource_id
// Body: {principal_type, principal_id, capability, expires_at?}
// Requires ADMIN on the resource.
func (h *HTTPHandler) grant(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	resourceType, resourceID, httpErr := parseResource(r)
	if httpErr != nil {
		writeHTTPError(w, r, httpErr)
		return
	}
	if !h.callerMayAdminister(r, u, resourceType, resourceID) {
		writeHTTPError(w, r, vdmserr.ErrForbidden)
		return
	}

	var body struct {
		PrincipalType string     `json:"principal_type"`
		PrincipalID   string     `json:"principal_id"`
		Capability    string     `json:"capability"`
		ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	}
	if err := decodeJSON(r, &body); err != nil {
		writeHTTPError(w, r, err)
		return
	}
	principalID, err := uuid.Parse(body.PrincipalID)
	if err != nil {
		writeHTTPError(w, r, vdmserr.Validation("principal_id", "not a uuid"))
		return
	}
	p, err := h.svc.Grant(r.Context(), service.GrantInput{
		TenantID:      u.TenantID,
		GrantedBy:     u.ID,
		ResourceType:  resourceType,
		ResourceID:    resourceID,
		PrincipalType: model.PrincipalType(body.PrincipalType),
		PrincipalID:   principalID,
		Capability:    model.Capability(body.Capability),
		ExpiresAt:     body.ExpiresAt,
	})
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, toPermissionDTO(*p))
}

// revoke — DELETE /permissions/:resource_type/:resource_id/:principal_id
// Revokes every active grant for (resource, principal).
// Requires ADMIN on the resource.
func (h *HTTPHandler) revoke(w http.ResponseWriter, r *http.Request) {
	u, err := auth.User(r.Context())
	if err != nil {
		writeHTTPError(w, r, vdmserr.ErrUnauthorized)
		return
	}
	resourceType, resourceID, httpErr := parseResource(r)
	if httpErr != nil {
		writeHTTPError(w, r, httpErr)
		return
	}
	if !h.callerMayAdminister(r, u, resourceType, resourceID) {
		writeHTTPError(w, r, vdmserr.ErrForbidden)
		return
	}
	principalID, err := uuid.Parse(r.PathValue("principal_id"))
	if err != nil {
		writeHTTPError(w, r, vdmserr.Validation("principal_id", "not a uuid"))
		return
	}

	existing, err := h.svc.ListByResource(r.Context(), u.TenantID, resourceType, resourceID)
	if err != nil {
		writeHTTPError(w, r, err)
		return
	}
	for _, p := range existing {
		if p.PrincipalID != principalID {
			continue
		}
		if err := h.svc.Revoke(r.Context(), u.TenantID, p.ID, u.ID); err != nil {
			writeHTTPError(w, r, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// callerMayAdminister re-runs an internal admin check via the service. The
// ADMIN capability on the resource is the gate for read + grant + revoke
// on its ACL.
func (h *HTTPHandler) callerMayAdminister(r *http.Request, u auth.UserInfo, resourceType model.ResourceType, resourceID uuid.UUID) bool {
	// Org owners and admins bypass the per-resource check.
	switch u.Role {
	case "owner", "admin":
		return true
	}
	res, err := h.svc.Check(r.Context(), service.CheckInput{
		TenantID:     u.TenantID,
		SubjectType:  "user",
		SubjectID:    u.ID.String(),
		Action:       "admin",
		ResourceType: string(resourceType),
		ResourceID:   resourceID.String(),
	})
	if err != nil {
		return false
	}
	return res.Allowed
}

func parseResource(r *http.Request) (model.ResourceType, uuid.UUID, error) {
	rt := r.PathValue("resource_type")
	switch rt {
	case "document", "folder", "workspace":
	default:
		return "", uuid.Nil, vdmserr.Validation("resource_type", "must be document, folder, or workspace")
	}
	id, err := uuid.Parse(r.PathValue("resource_id"))
	if err != nil {
		return "", uuid.Nil, vdmserr.Validation("resource_id", "not a uuid")
	}
	return model.ResourceType(rt), id, nil
}

func toPermissionDTO(p model.Permission) permissionDTO {
	return permissionDTO{
		ID:            p.ID.String(),
		ResourceType:  string(p.ResourceType),
		ResourceID:    p.ResourceID.String(),
		PrincipalType: string(p.PrincipalType),
		PrincipalID:   p.PrincipalID.String(),
		Capability:    string(p.Capability),
		GrantedBy:     p.GrantedBy.String(),
		GrantedAt:     p.GrantedAt,
		ExpiresAt:     p.ExpiresAt,
	}
}

func decodeJSON(r *http.Request, v any) error {
	if r.ContentLength > 64*1024 {
		return vdmserr.Validation("body", "request too large")
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return vdmserr.Validation("body", "invalid JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONOK(w http.ResponseWriter, v any) { writeJSON(w, http.StatusOK, v) }

func writeHTTPError(w http.ResponseWriter, r *http.Request, err error) {
	corr := auth.GetCorrelationID(r.Context())
	body := vdmserr.ToHTTPError(err, corr)
	if body.Code >= 500 {
		fmt.Printf("[policy http 5xx] corr=%s err=%v\n", corr, err)
	}
	writeJSON(w, body.Code, body)
}
