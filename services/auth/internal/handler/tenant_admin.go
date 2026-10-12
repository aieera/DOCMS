// Wave 12.7 — control-plane admin endpoints.
//
// Two tenant-scoped admin operations that previously required SQL:
//
//	PATCH /api/v1/admin/tenants/{id}/region-pin
//	  { region: "eu-west-1" }
//
//	POST /api/v1/admin/tenants/{id}/schedule-cmk-deletion
//	  { grace_hours: 24 }
//
//	POST /api/v1/admin/tenants/{id}/cancel-cmk-deletion
//
// Region-pin change rewrites the tenant default region
// (`organizations.primary_region`, via org_set_region_pin). The
// residency migrate workflow (Wave 8.4) handles actually moving
// per-document data; this endpoint only changes the tenant
// default so new uploads land in the target region.
//
// CMK scheduled-deletion sets
// `tenant_keks.scheduled_deletion_at = now() + grace` on every
// live KEK row for the tenant. The Wave 12.7b reaper runs daily
// and physically drops master material where that timestamp has
// passed.
//
// Both operations require org owner role — this is control-plane
// territory, not a compliance-officer action.
package handler

import (
	"encoding/json"
	"errors"
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
)

// TenantAdminHandler mounts /api/v1/admin/tenants/{id}/* routes.
type TenantAdminHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// NewTenantAdminHandler constructs the handler.
func NewTenantAdminHandler(pool *pgxpool.Pool, log zerolog.Logger) *TenantAdminHandler {
	return &TenantAdminHandler{pool: pool, log: log}
}

// Mount attaches routes. Caller wraps with AuthMiddleware +
// CSRFDoubleSubmit + RequireRole("owner").
func (h *TenantAdminHandler) Mount(r chi.Router) {
	r.Patch("/{id}/region-pin", h.changeRegion)
	r.Post("/{id}/schedule-cmk-deletion", h.scheduleCMKDeletion)
	r.Post("/{id}/cancel-cmk-deletion", h.cancelCMKDeletion)
}

// ---- region-pin -----------------------------------------------------------

type changeRegionBody struct {
	Region string `json:"region"`
}

func (h *TenantAdminHandler) changeRegion(w http.ResponseWriter, r *http.Request) {
	callerTenant, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	target, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	// Tenant isolation: owners operate on their own tenant only.
	// A separate platform-operator surface (not here) would drop
	// that check for cross-tenant admin.
	if target != callerTenant {
		h.writeErr(w, r, vdmserr.Forbidden("can only change your own tenant's region"))
		return
	}
	var body changeRegionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	if strings.TrimSpace(body.Region) == "" {
		h.writeErr(w, r, vdmserr.Validation("region", "required"))
		return
	}
	// dms_app has no UPDATE on organizations (document 000001 REVOKE);
	// the sanctioned write is the SECURITY DEFINER org_set_region_pin
	// (document 000111), which sets the tenant default region
	// (organizations.primary_region). Run in the tenant tx like every
	// other tenant-scoped call.
	var updated bool
	ctx := r.Context()
	err = database.WithTenantTx(ctx, h.pool, target, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx,
			`SELECT org_set_region_pin($1, $2)`, target, strings.TrimSpace(body.Region),
		).Scan(&updated)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	if !updated {
		h.writeErr(w, r, vdmserr.NotFound("tenant not found"))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":  target.String(),
		"region_pin": strings.TrimSpace(body.Region),
		"note":       "new uploads land in this region; existing documents keep their region_pin until a migrate workflow runs (Wave 8.4)",
	})
}

// ---- CMK scheduled deletion -----------------------------------------------

type scheduleCMKBody struct {
	GraceHours int `json:"grace_hours"`
}

func (h *TenantAdminHandler) scheduleCMKDeletion(w http.ResponseWriter, r *http.Request) {
	callerTenant, callerUser, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	target, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if target != callerTenant {
		h.writeErr(w, r, vdmserr.Forbidden("can only schedule on your own tenant"))
		return
	}
	var body scheduleCMKBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	// AWS KMS policy: min 7 days, max 30 days. We enforce 24h min
	// for dev convenience and cap at 30 days to match AWS.
	if body.GraceHours < 24 {
		body.GraceHours = 24
	}
	if body.GraceHours > 30*24 {
		body.GraceHours = 30 * 24
	}
	deleteAt := time.Now().UTC().Add(time.Duration(body.GraceHours) * time.Hour)

	ctx := r.Context()
	err = h.withTx(r, target, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `
			UPDATE tenant_keks
			   SET scheduled_deletion_at = $1,
			       scheduled_by          = $2
			 WHERE tenant_id = $3 AND retired_at IS NULL`,
			deleteAt, callerUser, target,
		)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("no live KEKs for tenant")
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, map[string]any{
		"tenant_id":             target.String(),
		"scheduled_deletion_at": deleteAt.Format(time.RFC3339),
		"grace_hours":           body.GraceHours,
		"cancel_before":         deleteAt.Format(time.RFC3339),
		"note":                  "existing ciphertext becomes unreadable after this timestamp; cancel to abort within the grace window",
	})
}

func (h *TenantAdminHandler) cancelCMKDeletion(w http.ResponseWriter, r *http.Request) {
	callerTenant, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	target, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.writeErr(w, r, vdmserr.Validation("id", "not a uuid"))
		return
	}
	if target != callerTenant {
		h.writeErr(w, r, vdmserr.Forbidden("can only cancel on your own tenant"))
		return
	}
	ctx := r.Context()
	err = h.withTx(r, target, func(tx pgx.Tx) error {
		tag, execErr := tx.Exec(ctx, `
			UPDATE tenant_keks
			   SET scheduled_deletion_at = NULL,
			       scheduled_by          = NULL
			 WHERE tenant_id = $1 AND scheduled_deletion_at IS NOT NULL
			   AND scheduled_deletion_at > now()`,
			target,
		)
		if execErr != nil {
			return execErr
		}
		if tag.RowsAffected() == 0 {
			return vdmserr.NotFound("no pending deletion for tenant (grace window may have already expired)")
		}
		return nil
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ---- helpers --------------------------------------------------------------

// withTx runs fn inside the target tenant's tx. tenant_keks is
// RLS-protected: in a plain tx the production NOBYPASSRLS role matched no
// row and every schedule/cancel answered "no live KEKs" (M-27). Callers
// have already checked target == caller's tenant.
func (h *TenantAdminHandler) withTx(r *http.Request, target uuid.UUID, fn func(tx pgx.Tx) error) error {
	return database.WithTenantTx(r.Context(), h.pool, target, fn)
}

func (h *TenantAdminHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *TenantAdminHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var e *vdmserr.Error
	if !errors.As(err, &e) {
		e = vdmserr.ErrInternal
	}
	corr := r.Header.Get("X-Correlation-ID")
	httpErr := vdmserr.ToHTTPError(err, corr)
	if httpErr.Code == 0 {
		httpErr.Code = http.StatusInternalServerError
	}
	h.writeJSON(w, httpErr.Code, httpErr)
}
