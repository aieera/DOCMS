// ADR 0062 — LDAP/AD admin HTTP surface.
//
//	GET    /api/v1/admin/ldap/config         — current active config (or 404)
//	GET    /api/v1/admin/ldap/configs        — all rows including inactive drafts
//	POST   /api/v1/admin/ldap/configs        — create draft
//	PATCH  /api/v1/admin/ldap/configs/{id}   — update / activate
//	DELETE /api/v1/admin/ldap/configs/{id}
//	POST   /api/v1/admin/ldap/test-bind      — dry-run; existing id OR draft
//
//	GET    /api/v1/admin/ldap/configs/{id}/mappings
//	POST   /api/v1/admin/ldap/configs/{id}/mappings
//	DELETE /api/v1/admin/ldap/configs/{id}/mappings  (body: {ldap_dn, dms_group_id})
//
//	GET    /api/v1/admin/ldap/configs/{id}/history
//	POST   /api/v1/admin/ldap/configs/{id}/sync     — trigger an on-demand run
//
// Wrapped by the router with AuthMiddleware + CSRFDoubleSubmit +
// RequireRole("admin", "owner"). Bind passwords are never echoed
// back; the GET responses always show "********" in their place.
package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/repository"
	"github.com/aieera/sedoc/services/auth/internal/service"
)

// LDAPAdminHandler mounts /api/v1/admin/ldap/* routes.
type LDAPAdminHandler struct {
	pool *pgxpool.Pool
	svc  *service.Service
	repo repository.LDAPRepository
	log  zerolog.Logger
}

// NewLDAPAdminHandler constructs the handler. Pass nil to disable
// the route group (the router checks for nil before mounting).
func NewLDAPAdminHandler(pool *pgxpool.Pool, svc *service.Service, repo repository.LDAPRepository, log zerolog.Logger) *LDAPAdminHandler {
	if repo == nil {
		return nil
	}
	return &LDAPAdminHandler{pool: pool, svc: svc, repo: repo, log: log}
}

// Mount attaches the routes.
func (h *LDAPAdminHandler) Mount(r chi.Router) {
	r.Get("/config", h.activeConfig)
	r.Get("/configs", h.list)
	r.Post("/configs", h.create)
	r.Patch("/configs/{id}", h.update)
	r.Delete("/configs/{id}", h.delete)
	r.Post("/test-bind", h.testBind)

	r.Route("/configs/{id}/mappings", func(rr chi.Router) {
		rr.Get("/", h.listMappings)
		rr.Post("/", h.addMapping)
		rr.Delete("/", h.deleteMapping)
	})

	r.Get("/configs/{id}/history", h.history)
	r.Post("/configs/{id}/sync", h.syncNow)
}

// ---- DTOs -----------------------------------------------------------------

type ldapConfigDTO struct {
	ID                   string     `json:"id"`
	URL                  string     `json:"url"`
	UseStartTLS          bool       `json:"use_starttls"`
	AllowInsecure        bool       `json:"allow_insecure"`
	BindDN               string     `json:"bind_dn"`
	UserSearchBase       string     `json:"user_search_base"`
	UserSearchFilter     string     `json:"user_search_filter"`
	EmailAttribute       string     `json:"email_attribute"`
	DisplayNameAttribute string     `json:"display_name_attribute"`
	GroupSearchBase      string     `json:"group_search_base"`
	GroupSearchFilter    string     `json:"group_search_filter"`
	NestedGroups         bool       `json:"nested_groups"`
	FallbackToLocal      bool       `json:"fallback_to_local"`
	IsActive             bool       `json:"is_active"`
	HasBindPassword      bool       `json:"has_bind_password"`
	LastSyncAt           *time.Time `json:"last_sync_at,omitempty"`
	LastSyncStatus       *string    `json:"last_sync_status,omitempty"`
	LastSyncError        *string    `json:"last_sync_error,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	UpdatedAt            time.Time  `json:"updated_at"`
}

type ldapWriteBody struct {
	URL                  string  `json:"url"`
	UseStartTLS          *bool   `json:"use_starttls,omitempty"`
	AllowInsecure        *bool   `json:"allow_insecure,omitempty"`
	BindDN               string  `json:"bind_dn"`
	BindPassword         *string `json:"bind_password,omitempty"` // optional on PATCH
	UserSearchBase       string  `json:"user_search_base"`
	UserSearchFilter     string  `json:"user_search_filter"`
	EmailAttribute       string  `json:"email_attribute,omitempty"`
	DisplayNameAttribute string  `json:"display_name_attribute,omitempty"`
	GroupSearchBase      string  `json:"group_search_base"`
	GroupSearchFilter    string  `json:"group_search_filter"`
	NestedGroups         *bool   `json:"nested_groups,omitempty"`
	FallbackToLocal      *bool   `json:"fallback_to_local,omitempty"`
	IsActive             *bool   `json:"is_active,omitempty"`
}

type ldapTestBindBody struct {
	ExistingConfigID *string        `json:"existing_config_id,omitempty"`
	Draft            *ldapWriteBody `json:"draft,omitempty"`
	SampleUsername   string         `json:"sample_username,omitempty"`
	SamplePassword   string         `json:"sample_password,omitempty"`
}

type ldapMappingDTO struct {
	LDAPGroupDN string `json:"ldap_group_dn"`
	DMSGroupID  string `json:"dms_group_id"`
}

type ldapHistoryDTO struct {
	ID            string     `json:"id"`
	Trigger       string     `json:"trigger"`
	StartedAt     time.Time  `json:"started_at"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	Status        string     `json:"status"`
	UsersSynced   int        `json:"users_synced"`
	GroupsSynced  int        `json:"groups_synced"`
	Errors        int        `json:"errors"`
	ErrorSummary  *string    `json:"error_summary,omitempty"`
}

// ---- Handlers -------------------------------------------------------------

func (h *LDAPAdminHandler) activeConfig(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var cfg *repository.LDAPConfigRow
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		c, err := h.repo.GetActiveConfig(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		cfg = c
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, toDTO(cfg))
}

func (h *LDAPAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := []ldapConfigDTO{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := h.repo.ListConfigs(r.Context(), tx, tenantID)
		if err != nil {
			return err
		}
		for i := range rows {
			out = append(out, toDTO(&rows[i]))
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *LDAPAdminHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body ldapWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := validateLDAPWrite(&body, true); err != nil {
		h.writeErr(w, r, err)
		return
	}
	if body.BindPassword == nil || *body.BindPassword == "" {
		h.writeErr(w, r, vdmserr.Validation("bind_password", "required on create"))
		return
	}
	sealed, err := h.svc.SealBindPassword(*body.BindPassword)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	row := buildConfigRow(tenantID, uuid.Nil, &body, sealed)

	// If the caller asks for is_active=true, deactivate any other active
	// config first (the partial unique index would reject otherwise).
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		if row.IsActive {
			if _, err := tx.Exec(r.Context(),
				`UPDATE ldap_configs SET is_active = FALSE WHERE tenant_id = $1 AND is_active`,
				tenantID); err != nil {
				return err
			}
		}
		return h.repo.UpsertConfig(r.Context(), tx, row)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]string{"id": row.ID.String()})
}

func (h *LDAPAdminHandler) update(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body ldapWriteBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if err := validateLDAPWrite(&body, false); err != nil {
		h.writeErr(w, r, err)
		return
	}

	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		current, err := h.repo.GetConfig(r.Context(), tx, tenantID, id)
		if err != nil {
			return err
		}
		// Bind password is rotated only if a new one was supplied.
		sealed := current.BindPasswordSealed
		if body.BindPassword != nil && *body.BindPassword != "" {
			s, err := h.svc.SealBindPassword(*body.BindPassword)
			if err != nil {
				return err
			}
			sealed = s
		}
		row := buildConfigRow(tenantID, id, &body, sealed)
		// Preserve last_sync metadata across updates.
		row.LastSyncAt = current.LastSyncAt
		row.LastSyncStatus = current.LastSyncStatus
		row.LastSyncError = current.LastSyncError

		if row.IsActive && !current.IsActive {
			if _, err := tx.Exec(r.Context(),
				`UPDATE ldap_configs SET is_active = FALSE WHERE tenant_id = $1 AND is_active AND id <> $2`,
				tenantID, id); err != nil {
				return err
			}
		}
		return h.repo.UpsertConfig(r.Context(), tx, row)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LDAPAdminHandler) delete(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return h.repo.DeleteConfig(r.Context(), tx, tenantID, id)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LDAPAdminHandler) testBind(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body ldapTestBindBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	params := service.TestBindParams{
		SampleUsername: body.SampleUsername,
		SamplePassword: body.SamplePassword,
	}
	if body.ExistingConfigID != nil && *body.ExistingConfigID != "" {
		id, err := uuid.Parse(*body.ExistingConfigID)
		if err != nil {
			h.writeErr(w, r, vdmserr.Validation("existing_config_id", "not a uuid"))
			return
		}
		params.ExistingConfigID = &id
	} else if body.Draft != nil {
		if err := validateLDAPWrite(body.Draft, true); err != nil {
			h.writeErr(w, r, err)
			return
		}
		if body.Draft.BindPassword == nil || *body.Draft.BindPassword == "" {
			h.writeErr(w, r, vdmserr.Validation("draft.bind_password", "required for draft test"))
			return
		}
		row := buildConfigRow(tenantID, uuid.Nil, body.Draft, nil)
		params.DraftConfig = row
		params.DraftPassword = *body.Draft.BindPassword
	} else {
		h.writeErr(w, r, vdmserr.Validation("body", "either existing_config_id or draft required"))
		return
	}
	res := h.svc.TestBind(r.Context(), tenantID, params)
	h.writeJSON(w, http.StatusOK, res)
}

func (h *LDAPAdminHandler) listMappings(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	configID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	out := []ldapMappingDTO{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := h.repo.ListMappings(r.Context(), tx, tenantID, configID)
		if err != nil {
			return err
		}
		for _, m := range rows {
			out = append(out, ldapMappingDTO{
				LDAPGroupDN: m.LDAPGroupDN,
				DMSGroupID:  m.DMSGroupID.String(),
			})
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *LDAPAdminHandler) addMapping(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	configID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body ldapMappingDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	dmsID, err := uuid.Parse(body.DMSGroupID)
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("dms_group_id", "not a uuid"))
		return
	}
	if body.LDAPGroupDN == "" {
		h.writeErr(w, r, vdmserr.Validation("ldap_group_dn", "required"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return h.repo.UpsertMapping(r.Context(), tx, &repository.LDAPGroupMappingRow{
			TenantID: tenantID, LDAPConfigID: configID,
			LDAPGroupDN: body.LDAPGroupDN, DMSGroupID: dmsID,
		})
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LDAPAdminHandler) deleteMapping(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	configID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	var body ldapMappingDTO
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	dmsID, err := uuid.Parse(body.DMSGroupID)
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("dms_group_id", "not a uuid"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return h.repo.DeleteMapping(r.Context(), tx, tenantID, configID, body.LDAPGroupDN, dmsID)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *LDAPAdminHandler) history(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := []ldapHistoryDTO{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := h.repo.ListHistory(r.Context(), tx, tenantID, 50)
		if err != nil {
			return err
		}
		for _, h := range rows {
			out = append(out, ldapHistoryDTO{
				ID:           h.ID.String(),
				Trigger:      h.Trigger,
				StartedAt:    h.StartedAt,
				FinishedAt:   h.FinishedAt,
				Status:       h.Status,
				UsersSynced:  h.UsersSynced,
				GroupsSynced: h.GroupsSynced,
				Errors:       h.Errors,
				ErrorSummary: h.ErrorSummary,
			})
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

// syncNow triggers a sync run synchronously. The UI shows a spinner
// while the request is in flight; admins watching a specific group
// mapping want immediate feedback.
func (h *LDAPAdminHandler) syncNow(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	// 60-second guardrail — a runaway directory shouldn't tie up
	// the request goroutine forever.
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := h.svc.SyncTenant(ctx, tenantID, "manual"); err != nil {
		h.writeErr(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers --------------------------------------------------------------

func toDTO(c *repository.LDAPConfigRow) ldapConfigDTO {
	return ldapConfigDTO{
		ID:                   c.ID.String(),
		URL:                  c.URL,
		UseStartTLS:          c.UseStartTLS,
		AllowInsecure:        c.AllowInsecure,
		BindDN:               c.BindDN,
		UserSearchBase:       c.UserSearchBase,
		UserSearchFilter:     c.UserSearchFilter,
		EmailAttribute:       c.EmailAttribute,
		DisplayNameAttribute: c.DisplayNameAttribute,
		GroupSearchBase:      c.GroupSearchBase,
		GroupSearchFilter:    c.GroupSearchFilter,
		NestedGroups:         c.NestedGroups,
		FallbackToLocal:      c.FallbackToLocal,
		IsActive:             c.IsActive,
		HasBindPassword:      len(c.BindPasswordSealed) > 0,
		LastSyncAt:           c.LastSyncAt,
		LastSyncStatus:       c.LastSyncStatus,
		LastSyncError:        c.LastSyncError,
		CreatedAt:            c.CreatedAt,
		UpdatedAt:            c.UpdatedAt,
	}
}

func buildConfigRow(tenantID, id uuid.UUID, body *ldapWriteBody, sealed []byte) *repository.LDAPConfigRow {
	row := &repository.LDAPConfigRow{
		ID:                   id,
		TenantID:             tenantID,
		URL:                  body.URL,
		BindDN:               body.BindDN,
		BindPasswordSealed:   sealed,
		UserSearchBase:       body.UserSearchBase,
		UserSearchFilter:     body.UserSearchFilter,
		EmailAttribute:       defaultStr(body.EmailAttribute, "mail"),
		DisplayNameAttribute: defaultStr(body.DisplayNameAttribute, "displayName"),
		GroupSearchBase:      body.GroupSearchBase,
		GroupSearchFilter:    body.GroupSearchFilter,
	}
	row.UseStartTLS = boolDefault(body.UseStartTLS, true)
	row.AllowInsecure = boolDefault(body.AllowInsecure, false)
	row.NestedGroups = boolDefault(body.NestedGroups, true)
	row.FallbackToLocal = boolDefault(body.FallbackToLocal, false)
	row.IsActive = boolDefault(body.IsActive, false)
	return row
}

func validateLDAPWrite(b *ldapWriteBody, _ bool) error {
	if b.URL == "" {
		return vdmserr.Validation("url", "required")
	}
	if b.BindDN == "" {
		return vdmserr.Validation("bind_dn", "required")
	}
	if b.UserSearchBase == "" {
		return vdmserr.Validation("user_search_base", "required")
	}
	if b.UserSearchFilter == "" {
		return vdmserr.Validation("user_search_filter", "required")
	}
	if b.GroupSearchBase == "" {
		return vdmserr.Validation("group_search_base", "required")
	}
	if b.GroupSearchFilter == "" {
		return vdmserr.Validation("group_search_filter", "required")
	}
	return nil
}

func defaultStr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func boolDefault(p *bool, fallback bool) bool {
	if p == nil {
		return fallback
	}
	return *p
}

func (h *LDAPAdminHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *LDAPAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	httpErr := vdmserr.ToHTTPError(err, r.Header.Get("X-Correlation-ID"))
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, httpErr.Code, httpErr)
}
