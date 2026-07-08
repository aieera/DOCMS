// Microsoft 365 → SeDoc session exchange (ADR 0112).
//
// The Outlook add-in obtains an Entra ID token via
// OfficeRuntime.auth.getAccessToken and posts it here. We:
//  1. Call Graph /me with the bearer to verify the token + read
//     the authenticated user's primary email.
//  2. Look up SeDoc users by that email. If exactly one match,
//     issue a session and return its plaintext token. If >1, surface
//     a 409 with the candidate list so the add-in can prompt.
//  3. If zero matches, 404. We DO NOT auto-provision — that would
//     bypass the tenant-admin invite flow + the SSO mapping that
//     ADR 0061 + 0062 already provide.
//
// Why call Graph instead of validating the JWT?
//   - The auth service already speaks HTTPS to outside the cluster
//     for SAML/OIDC; adding Graph is a small marginal cost.
//   - Validating the Entra JWT means keeping JWKS up to date,
//     handling key rollover, and minting a JWT validator per Entra
//     tenant (since the multi-tenant `common` audience changes per
//     directory). The Graph round-trip dodges all of that —
//     Microsoft validates the token for us as a side-effect of
//     the /me read.
//   - Cost: one 100-300 ms round-trip on session establishment,
//     which the add-in caches for the lifetime of the taskpane.
package service

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/aieera/sedoc/pkg/database"
)

// M365ExchangeResult is what the handler returns to the add-in on
// the happy path.
type M365ExchangeResult struct {
	SessionToken string
	UserID       string
	TenantID     string
	Email        string
}

// ErrM365NoVDMSUser is returned when no SeDoc user matches the
// Entra user's email. Handler translates to 404.
var ErrM365NoVDMSUser = errors.New("m365: no SeDoc user with this email")

// ErrM365NotConfigured is returned when SEDOC_M365_AUDIENCE /
// SEDOC_M365_ALLOWED_TIDS are unset. Handler translates to 503.
var ErrM365NotConfigured = errors.New("m365: exchange not configured")

// ErrM365TokenInvalid wraps every Entra JWT rejection. Handler
// translates to 401 so the add-in re-acquires a token and retries.
var ErrM365TokenInvalid = errors.New("m365: invalid access token")

// ErrM365MultipleTenants is returned when the email exists in more
// than one SeDoc tenant. Handler translates to 409 + a list.
type ErrM365MultipleTenants struct {
	Candidates []M365TenantCandidate
}

// M365TenantCandidate is one of N possible homes for an
// ambiguous-email exchange.
type M365TenantCandidate struct {
	TenantID   string `json:"tenant_id"`
	TenantSlug string `json:"tenant_slug"`
	TenantName string `json:"tenant_name"`
	UserID     string `json:"user_id"`
}

func (e *ErrM365MultipleTenants) Error() string {
	return fmt.Sprintf("m365: email belongs to %d SeDoc tenants", len(e.Candidates))
}

// ExchangeM365Token runs the three-step exchange flow.
//
// `preferredTenantID` is optional — when non-empty, the lookup is
// scoped to that tenant. The add-in passes this after the user
// picks one of the 409 candidates so the second call goes through.
func (s *Service) ExchangeM365Token(ctx context.Context, msAccessToken, ip, userAgent, preferredTenantID string) (*M365ExchangeResult, error) {
	if msAccessToken == "" {
		return nil, errors.New("ms_access_token required")
	}

	// SECURITY: validate the Entra ID JWT BEFORE doing anything else.
	// The pre-audit flow trusted Graph /me's `mail` field to identify
	// the caller, which let an attacker on any Entra tenant they
	// control set `mail` to a victim's address and impersonate them.
	// We now require:
	//   * Signature verified against the issuing directory's JWKS
	//   * `aud` == SEDOC_M365_AUDIENCE
	//   * `tid` in SEDOC_M365_ALLOWED_TIDS allow-list
	//   * `exp` / `nbf` within tolerance
	// Graph /me is NO LONGER called. The verified email comes from
	// the JWT's `email`/`preferred_username` claim, which Microsoft
	// populates from the user's UPN (verified domain), not the free-
	// form `mail` directory attribute. (tid, oid) is logged for the
	// eventual link-table migration.
	verified, verr := s.m365.verify(ctx, msAccessToken)
	if verr != nil {
		if errors.Is(verr, ErrM365NotConfigured) {
			return nil, verr
		}
		return nil, fmt.Errorf("%w: %v", ErrM365TokenInvalid, verr)
	}
	email := verified.Email
	if email == "" {
		return nil, errors.New("m365: verified token has no email claim")
	}
	s.log.Info().
		Str("entra_tid", verified.TID).
		Str("entra_oid", verified.OID).
		Msg("m365 token verified; mapping by email until link-table migration lands")

	candidates, err := s.findUsersByEmailAcrossTenants(ctx, email, preferredTenantID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, ErrM365NoVDMSUser
	}
	if len(candidates) > 1 && preferredTenantID == "" {
		return nil, &ErrM365MultipleTenants{Candidates: candidates}
	}

	chosen := candidates[0]
	tenantID, terr := uuid.Parse(chosen.TenantID)
	if terr != nil {
		return nil, fmt.Errorf("bad tenant_id from DB: %w", terr)
	}
	userID, uerr := uuid.Parse(chosen.UserID)
	if uerr != nil {
		return nil, fmt.Errorf("bad user_id from DB: %w", uerr)
	}

	var session *CreatedSession
	err = database.WithTenantTx(ctx, s.pool, tenantID, func(tx pgx.Tx) error {
		u, gerr := s.users.GetByID(ctx, tx, tenantID, userID)
		if gerr != nil {
			return gerr
		}
		// Match the standard login flow's last-login bookkeeping so
		// the audit log doesn't show this user as "never logged in"
		// just because they prefer the add-in.
		_ = s.users.UpdateLastLogin(ctx, tx, u.TenantID, u.ID, time.Now().UTC())
		cs, serr := s.createSessionInTx(ctx, tx, u, ip, userAgent)
		if serr != nil {
			return serr
		}
		session = cs
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &M365ExchangeResult{
		SessionToken: session.Token,
		UserID:       session.Session.UserID.String(),
		TenantID:     session.Session.TenantID.String(),
		Email:        email,
	}, nil
}

// findUsersByEmailAcrossTenants resolves an email to its tenant(s) for
// the m365/Outlook exchange. `preferredTenantID`, when non-empty,
// narrows the search.
//
// This is a legitimately cross-tenant PRE-TENANT read (an email may
// exist in multiple tenants → 409 disambiguation). users is FORCE RLS,
// so under the dms_app (NOBYPASSRLS) role a raw read yields zero rows
// and every exchange 404s. Route through the SECURITY DEFINER
// exact-match function (auth migration 000001, issue #75) — the earlier
// "auth uses a BYPASSRLS role" comment was wrong; the prod app role is
// NOBYPASSRLS and this function is the sanctioned, minimal bypass.
func (s *Service) findUsersByEmailAcrossTenants(ctx context.Context, email, preferredTenantID string) ([]M365TenantCandidate, error) {
	var pref any // NULL narrows nothing
	if preferredTenantID != "" {
		pref = preferredTenantID
	}
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id, tenant_slug, tenant_name, user_id
		 FROM auth_lookup_users_by_email($1, $2)`, email, pref)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []M365TenantCandidate
	for rows.Next() {
		var c M365TenantCandidate
		if err := rows.Scan(&c.TenantID, &c.TenantSlug, &c.TenantName, &c.UserID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// graphMe and firstNonEmpty were removed after the security audit
// rewrote ExchangeM365Token to authenticate exclusively from the
// JWT (see m365_jwt.go). Microsoft Graph /me's `mail` attribute is
// not domain-verified cross-tenant, so trusting it allowed account
// takeover. The verified `email` / `preferred_username` JWT claim
// now drives the email-based SeDoc user lookup, and a future
// migration will replace email mapping with a (tid, oid) link
// table per the recommendation in the audit report.

func snippet(b []byte) string {
	const max = 300
	if len(b) <= max {
		return string(b)
	}
	return string(b[:max]) + "…"
}
