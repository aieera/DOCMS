// Wave 10 — SSO admin CRUD + validate.
//
//	GET    /api/v1/admin/sso-configs
//	POST   /api/v1/admin/sso-configs
//	GET    /api/v1/admin/sso-configs/{id}
//	PATCH  /api/v1/admin/sso-configs/{id}
//	DELETE /api/v1/admin/sso-configs/{id}
//	POST   /api/v1/admin/sso-configs/validate  (dry-run; no persistence)
//
// Writes land on sso_configs. The `config` column stores the
// provider-specific JSONB (see sso.SAMLConfig / sso.OIDCConfig).
// Secrets (OIDC client_secret) are redacted on list/get responses.
//
// "Validate" is a non-persisting endpoint that the wizard calls to
// surface parse / reachability errors before the operator commits.
// It doesn't run an end-to-end authentication — that requires a
// real IdP round trip, which is Wave 11's follow-up ("test
// assertion" in the spec).
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
	"github.com/aieera/sedoc/services/auth/internal/sso"
)

// SSOAdminHandler mounts /api/v1/admin/sso-configs/* routes.
type SSOAdminHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
	// metadataTimeout bounds the external fetch in validateSAML when
	// the admin provides idp_metadata_url. Defaults to 5 seconds.
	metadataTimeout time.Duration
}

// NewSSOAdminHandler constructs the handler.
func NewSSOAdminHandler(pool *pgxpool.Pool, log zerolog.Logger) *SSOAdminHandler {
	return &SSOAdminHandler{pool: pool, log: log, metadataTimeout: 5 * time.Second}
}

// Mount attaches routes to a chi router. Caller wraps the group
// with AuthMiddleware + CSRFDoubleSubmit + RequireRole.
func (h *SSOAdminHandler) Mount(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Post("/validate", h.validate)
	r.Get("/{id}", h.get)
	r.Patch("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

// ---- DTOs -----------------------------------------------------------------

type ssoConfigDTO struct {
	ID          string          `json:"id"`
	Provider    string          `json:"provider"` // saml | oidc
	DisplayName string          `json:"display_name"`
	IsActive    bool            `json:"is_active"`
	Config      json.RawMessage `json:"config"`
	CreatedAt   time.Time       `json:"created_at"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

type createSSOBody struct {
	Provider    string          `json:"provider"`
	DisplayName string          `json:"display_name"`
	Config      json.RawMessage `json:"config"`
	IsActive    *bool           `json:"is_active,omitempty"`
}

type updateSSOBody struct {
	DisplayName *string         `json:"display_name,omitempty"`
	Config      json.RawMessage `json:"config,omitempty"`
	IsActive    *bool           `json:"is_active,omitempty"`
}

type validateResult struct {
	OK       bool              `json:"ok"`
	Provider string            `json:"provider"`
	Warnings []string          `json:"warnings,omitempty"`
	Details  map[string]string `json:"details,omitempty"`
	Error    string            `json:"error,omitempty"`
}

// ---- Handlers -------------------------------------------------------------

func (h *SSOAdminHandler) list(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := []ssoConfigDTO{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id::text, provider_type, display_name, is_active, config, created_at, updated_at
			  FROM sso_configs WHERE tenant_id = $1
			 ORDER BY updated_at DESC`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d ssoConfigDTO
			if err := rows.Scan(&d.ID, &d.Provider, &d.DisplayName, &d.IsActive,
				&d.Config, &d.CreatedAt, &d.UpdatedAt); err != nil {
				return err
			}
			d.Config = redactSecrets(d.Provider, d.Config)
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}

func (h *SSOAdminHandler) get(w http.ResponseWriter, r *http.Request) {
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
	var d ssoConfigDTO
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		err := tx.QueryRow(r.Context(), `
			SELECT id::text, provider_type, display_name, is_active, config, created_at, updated_at
			  FROM sso_configs WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&d.ID, &d.Provider, &d.DisplayName, &d.IsActive,
			&d.Config, &d.CreatedAt, &d.UpdatedAt)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("sso config not found")
			}
			return err
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	d.Config = redactSecrets(d.Provider, d.Config)
	h.writeJSON(w, http.StatusOK, d)
}

func (h *SSOAdminHandler) create(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body createSSOBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Provider != "saml" && body.Provider != "oidc" {
		h.writeErr(w, r, vdmserr.Validation("provider", "must be saml or oidc"))
		return
	}
	if strings.TrimSpace(body.DisplayName) == "" {
		h.writeErr(w, r, vdmserr.Validation("display_name", "required"))
		return
	}
	if len(body.Config) == 0 {
		h.writeErr(w, r, vdmserr.Validation("config", "required"))
		return
	}
	if v := h.validateConfig(r.Context(), body.Provider, body.Config); !v.OK {
		h.writeErr(w, r, vdmserr.Validation("config", v.Error))
		return
	}
	active := true
	if body.IsActive != nil {
		active = *body.IsActive
	}
	id := uuid.New()
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		if _, err := tx.Exec(r.Context(), `
			INSERT INTO sso_configs (tenant_id, id, provider_type, display_name, config, is_active)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			tenantID, id, body.Provider, body.DisplayName, body.Config, active); err != nil {
			return err
		}
		return h.emitConfigured(r.Context(), tx, tenantID, id, body.Provider, body.DisplayName, active, "created")
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusCreated, map[string]any{
		"id":       id.String(),
		"provider": body.Provider,
	})
}

func (h *SSOAdminHandler) update(w http.ResponseWriter, r *http.Request) {
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
	var body updateSSOBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}

	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		// Need the provider to validate any config patch.
		var provider string
		if err := tx.QueryRow(r.Context(),
			`SELECT provider_type FROM sso_configs WHERE tenant_id = $1 AND id = $2`,
			tenantID, id,
		).Scan(&provider); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return vdmserr.NotFound("sso config not found")
			}
			return err
		}
		if len(body.Config) > 0 {
			if v := h.validateConfig(r.Context(), provider, body.Config); !v.OK {
				return vdmserr.Validation("config", v.Error)
			}
		}

		var configArg any
		if len(body.Config) > 0 {
			configArg = []byte(body.Config)
		}
		tag, err := tx.Exec(r.Context(), `
			UPDATE sso_configs
			   SET display_name = COALESCE($1, display_name),
			       config       = COALESCE($2, config),
			       is_active    = COALESCE($3, is_active),
			       updated_at   = now()
			 WHERE tenant_id = $4 AND id = $5`,
			body.DisplayName, configArg, body.IsActive, tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("sso config not found")
		}
		action := "updated"
		if body.IsActive != nil && !*body.IsActive {
			action = "disabled"
		}
		dn := ""
		if body.DisplayName != nil {
			dn = *body.DisplayName
		}
		active := body.IsActive == nil || *body.IsActive
		return h.emitConfigured(r.Context(), tx, tenantID, id, provider, dn, active, action)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// emitConfigured writes dms.idp.configured.v1 to the outbox in the same tx as
// the config write (transactional-outbox → durable delivery + audit).
func (h *SSOAdminHandler) emitConfigured(ctx context.Context, tx pgx.Tx, tenantID, id uuid.UUID, provider, displayName string, active bool, action string) error {
	payload, _ := json.Marshal(map[string]any{
		"sso_config_id": id.String(),
		"tenant_id":     tenantID.String(),
		"provider":      provider,
		"display_name":  displayName,
		"is_active":     active,
		"action":        action, // created | updated | disabled
	})
	evt := database.NewOutboxEvent(tenantID, "dms.idp.configured.v1", "sso_config", id, payload)
	return database.NewOutboxRepository().Insert(ctx, tx, evt)
}

func (h *SSOAdminHandler) delete(w http.ResponseWriter, r *http.Request) {
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
		tag, err := tx.Exec(r.Context(),
			`DELETE FROM sso_configs WHERE tenant_id = $1 AND id = $2`,
			tenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("sso config not found")
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// validate is the wizard's dry-run endpoint.
func (h *SSOAdminHandler) validate(w http.ResponseWriter, r *http.Request) {
	if _, _, _, err := requireUser(r); err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body createSSOBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if body.Provider != "saml" && body.Provider != "oidc" {
		h.writeErr(w, r, vdmserr.Validation("provider", "must be saml or oidc"))
		return
	}
	if len(body.Config) == 0 {
		h.writeErr(w, r, vdmserr.Validation("config", "required"))
		return
	}
	result := h.validateConfig(r.Context(), body.Provider, body.Config)
	h.writeJSON(w, http.StatusOK, result)
}

// ---- Validation -----------------------------------------------------------

func (h *SSOAdminHandler) validateConfig(ctx context.Context, provider string, raw json.RawMessage) validateResult {
	switch provider {
	case "saml":
		return h.validateSAML(ctx, raw)
	case "oidc":
		return h.validateOIDC(ctx, raw)
	default:
		return validateResult{OK: false, Error: "unknown provider"}
	}
}

func (h *SSOAdminHandler) validateSAML(ctx context.Context, raw json.RawMessage) validateResult {
	var cfg sso.SAMLConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return validateResult{OK: false, Provider: "saml", Error: "invalid JSON body: " + err.Error()}
	}
	if cfg.IdPMetadataURL == "" && cfg.IdPMetadataXML == "" {
		return validateResult{OK: false, Provider: "saml",
			Error: "one of idp_metadata_url or idp_metadata_xml is required"}
	}
	if cfg.IdPMetadataURL != "" && cfg.IdPMetadataXML != "" {
		return validateResult{OK: false, Provider: "saml",
			Error: "only one of idp_metadata_url or idp_metadata_xml may be provided"}
	}
	warnings := []string{}
	details := map[string]string{}

	if cfg.IdPMetadataURL != "" {
		if !strings.HasPrefix(cfg.IdPMetadataURL, "https://") {
			warnings = append(warnings, "metadata URL should use https://")
		}
		// Fetch + parse, bounded by h.metadataTimeout.
		cctx, cancel := context.WithTimeout(ctx, h.metadataTimeout)
		defer cancel()
		req, err := http.NewRequestWithContext(cctx, http.MethodGet, cfg.IdPMetadataURL, nil)
		if err != nil {
			return validateResult{OK: false, Provider: "saml", Error: "bad url: " + err.Error()}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return validateResult{OK: false, Provider: "saml", Error: "fetch failed: " + err.Error()}
		}
		defer resp.Body.Close()
		if resp.StatusCode >= 400 {
			return validateResult{OK: false, Provider: "saml",
				Error: fmt.Sprintf("metadata URL returned %d", resp.StatusCode)}
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1 MiB cap
		details["metadata_bytes"] = fmt.Sprintf("%d", len(body))
		if !strings.Contains(string(body), "EntityDescriptor") {
			warnings = append(warnings, "metadata body doesn't contain <EntityDescriptor> — not recognisable SAML 2.0")
		}
	}
	if cfg.IdPMetadataXML != "" {
		if !strings.Contains(cfg.IdPMetadataXML, "EntityDescriptor") {
			return validateResult{OK: false, Provider: "saml",
				Error: "inline XML missing <EntityDescriptor> — not SAML 2.0 metadata"}
		}
		details["inline_bytes"] = fmt.Sprintf("%d", len(cfg.IdPMetadataXML))
	}
	if cfg.AttributeMapping.Email == "" {
		warnings = append(warnings, "attribute_mapping.email not set — falling back to SAML defaults; IdPs vary")
	}
	if cfg.ClockSkewSeconds > 600 {
		return validateResult{OK: false, Provider: "saml",
			Error: "clock_skew_seconds capped at 600"}
	}

	return validateResult{OK: true, Provider: "saml", Warnings: warnings, Details: details}
}

func (h *SSOAdminHandler) validateOIDC(ctx context.Context, raw json.RawMessage) validateResult {
	var cfg sso.OIDCConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return validateResult{OK: false, Provider: "oidc", Error: "invalid JSON body: " + err.Error()}
	}
	if cfg.IssuerURL == "" {
		return validateResult{OK: false, Provider: "oidc", Error: "issuer_url required"}
	}
	if cfg.ClientID == "" {
		return validateResult{OK: false, Provider: "oidc", Error: "client_id required"}
	}
	if cfg.ClientSecret == "" {
		return validateResult{OK: false, Provider: "oidc", Error: "client_secret required"}
	}
	if cfg.RedirectURL == "" {
		return validateResult{OK: false, Provider: "oidc", Error: "redirect_url required"}
	}
	if !strings.HasPrefix(cfg.IssuerURL, "https://") {
		return validateResult{OK: false, Provider: "oidc", Error: "issuer_url must be https"}
	}
	if !strings.HasPrefix(cfg.RedirectURL, "https://") && !strings.HasPrefix(cfg.RedirectURL, "http://localhost") {
		return validateResult{OK: false, Provider: "oidc",
			Error: "redirect_url must be https (http allowed only for http://localhost dev)"}
	}
	warnings := []string{}
	details := map[string]string{}

	// Hit the discovery doc. Wave 11 adds JWKS prefetch + token round-trip.
	discovery := strings.TrimSuffix(cfg.IssuerURL, "/") + "/.well-known/openid-configuration"
	cctx, cancel := context.WithTimeout(ctx, h.metadataTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, discovery, nil)
	if err != nil {
		return validateResult{OK: false, Provider: "oidc", Error: "bad issuer_url: " + err.Error()}
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return validateResult{OK: false, Provider: "oidc",
			Error: "discovery fetch failed: " + err.Error()}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return validateResult{OK: false, Provider: "oidc",
			Error: fmt.Sprintf("discovery returned %d", resp.StatusCode)}
	}
	var doc map[string]any
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&doc); err != nil {
		return validateResult{OK: false, Provider: "oidc", Error: "discovery body not JSON"}
	}
	for _, key := range []string{"authorization_endpoint", "token_endpoint", "jwks_uri"} {
		if _, ok := doc[key]; !ok {
			warnings = append(warnings, "discovery doc missing "+key)
		} else {
			details[key] = fmt.Sprint(doc[key])
		}
	}
	if len(cfg.Scopes) == 0 {
		warnings = append(warnings, "scopes unset — falling back to openid profile email")
	}
	return validateResult{OK: true, Provider: "oidc", Warnings: warnings, Details: details}
}

// redactSecrets scrubs known secret fields from the config JSONB
// before responses leave the service. Today: OIDC client_secret.
func redactSecrets(provider string, raw json.RawMessage) json.RawMessage {
	if provider != "oidc" {
		return raw
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return raw
	}
	if _, ok := m["client_secret"]; ok {
		m["client_secret"] = "••••••••"
	}
	out, _ := json.Marshal(m)
	return out
}

// ---- helpers --------------------------------------------------------------

func (h *SSOAdminHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *SSOAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	httpErr := vdmserr.ToHTTPError(err, r.Header.Get("X-Correlation-ID"))
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, httpErr.Code, httpErr)
}
