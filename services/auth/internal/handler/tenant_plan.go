package handler

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// GetTenantPlan exposes the caller tenant's plan identifier so the
// uploader can render tier-aware hints ("5 GB / Standard"). The column
// is already persisted on organizations; this endpoint is the only
// thing the web tier needs to ask for it.
//
// Any authenticated user can read their own tenant's plan — it's not
// sensitive and the UI surfaces derive from it (upload limit tooltip,
// billing CTAs). Writes happen through /admin/billing flows elsewhere.
func (h *Handler) GetTenantPlan(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	var plan string
	err = database.WithTenantTx(r.Context(), h.sessionPolicy.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(),
			`SELECT COALESCE(plan, 'standard') FROM organizations WHERE id = $1`,
			tenantID,
		).Scan(&plan)
	})
	if err != nil {
		h.writeError(w, r, vdmserr.FromPgError(err))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"plan": plan})
}
