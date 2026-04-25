// Session policy admin API — Blueprint §8.1.
//
//	GET /api/v1/tenant/session-policy
//	PUT /api/v1/tenant/session-policy
//
// Reads and writes the five session_* columns added in migration
// 000002. Gated by the owner role (same bar as region-pin + CMK
// deletion — this is tenant-wide security policy, not a per-user
// setting). Admins can read to render the page but a save is
// owner-only; enforcement happens in the router via RequireRole.

package handler

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	"github.com/vaultdms/vaultdms/pkg/database"
	vdmserr "github.com/vaultdms/vaultdms/pkg/errors"
)

// SessionPolicyHandler exposes the policy CRUD.
type SessionPolicyHandler struct {
	pool *pgxpool.Pool
	log  zerolog.Logger
}

// NewSessionPolicyHandler constructs the handler.
func NewSessionPolicyHandler(pool *pgxpool.Pool, log zerolog.Logger) *SessionPolicyHandler {
	return &SessionPolicyHandler{pool: pool, log: log}
}

type sessionPolicyDTO struct {
	TTLHours                int    `json:"ttl_hours"`
	SlidingMinutes          int    `json:"sliding_minutes"`
	AbsoluteMaxDays         int    `json:"absolute_max_days"`
	ConcurrentLimit         int    `json:"concurrent_limit"`
	BindingStrictness       string `json:"binding_strictness"`
}

// GetSessionPolicy reads the five columns. Runs under AuthMiddleware
// (any authenticated user can read, so non-owner admins can render a
// read-only view) — the write path is owner-gated.
func (h *SessionPolicyHandler) GetSessionPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, _, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var dto sessionPolicyDTO
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		return tx.QueryRow(r.Context(), `
			SELECT session_ttl_hours, session_sliding_minutes,
			       session_absolute_max_days, concurrent_session_limit,
			       session_binding_strictness
			FROM organizations WHERE id = $1
		`, tenantID).Scan(
			&dto.TTLHours, &dto.SlidingMinutes, &dto.AbsoluteMaxDays,
			&dto.ConcurrentLimit, &dto.BindingStrictness,
		)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, dto)
}

// PutSessionPolicy updates the five columns. The CHECK constraints in
// migration 000002 enforce range validity; any violation surfaces as a
// Validation error to the client (400). We don't clamp silently —
// rejecting obviously bad input gives the admin a clear signal.
func (h *SessionPolicyHandler) PutSessionPolicy(w http.ResponseWriter, r *http.Request) {
	tenantID, userID, _, err := requireUser(r)
	if err != nil {
		h.writeErr(w, r, err)
		return
	}
	var in sessionPolicyDTO
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		h.writeErr(w, r, vdmserr.Validation("body", "invalid json"))
		return
	}
	// Light client-side sanity checks — the CHECK constraints below are
	// the authoritative gate.
	switch in.BindingStrictness {
	case "none", "warn", "enforce":
	default:
		h.writeErr(w, r, vdmserr.Validation("binding_strictness", "must be one of none|warn|enforce"))
		return
	}
	err = database.WithTenantTx(r.Context(), h.pool, tenantID, func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(), `
			UPDATE organizations
			SET session_ttl_hours          = $2,
			    session_sliding_minutes    = $3,
			    session_absolute_max_days  = $4,
			    concurrent_session_limit   = $5,
			    session_binding_strictness = $6
			WHERE id = $1
		`,
			tenantID,
			in.TTLHours, in.SlidingMinutes, in.AbsoluteMaxDays,
			in.ConcurrentLimit, in.BindingStrictness,
		)
		if err != nil {
			return err
		}
		// Audit so owners can track who changed security policy.
		payload, _ := json.Marshal(map[string]any{
			"actor_id":  userID.String(),
			"tenant_id": tenantID.String(),
			"policy":    in,
		})
		evt := database.NewOutboxEvent(tenantID,
			"dms.audit.session_policy_updated.v1", "tenant", tenantID, payload)
		return database.NewOutboxRepository().Insert(r.Context(), tx, evt)
	})
	if err != nil {
		h.writeErr(w, r, vdmserr.FromPgError(err))
		return
	}
	h.writeJSON(w, http.StatusOK, in)
}

// writeJSON / writeErr mirror the other admin handlers' helpers.
func (h *SessionPolicyHandler) writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (h *SessionPolicyHandler) writeErr(w http.ResponseWriter, r *http.Request, err error) {
	var e *vdmserr.Error
	if !errors.As(err, &e) {
		e = vdmserr.ErrInternal
	}
	corr := r.Header.Get("X-Correlation-ID")
	he := vdmserr.ToHTTPError(e, corr)
	h.writeJSON(w, he.Code, he)
}
