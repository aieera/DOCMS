package handler

import (
	"encoding/json"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
	"github.com/vaultdms/vaultdms/pkg/regionenforcer"
)

// Tenant residency policy + workspace-region listing endpoints.
//
// The /admin/tenant/residency frontend page consumes three calls:
//
//	GET  /api/v1/tenant/residency-policy   — read allowed_regions + default_region_pin
//	PUT  /api/v1/tenant/residency-policy   — owner-gated write
//	GET  /api/v1/tenant/workspace-regions  — per-workspace region overrides
//
// The data lives in `organizations` (Wave 16 migration 000014) and
// `workspaces` (initial schema). Both tables sit in the shared
// document-service Postgres database; auth reads them via its own
// pgxpool — RLS is unchanged because the queries run inside
// WithTenantTx which sets app.current_tenant per request.

// tenantResidencyPolicyDTO mirrors the frontend's TenantResidencyPolicy
// in web/src/api/residency.ts so the JSON shape round-trips without
// remapping. allowed_regions=null is serialized as the empty array
// (NULL means "no restriction" in the schema, but the UI is happier
// with an empty list).
type tenantResidencyPolicyDTO struct {
	AllowedRegions   []string `json:"allowed_regions"`
	DefaultRegionPin string   `json:"default_region_pin"`
}

// GetTenantResidencyPolicy returns the org's residency policy.
// Any authenticated user can read it — non-admins use this for their
// upload region picker so the dropdown disables values outside the
// allowlist before round-tripping to the backend.
func (h *Handler) GetTenantResidencyPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var (
		allowed []string
		def     string
	)
	err = database.WithTenantTx(r.Context(), h.sessionPolicy.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT COALESCE(allowed_regions, '{}'::text[]),
			       COALESCE(default_region_pin, 'us-east-1')
			  FROM organizations WHERE id = $1
		`, tenantID).Scan(&allowed, &def)
	})
	if err != nil {
		h.writeError(w, r, vdmserr.FromPgError(err))
		return
	}
	if allowed == nil {
		allowed = []string{}
	}
	h.writeJSON(w, http.StatusOK, tenantResidencyPolicyDTO{
		AllowedRegions: allowed, DefaultRegionPin: def,
	})
}

// PutTenantResidencyPolicy updates allowed_regions + default_region_pin.
// Owner-only — handler is registered behind RequireRole("owner") in
// router.go, but we double-check here so a misconfigured route never
// bypasses the gate. Validates each region against
// pkg/regionenforcer's known list (mirrored against the
// supported_regions seed via TestBoundariesMatchMigration in CI).
func (h *Handler) PutTenantResidencyPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _, role, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	if role != "owner" {
		h.writeError(w, r, vdmserr.Forbidden("owner role required"))
		return
	}
	var in tenantResidencyPolicyDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		h.writeError(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	for _, region := range append([]string{in.DefaultRegionPin}, in.AllowedRegions...) {
		if region == "" {
			continue
		}
		if !regionenforcer.IsKnown(region) {
			h.writeError(w, r, vdmserr.Validation("region",
				"unsupported region: "+region))
			return
		}
	}
	if in.DefaultRegionPin == "" {
		h.writeError(w, r, vdmserr.Validation("default_region_pin", "required"))
		return
	}
	// Defense-in-depth: a default outside the allowlist would brick
	// every new upload. Fail fast at the API rather than wait for
	// resolveRegionForCreate to return REGION_VIOLATION on every
	// subsequent doc create.
	if len(in.AllowedRegions) > 0 {
		ok := false
		for _, a := range in.AllowedRegions {
			if a == in.DefaultRegionPin {
				ok = true
				break
			}
		}
		if !ok {
			h.writeError(w, r, vdmserr.Validation("default_region_pin",
				"must appear in allowed_regions"))
			return
		}
	}
	err = database.WithTenantTx(r.Context(), h.sessionPolicy.pool, tenantID, func(tx pgx.Tx) error {
		// allowed_regions=NULL when the input list is empty so the
		// "no restriction" semantic is preserved — Postgres CHECK
		// constraints allow this and resolveRegionForCreate skips
		// the allowlist check when NULL.
		var allowedArg any
		if len(in.AllowedRegions) == 0 {
			allowedArg = nil
		} else {
			allowedArg = in.AllowedRegions
		}
		_, err := tx.Exec(r.Context(), `
			UPDATE organizations
			   SET allowed_regions = $1::text[],
			       default_region_pin = $2
			 WHERE id = $3
		`, allowedArg, in.DefaultRegionPin, tenantID)
		return err
	})
	if err != nil {
		h.writeError(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, in)
}

// workspaceRegionDTO mirrors web/src/api/residency.ts:WorkspaceRegion.
type workspaceRegionDTO struct {
	WorkspaceID   uuid.UUID `json:"workspace_id"`
	WorkspaceName string    `json:"workspace_name"`
	RegionPin     string    `json:"region_pin"`
}

// ListWorkspaceRegions surfaces every workspace + its region_pin so the
// admin UI can render the per-workspace override section. Open to all
// authenticated users for now — the data isn't sensitive, and the UI
// hides the section behind AdminGuard anyway.
func (h *Handler) ListWorkspaceRegions(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	out := []workspaceRegionDTO{}
	err = database.WithTenantTx(r.Context(), h.sessionPolicy.pool, tenantID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT id, name, COALESCE(region_pin, '')
			  FROM workspaces
			 WHERE tenant_id = $1
			 ORDER BY name ASC
		`, tenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var row workspaceRegionDTO
			if err := rows.Scan(&row.WorkspaceID, &row.WorkspaceName, &row.RegionPin); err != nil {
				return err
			}
			out = append(out, row)
		}
		return rows.Err()
	})
	if err != nil {
		h.writeError(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, out)
}
