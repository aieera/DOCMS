package scim

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

// Handler wires SCIM routes onto a chi router.
type Handler struct {
	repo *Repo
	base string // absolute URL prefix, e.g. https://app.example.com
}

// NewHandler builds a handler. base is used to render `meta.location` on
// returned resources per RFC 7644; leave empty to get relative URLs.
func NewHandler(repo *Repo, baseURL string) *Handler {
	return &Handler{repo: repo, base: strings.TrimRight(baseURL, "/")}
}

// Mount attaches the handler to a chi router under `/scim/v2/{tenant_slug}`.
// The caller wraps the group with the TenantResolver.Authenticate middleware
// for bearer-token auth per tenant.
func (h *Handler) Mount(r chi.Router) {
	r.Get("/ServiceProviderConfig", h.serviceProviderConfig)
	r.Get("/ResourceTypes", h.resourceTypes)
	r.Get("/Schemas", h.schemas)

	r.Route("/Users", func(r chi.Router) {
		r.Get("/", h.listUsers)
		r.Post("/", h.createUser)
		r.Get("/{id}", h.getUser)
		r.Put("/{id}", h.replaceUser)
		r.Patch("/{id}", h.patchUser)
		r.Delete("/{id}", h.deleteUser)
	})

	r.Route("/Groups", func(r chi.Router) {
		r.Get("/", h.listGroups)
		r.Post("/", h.createGroup)
		r.Get("/{id}", h.getGroup)
		r.Patch("/{id}", h.patchGroup)
		r.Delete("/{id}", h.deleteGroup)
	})
}

// ---- Users ----------------------------------------------------------------

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) {
	tenantID, ok := TenantFromContext(r.Context())
	if !ok {
		h.writeErr(w, http.StatusUnauthorized, "", "tenant missing")
		return
	}
	f, err := ParseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		h.writeErr(w, http.StatusNotImplemented, "invalidFilter", err.Error())
		return
	}
	start, count := paging(r)
	rows, total, err := h.repo.ListUsers(r.Context(), tenantID, f, start, count)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	out := ListResponse[User]{
		Schemas:      []string{SchemaListResp},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: len(rows),
		Resources:    make([]User, 0, len(rows)),
	}
	for _, u := range rows {
		out.Resources = append(out.Resources, h.userToProto(u))
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getUser(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	u, err := h.repo.GetUser(r.Context(), tenantID, id)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.userToProto(*u))
}

type userCreateBody struct {
	Schemas     []string `json:"schemas"`
	UserName    string   `json:"userName"`
	DisplayName string   `json:"displayName"`
	ExternalID  string   `json:"externalId,omitempty"`
	Active      *bool    `json:"active,omitempty"`
	Emails      []Email  `json:"emails,omitempty"`
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	var body userCreateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidSyntax", "bad JSON")
		return
	}
	email := pickPrimaryEmail(body.Emails, body.UserName)
	if email == "" {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "userName or emails required")
		return
	}
	display := strings.TrimSpace(body.DisplayName)
	if display == "" {
		display = email
	}
	u, err := h.repo.CreateUser(r.Context(), tenantID, email, display)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.repo.LogProvisioning(r.Context(), tenantID, "provisioned", "user", body.ExternalID, &u.ID, email)
	h.writeJSON(w, http.StatusCreated, h.userToProto(*u))
}

// applyUserUpdate persists a SCIM user change. A status→deactivated update is a
// deprovision — it routes through DeactivateUser (revoke sessions + API keys,
// emit dms.user.deprovisioned.v1, log) rather than a bare status write, so
// IdPs that deprovision via PATCH/PUT active:false get the full side effects.
func (h *Handler) applyUserUpdate(ctx context.Context, tenantID, id uuid.UUID, updates map[string]any) (*UserRow, error) {
	if s, _ := updates["status"].(string); s == "deactivated" {
		if err := h.repo.DeactivateUser(ctx, tenantID, id); err != nil {
			return nil, err
		}
		delete(updates, "status") // side effects done; don't re-write status
	}
	if len(updates) == 0 {
		return h.repo.GetUser(ctx, tenantID, id)
	}
	u, err := h.repo.UpdateUser(ctx, tenantID, id, updates)
	if err == nil {
		h.repo.LogProvisioning(ctx, tenantID, "updated", "user", "", &id, "")
	}
	return u, err
}

// replaceUser is PUT /Users/{id}. SCIM requires a full representation;
// the subset we actually persist is displayName + active.
func (h *Handler) replaceUser(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	var body userCreateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&body); err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidSyntax", "bad JSON")
		return
	}
	updates := map[string]any{}
	if body.DisplayName != "" {
		updates["display_name"] = body.DisplayName
	}
	if body.Active != nil {
		if *body.Active {
			updates["status"] = "active"
		} else {
			updates["status"] = "deactivated"
		}
	}
	u, err := h.applyUserUpdate(r.Context(), tenantID, id, updates)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.userToProto(*u))
}

// patchUser supports a small path allowlist: displayName, active.
func (h *Handler) patchUser(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	var patch PatchOp
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&patch); err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidSyntax", "bad JSON")
		return
	}
	updates := map[string]any{}
	for _, op := range patch.Operations {
		path := strings.ToLower(strings.TrimSpace(op.Path))
		switch path {
		case "displayname":
			if s, ok := op.Value.(string); ok {
				updates["display_name"] = s
			}
		case "active":
			if b, ok := op.Value.(bool); ok {
				if b {
					updates["status"] = "active"
				} else {
					updates["status"] = "deactivated"
				}
			}
			// Unknown paths are ignored — RFC 7644 allows targeted PATCH to
			// no-op unhandled attributes.
		}
	}
	u, err := h.applyUserUpdate(r.Context(), tenantID, id, updates)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.userToProto(*u))
}

func (h *Handler) deleteUser(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	if err := h.repo.DeactivateUser(r.Context(), tenantID, id); err != nil {
		h.writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Groups ---------------------------------------------------------------

type groupCreateBody struct {
	Schemas     []string      `json:"schemas"`
	DisplayName string        `json:"displayName"`
	Description string        `json:"description,omitempty"`
	Members     []GroupMember `json:"members,omitempty"`
}

func (h *Handler) listGroups(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	f, err := ParseFilter(r.URL.Query().Get("filter"))
	if err != nil {
		h.writeErr(w, http.StatusNotImplemented, "invalidFilter", err.Error())
		return
	}
	start, count := paging(r)
	rows, total, err := h.repo.ListGroups(r.Context(), tenantID, f, start, count)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	out := ListResponse[Group]{
		Schemas:      []string{SchemaListResp},
		TotalResults: total,
		StartIndex:   start,
		ItemsPerPage: len(rows),
		Resources:    make([]Group, 0, len(rows)),
	}
	for _, g := range rows {
		out.Resources = append(out.Resources, h.groupToProto(g))
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *Handler) getGroup(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	g, err := h.repo.GetGroup(r.Context(), tenantID, id)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.groupToProto(*g))
}

func (h *Handler) createGroup(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	var body groupCreateBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128*1024)).Decode(&body); err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidSyntax", "bad JSON")
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "displayName required")
		return
	}
	var memberIDs []uuid.UUID
	for _, m := range body.Members {
		if id, err := uuid.Parse(m.Value); err == nil {
			memberIDs = append(memberIDs, id)
		}
	}
	g, err := h.repo.CreateGroup(r.Context(), tenantID, body.DisplayName, body.Description, memberIDs)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusCreated, h.groupToProto(*g))
}

// patchGroup handles the two IdP-common ops: replace displayName, and
// add/remove/replace on "members" (the latter is where IdPs spend 95% of
// their PATCH traffic — membership syncs).
func (h *Handler) patchGroup(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	var patch PatchOp
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 512*1024)).Decode(&patch); err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidSyntax", "bad JSON")
		return
	}

	current, err := h.repo.GetGroup(r.Context(), tenantID, id)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}

	var (
		newName        *string
		newDesc        *string
		members        []uuid.UUID = append([]uuid.UUID{}, current.MemberIDs...)
		replaceMembers bool
	)

	for _, op := range patch.Operations {
		path := strings.ToLower(strings.TrimSpace(op.Path))
		switch {
		case path == "displayname":
			if s, ok := op.Value.(string); ok {
				newName = &s
			}
		case path == "description":
			if s, ok := op.Value.(string); ok {
				newDesc = &s
			}
		case path == "members" || strings.HasPrefix(path, "members"):
			switch strings.ToLower(op.Op) {
			case "replace":
				members = extractMemberIDs(op.Value)
				replaceMembers = true
			case "add":
				members = dedupAppend(members, extractMemberIDs(op.Value))
				replaceMembers = true
			case "remove":
				// Remove ops target specific user ids either by filter or
				// by explicit value. For IdP-emitted filters like
				// `members[value eq "<uuid>"]` we extract the id.
				members = removeMembers(members, extractMemberIDs(op.Value), path)
				replaceMembers = true
			}
		}
	}

	var memberArg []uuid.UUID
	if replaceMembers {
		memberArg = members
	}
	g, err := h.repo.UpdateGroup(r.Context(), tenantID, id, newName, newDesc, memberArg)
	if err != nil {
		h.writeDomainErr(w, err)
		return
	}
	h.writeJSON(w, http.StatusOK, h.groupToProto(*g))
}

func (h *Handler) deleteGroup(w http.ResponseWriter, r *http.Request) {
	tenantID, _ := TenantFromContext(r.Context())
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, http.StatusBadRequest, "invalidValue", "bad id")
		return
	}
	if err := h.repo.DeleteGroup(r.Context(), tenantID, id); err != nil {
		h.writeDomainErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- Discovery endpoints (static, cacheable) -----------------------------

func (h *Handler) serviceProviderConfig(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]any{
		"schemas":        []string{SchemaSPConfig},
		"patch":          map[string]bool{"supported": true},
		"bulk":           map[string]any{"supported": false, "maxOperations": 0, "maxPayloadSize": 0},
		"filter":         map[string]any{"supported": true, "maxResults": 200},
		"changePassword": map[string]bool{"supported": false},
		"sort":           map[string]bool{"supported": false},
		"etag":           map[string]bool{"supported": false},
		"authenticationSchemes": []map[string]string{{
			"type":        "oauthbearertoken",
			"name":        "Bearer Token",
			"description": "Per-tenant bearer token configured in sso_configs.config.scim_token_hash",
		}},
	})
}

func (h *Handler) resourceTypes(w http.ResponseWriter, r *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{SchemaListResp},
		"totalResults": 2,
		"Resources": []map[string]any{
			{"schemas": []string{SchemaResType}, "id": "User", "name": "User", "endpoint": "/Users", "schema": SchemaUser},
			{"schemas": []string{SchemaResType}, "id": "Group", "name": "Group", "endpoint": "/Groups", "schema": SchemaGroup},
		},
	})
}

func (h *Handler) schemas(w http.ResponseWriter, r *http.Request) {
	// Minimal — full schema JSON for User/Group is ~1KB each and rarely
	// consulted by IdPs at runtime. Returning the IDs is enough for
	// discovery; IdPs treat it as confirmation.
	h.writeJSON(w, http.StatusOK, map[string]any{
		"schemas":      []string{SchemaListResp},
		"totalResults": 2,
		"Resources": []map[string]string{
			{"id": SchemaUser, "name": "User"},
			{"id": SchemaGroup, "name": "Group"},
		},
	})
}

// ---- domain → wire projections -------------------------------------------

func (h *Handler) userToProto(u UserRow) User {
	return User{
		Schemas:     []string{SchemaUser},
		ID:          u.ID.String(),
		UserName:    u.Email,
		DisplayName: u.DisplayName,
		Emails:      []Email{{Value: u.Email, Primary: true, Type: "work"}},
		Active:      u.Status == "active",
		Meta: Meta{
			ResourceType: "User",
			Created:      u.CreatedAt,
			LastModified: u.UpdatedAt,
			Location:     h.base + "/Users/" + u.ID.String(),
		},
	}
}

func (h *Handler) groupToProto(g GroupRow) Group {
	members := make([]GroupMember, 0, len(g.MemberIDs))
	for _, uid := range g.MemberIDs {
		members = append(members, GroupMember{
			Value: uid.String(),
			Type:  "User",
			Ref:   h.base + "/Users/" + uid.String(),
		})
	}
	return Group{
		Schemas:     []string{SchemaGroup},
		ID:          g.ID.String(),
		DisplayName: g.Name,
		Members:     members,
		Meta: Meta{
			ResourceType: "Group",
			Created:      g.CreatedAt,
			LastModified: g.UpdatedAt,
			Location:     h.base + "/Groups/" + g.ID.String(),
		},
	}
}

// ---- small helpers --------------------------------------------------------

func (h *Handler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/scim+json")
	w.WriteHeader(code)
	if v == nil {
		return
	}
	_ = json.NewEncoder(w).Encode(v)
}

func (h *Handler) writeErr(w http.ResponseWriter, code int, scimType, detail string) {
	h.writeJSON(w, code, NewError(code, scimType, detail))
}

func (h *Handler) writeDomainErr(w http.ResponseWriter, err error) {
	switch vdmserr.KindOf(err) {
	case vdmserr.KindNotFound:
		h.writeErr(w, http.StatusNotFound, "", "resource not found")
	case vdmserr.KindAlreadyExists:
		h.writeErr(w, http.StatusConflict, "uniqueness", "resource already exists")
	case vdmserr.KindValidation:
		h.writeErr(w, http.StatusBadRequest, "invalidValue", err.Error())
	default:
		h.writeErr(w, http.StatusInternalServerError, "", "internal error")
	}
	_ = errors.Is // keep errors package reachable
}

func paging(r *http.Request) (start, count int) {
	start, _ = strconv.Atoi(r.URL.Query().Get("startIndex"))
	count, _ = strconv.Atoi(r.URL.Query().Get("count"))
	if start < 1 {
		start = 1
	}
	if count <= 0 {
		count = 100
	}
	if count > 200 {
		count = 200
	}
	return
}

// pickPrimaryEmail returns the email SCIM clients emit. IdPs send userName
// as an email sometimes and an emails array other times — accept either.
func pickPrimaryEmail(emails []Email, userName string) string {
	for _, e := range emails {
		if e.Primary && e.Value != "" {
			return e.Value
		}
	}
	for _, e := range emails {
		if e.Value != "" {
			return e.Value
		}
	}
	if strings.Contains(userName, "@") {
		return userName
	}
	return ""
}

func extractMemberIDs(v any) []uuid.UUID {
	var out []uuid.UUID
	switch vv := v.(type) {
	case []any:
		for _, m := range vv {
			if mm, ok := m.(map[string]any); ok {
				if s, ok := mm["value"].(string); ok {
					if id, err := uuid.Parse(s); err == nil {
						out = append(out, id)
					}
				}
			} else if s, ok := m.(string); ok {
				if id, err := uuid.Parse(s); err == nil {
					out = append(out, id)
				}
			}
		}
	case map[string]any:
		if s, ok := vv["value"].(string); ok {
			if id, err := uuid.Parse(s); err == nil {
				out = []uuid.UUID{id}
			}
		}
	case string:
		if id, err := uuid.Parse(vv); err == nil {
			out = []uuid.UUID{id}
		}
	}
	return out
}

func dedupAppend(dst []uuid.UUID, add []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(dst))
	for _, x := range dst {
		seen[x] = struct{}{}
	}
	for _, x := range add {
		if _, ok := seen[x]; !ok {
			seen[x] = struct{}{}
			dst = append(dst, x)
		}
	}
	return dst
}

// removeMembers handles both "remove op with value list" and "remove op
// with filter path like members[value eq "uuid"]". For the filter form
// we extract the uuid literal from the path.
func removeMembers(current []uuid.UUID, explicit []uuid.UUID, path string) []uuid.UUID {
	toRemove := make(map[uuid.UUID]struct{}, len(explicit))
	for _, x := range explicit {
		toRemove[x] = struct{}{}
	}
	if i := strings.Index(path, "\""); i > 0 {
		if j := strings.Index(path[i+1:], "\""); j > 0 {
			if id, err := uuid.Parse(path[i+1 : i+1+j]); err == nil {
				toRemove[id] = struct{}{}
			}
		}
	}
	out := current[:0]
	for _, x := range current {
		if _, drop := toRemove[x]; !drop {
			out = append(out, x)
		}
	}
	return out
}

// static assertion: RFC 7644 timestamps are RFC 3339.
var _ = time.RFC3339
