// SCIM admin surface (§8). Lets a tenant admin see the SCIM base URL, rotate
// the per-tenant bearer token, and read the provisioning log.
//
//	GET  /api/v1/admin/scim/info            → { slug, configured }
//	POST /api/v1/admin/scim/token/rotate    → { token }  (plaintext, shown once)
//	GET  /api/v1/admin/scim/log             → { entries: [...] }
package handler

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

type SCIMAdminHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

func NewSCIMAdminHandler(pool *pgxpool.Pool, log zerolog.Logger) *SCIMAdminHandler {
	return &SCIMAdminHandler{pool: pool, log: log}
}

func (h *SCIMAdminHandler) Mount(r chi.Router) {
	r.Get("/info", h.info)
	r.Post("/token/rotate", h.rotate)
	r.Get("/log", h.provisioningLog)
}

func (h *SCIMAdminHandler) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *SCIMAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	httpErr := vdmserr.ToHTTPError(err, r.Header.Get("X-Correlation-ID"))
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, httpErr.Code, httpErr)
}

// info returns the tenant slug (for building the SCIM base URL) and whether a
// SCIM token is currently configured.
func (h *SCIMAdminHandler) info(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var slug string
	var configured, ssoActive bool
	_ = h.pool.QueryRow(r.Context(),
		`SELECT slug FROM organizations WHERE id = $1`, tenantID).Scan(&slug)
	_ = h.pool.QueryRow(r.Context(), `
		SELECT EXISTS (SELECT 1 FROM sso_configs WHERE tenant_id=$1 AND is_active AND config ? 'scim_token_hash')`,
		tenantID).Scan(&configured)
	// sso_active tells the admin panel whether there's an active SSO config
	// to attach a token to. Without one, rotate 409s ("register an IdP
	// first"), so the UI gates the button on this rather than inviting a
	// doomed click. Distinct from `configured` (a token is already set).
	_ = h.pool.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM sso_configs WHERE tenant_id=$1 AND is_active)`,
		tenantID).Scan(&ssoActive)
	h.writeJSON(w, http.StatusOK, map[string]any{
		"slug":       slug,
		"configured": configured,
		"sso_active": ssoActive,
		"base_path":  "/api/v1/scim/v2/" + slug,
	})
}

// rotate generates a new SCIM bearer token, stores its SHA-256 hash on the
// tenant's most-recent active SSO config (where the resolver reads it), and
// returns the plaintext ONCE. Fails clearly if the tenant has no active SSO
// config to attach the token to (register an IdP first).
func (h *SCIMAdminHandler) rotate(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		h.writeErr(w, r, err)
		return
	}
	token := "scim_" + hex.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(sum[:])

	var updated int64
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(), `
			UPDATE sso_configs
			   SET config = jsonb_set(config, '{scim_token_hash}', to_jsonb($2::text)), updated_at = now()
			 WHERE tenant_id = $1
			   AND id = (SELECT id FROM sso_configs WHERE tenant_id=$1 AND is_active ORDER BY updated_at DESC LIMIT 1)`,
			tenantID, hash)
		if e != nil {
			return e
		}
		updated = ct.RowsAffected()
		return nil
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	if updated == 0 {
		h.writeJSON(w, http.StatusConflict, map[string]any{
			"error": "no active SSO config to attach the SCIM token to — register an IdP first",
		})
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"token": token})
}

// provisioningLog returns the recent provisioning-log entries for the panel.
func (h *SCIMAdminHandler) provisioningLog(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	type entry struct {
		ID           string     `json:"id"`
		Action       string     `json:"action"`
		ResourceType string     `json:"resource_type"`
		ExternalID   string     `json:"external_id,omitempty"`
		UserID       *uuid.UUID `json:"user_id,omitempty"`
		Detail       string     `json:"detail,omitempty"`
		CreatedAt    string     `json:"created_at"`
	}
	out := []entry{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `
			SELECT id::text, action, resource_type, COALESCE(external_id,''), user_id,
			       COALESCE(detail,''), to_char(created_at, 'YYYY-MM-DD"T"HH24:MI:SSZ')
			  FROM scim_provisioning_log WHERE tenant_id = $1
			 ORDER BY created_at DESC LIMIT 100`, tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var en entry
			if e := rows.Scan(&en.ID, &en.Action, &en.ResourceType, &en.ExternalID, &en.UserID, &en.Detail, &en.CreatedAt); e != nil {
				return e
			}
			out = append(out, en)
		}
		return rows.Err()
	})
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}
