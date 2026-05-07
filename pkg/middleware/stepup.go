// ADR 0070 — step-up auth middleware.
//
// Wraps a handler with a check for an active step_up_grants row
// matching (tenant, user, scope-or-wildcard, expires_at > now).
// Missing or expired → 401 with X-Step-Up-Required: webauthn so
// the client can surface the step-up modal and run the passkey
// flow again.
//
// Lives in pkg/middleware so any service can wrap a sensitive
// route (legal-hold release, DSR erase approval, quarantine
// release, …) without taking an auth-service dep — the table is
// in the shared DB and RLS scopes the read by tenant.
package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"

	vdmsauth "github.com/vaultdms/vaultdms/pkg/auth"
)

// StepUpRequiredHeader — set on the 401 response so the client
// knows the failure was a missing fresh-presence (vs a missing
// session or insufficient role). Frontend reads this to decide
// whether to show the step-up modal vs redirect to login.
const StepUpRequiredHeader = "X-Step-Up-Required"

// StepUpRequiredHeaderValue — the auth method the client should
// present. "webauthn" today; future "totp_fresh" / "sso_recent".
const StepUpRequiredHeaderValue = "webauthn"

// RequireStepUp returns a middleware that checks step_up_grants
// for the authenticated caller. scope is the operation tag — pass
// "" for the catch-all "any sensitive op" gate, or a specific
// scope ("legal_hold:release") to require a grant scoped to that
// operation.
//
// pool MUST be the same DB pool the rest of the service uses;
// this middleware sets app.current_tenant on a tenant-scoped tx
// the same way the existing tenant middleware does.
func RequireStepUp(pool *pgxpool.Pool, scope string, log zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, err := vdmsauth.GetTenantID(r.Context())
			if err != nil {
				deny(w, "tenant_required")
				return
			}
			userID, err := vdmsauth.GetUserID(r.Context())
			if err != nil {
				deny(w, "user_required")
				return
			}

			ok, err := hasActiveStepUp(r.Context(), pool, tenantID, userID, scope)
			if err != nil {
				log.Error().Err(err).
					Str("tenant_id", tenantID.String()).
					Str("user_id", userID.String()).
					Str("scope", scope).
					Msg("stepup: db lookup failed")
				// Fail-CLOSED on DB error: the alternative is a
				// privilege-escalation vector if the DB returns a
				// transient error and we wave the request through.
				w.Header().Set(StepUpRequiredHeader, StepUpRequiredHeaderValue)
				http.Error(w, "step-up check failed", http.StatusServiceUnavailable)
				return
			}
			if !ok {
				deny(w, "step_up_required")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func deny(w http.ResponseWriter, reason string) {
	w.Header().Set(StepUpRequiredHeader, StepUpRequiredHeaderValue)
	http.Error(w, "step-up required: "+reason, http.StatusUnauthorized)
}

// EnforceStepUp is a handler-level helper for routes that aren't
// router-level decorated. Returns true when the gate passed; on
// false it has ALREADY written the 401 response (caller just
// returns).
//
// Reads tenant + user from the request context (populated by the
// pkg/auth-aware AuthMiddleware). Handlers that read identity
// from headers directly (e.g. the document service's callers()
// helper that reads X-Auth-Tenant-ID + X-User-ID) should use
// EnforceStepUpExplicit instead.
func EnforceStepUp(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, scope string, log zerolog.Logger) bool {
	tenantID, err := vdmsauth.GetTenantID(r.Context())
	if err != nil {
		deny(w, "tenant_required")
		return false
	}
	userID, err := vdmsauth.GetUserID(r.Context())
	if err != nil {
		deny(w, "user_required")
		return false
	}
	return EnforceStepUpExplicit(w, r, pool, tenantID, userID, scope, log)
}

// EnforceStepUpExplicit is the parameter-driven variant. Used when
// the calling handler has resolved (tenantID, userID) by some
// other path (header parse, gRPC metadata, signed JWT) and just
// wants the SQL gate.
func EnforceStepUpExplicit(
	w http.ResponseWriter,
	r *http.Request,
	pool *pgxpool.Pool,
	tenantID, userID uuid.UUID,
	scope string,
	log zerolog.Logger,
) bool {
	ok, err := hasActiveStepUp(r.Context(), pool, tenantID, userID, scope)
	if err != nil {
		log.Error().Err(err).Msg("stepup: db lookup failed")
		w.Header().Set(StepUpRequiredHeader, StepUpRequiredHeaderValue)
		http.Error(w, "step-up check failed", http.StatusServiceUnavailable)
		return false
	}
	if !ok {
		deny(w, "step_up_required")
		return false
	}
	return true
}

// hasActiveStepUp runs the same SQL the auth service's
// HasActiveStepUp method does, but inline so this package doesn't
// need to import the auth service. Tenant scoping is via SET LOCAL
// app.current_tenant on a fresh tx; FORCE RLS on step_up_grants
// makes this safe even if a future caller forgets the SET LOCAL
// (the row would be invisible to dms_app).
func hasActiveStepUp(ctx context.Context, pool *pgxpool.Pool, tenantID, userID uuid.UUID, scope string) (bool, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID.String()); err != nil {
		return false, err
	}
	var n int
	err = tx.QueryRow(ctx, `
		SELECT 1
		  FROM step_up_grants
		 WHERE tenant_id  = $1
		   AND user_id    = $2
		   AND (scope = '' OR scope = $3)
		   AND expires_at > now()
		 LIMIT 1
	`, tenantID, userID, scope).Scan(&n)
	if err != nil {
		// pgx returns a typed ErrNoRows when SELECT 1 finds nothing.
		// We can't import pgx here without polluting the dep graph;
		// the string check is good enough since the only no-rows
		// case is "user has no grant" which is the expected denial
		// path.
		if err.Error() == "no rows in result set" {
			return false, nil
		}
		return false, err
	}
	return n == 1, nil
}
