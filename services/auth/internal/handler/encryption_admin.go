// External-KMS / encryption admin API (§5/§8). Lets a tenant admin register an
// external KMS key (Vault transit / AWS KMS / Azure Key Vault) as their KEK
// source, rotate it (new version; existing ciphertext still decrypts under the
// prior version — DEK rewrap is the separate `dms-admin kms rewrap` step),
// revoke a version (break-glass), and view key status.
//
//	GET  /api/v1/admin/encryption/status
//	POST /api/v1/admin/encryption/register   {provider, external_key_ref}
//	POST /api/v1/admin/encryption/rotate     {provider?, external_key_ref?}
//	POST /api/v1/admin/encryption/revoke     {version}
//
// Wrapped by the router with AuthMiddleware + CSRFDoubleSubmit + admin/owner.
package handler

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/pkg/database"
	vdmserr "github.com/aieera/sedoc/pkg/errors"
)

type EncryptionAdminHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

func NewEncryptionAdminHandler(pool *pgxpool.Pool, log zerolog.Logger) *EncryptionAdminHandler {
	return &EncryptionAdminHandler{pool: pool, log: log}
}

func (h *EncryptionAdminHandler) Mount(r chi.Router) {
	r.Get("/status", h.status)
	r.Post("/register", h.register)
	r.Post("/rotate", h.rotate)
	r.Post("/revoke", h.revoke)
}

var validKMSProviders = map[string]bool{"local": true, "vault": true, "aws_kms": true, "azure_kv": true}

func (h *EncryptionAdminHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *EncryptionAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	e := vdmserr.ToHTTPError(err, r.Header.Get("X-Correlation-ID"))
	if e.Code == 0 {
		e.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, e.Code, e)
}

type kekVersionDTO struct {
	Version        int        `json:"version"`
	Alias          string     `json:"alias"`
	Provider       string     `json:"provider"`
	ExternalKeyRef string     `json:"external_key_ref,omitempty"`
	CreatedAt      time.Time  `json:"created_at"`
	RetiredAt      *time.Time `json:"retired_at,omitempty"`
	RevokedAt      *time.Time `json:"revoked_at,omitempty"`
	Active         bool       `json:"active"`
}

// status lists the tenant's KEK versions (newest first). Active = the live,
// non-retired, non-revoked version DEKs are wrapped under.
func (h *EncryptionAdminHandler) status(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	out := []kekVersionDTO{}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		rows, e := tx.Query(r.Context(), `
			SELECT version, alias, provider, COALESCE(external_key_ref,''),
			       created_at, retired_at, revoked_at
			  FROM tenant_keks WHERE tenant_id = $1 ORDER BY version DESC`, tenantID)
		if e != nil {
			return e
		}
		defer rows.Close()
		for rows.Next() {
			var d kekVersionDTO
			if e := rows.Scan(&d.Version, &d.Alias, &d.Provider, &d.ExternalKeyRef,
				&d.CreatedAt, &d.RetiredAt, &d.RevokedAt); e != nil {
				return e
			}
			d.Active = d.RetiredAt == nil && d.RevokedAt == nil
			out = append(out, d)
		}
		return rows.Err()
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"versions": out})
}

type registerBody struct {
	Provider       string `json:"provider"`
	ExternalKeyRef string `json:"external_key_ref"`
}

// register records the external KMS provider + key reference on the tenant's
// live KEK version — declaring the external key as the KEK source. The kekID/
// alias is unchanged (the wrap handle); this documents which external key
// backs it and drives rotation + the admin UI.
func (h *EncryptionAdminHandler) register(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body registerBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if !validKMSProviders[body.Provider] {
		h.writeErr(w, r, vdmserr.Validation("provider", "must be local|vault|aws_kms|azure_kv"))
		return
	}
	if body.Provider != "local" && body.ExternalKeyRef == "" {
		h.writeErr(w, r, vdmserr.Validation("external_key_ref", "required for an external provider"))
		return
	}
	var updated int64
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(), `
			UPDATE tenant_keks SET provider = $2, external_key_ref = NULLIF($3,'')
			 WHERE tenant_id = $1 AND retired_at IS NULL AND revoked_at IS NULL`,
			tenantID, body.Provider, body.ExternalKeyRef)
		if e != nil {
			return e
		}
		updated = ct.RowsAffected()
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	if updated == 0 {
		h.writeErr(w, r, vdmserr.NotFound("no active KEK version; provision one first (dms-admin kms create)"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "registered", "provider": body.Provider})
}

// rotate retires the live version and mints v+1 carrying the (optionally
// updated) provider/ref. Existing ciphertext still decrypts under the prior
// version; DEK rewrap under the new version is the `dms-admin kms rewrap`
// step. Emits dms.tenant.key_rotated.v1.
func (h *EncryptionAdminHandler) rotate(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body registerBody // provider + external_key_ref both optional on rotate
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Provider != "" && !validKMSProviders[body.Provider] {
		h.writeErr(w, r, vdmserr.Validation("provider", "must be local|vault|aws_kms|azure_kv"))
		return
	}
	var liveVer, newVer int
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		var provider, ref string
		if e := tx.QueryRow(r.Context(), `
			SELECT version, provider, COALESCE(external_key_ref,'')
			  FROM tenant_keks WHERE tenant_id=$1 AND retired_at IS NULL AND revoked_at IS NULL
			 ORDER BY version DESC LIMIT 1`, tenantID).Scan(&liveVer, &provider, &ref); e != nil {
			if e == pgx.ErrNoRows {
				return vdmserr.NotFound("no live KEK to rotate (dms-admin kms create first)")
			}
			return e
		}
		if body.Provider != "" {
			provider = body.Provider
		}
		if body.ExternalKeyRef != "" {
			ref = body.ExternalKeyRef
		}
		if _, e := tx.Exec(r.Context(),
			`UPDATE tenant_keks SET retired_at = now() WHERE tenant_id=$1 AND version=$2`,
			tenantID, liveVer); e != nil {
			return e
		}
		newVer = liveVer + 1
		newAlias := fmt.Sprintf("vaultdms/tenant/%s@v%d", tenantID.String(), newVer)
		if _, e := tx.Exec(r.Context(), `
			INSERT INTO tenant_keks (tenant_id, version, alias, provider, external_key_ref, created_at)
			VALUES ($1, $2, $3, $4, NULLIF($5,''), now())`,
			tenantID, newVer, newAlias, provider, ref); e != nil {
			return e
		}
		evt := database.NewOutboxEvent(tenantID, "dms.tenant.key_rotated.v1", "tenant", tenantID, mustJSON(map[string]any{
			"tenant_id":       tenantID.String(),
			"retired_version": liveVer,
			"live_version":    newVer,
			"provider":        provider,
			"rotated_by":      actorID.String(),
		}))
		return database.NewOutboxRepository().Insert(r.Context(), tx, evt)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"status":          "rotated",
		"retired_version": liveVer,
		"live_version":    newVer,
		"note":            "existing ciphertext still decrypts under the prior version; run `dms-admin kms rewrap` to re-wrap DEKs under the new version (blobs are NOT re-encrypted).",
	})
}

type revokeBody struct {
	Version int `json:"version"`
}

// revoke marks a KEK version unusable (break-glass). A revoked version must
// never wrap/unwrap — the intent is to make data wrapped under it inaccessible.
func (h *EncryptionAdminHandler) revoke(w http.ResponseWriter, r *http.Request) {
	tenantID, actorID, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var body revokeBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Version <= 0 {
		h.writeErr(w, r, vdmserr.Validation("version", "positive version required"))
		return
	}
	var affected int64
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		ct, e := tx.Exec(r.Context(),
			`UPDATE tenant_keks SET revoked_at = now(), revoked_by = $3
			  WHERE tenant_id = $1 AND version = $2 AND revoked_at IS NULL`,
			tenantID, body.Version, actorID)
		if e != nil {
			return e
		}
		affected = ct.RowsAffected()
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	if affected == 0 {
		h.writeErr(w, r, vdmserr.NotFound("version not found or already revoked"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{"status": "revoked", "version": body.Version})
}

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }
