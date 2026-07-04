// Google Workspace → SeDoc session exchange (ADR 0116).
//
// The Google Workspace Add-on (Docs + Gmail) obtains an OpenID Connect
// ID token via Apps Script `ScriptApp.getIdentityToken()` and posts it
// here. The flow mirrors ExchangeM365Token exactly:
//  1. Verify the ID token (signature, aud, iss, exp) and require a
//     verified email on an allow-listed Workspace domain — google_jwt.go.
//  2. Look up SeDoc users by that email across tenants. Exactly one
//     match → issue a session. More than one → 409 + candidate list so
//     the add-on can prompt. Zero → 404 (we never auto-provision).
//
// The cross-tenant lookup + session minting are shared with the M365
// path (findUsersByEmailAcrossTenants, createSessionInTx) — only the
// token-verification front-end differs per provider.
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

// TenantCandidate is one possible home for an ambiguous-email exchange.
// Aliased to the M365 type so both add-in flows share one wire shape
// (its JSON tags carry no provider name); see M365TenantCandidate.
type TenantCandidate = M365TenantCandidate

// GoogleExchangeResult is what the handler returns to the add-on on the
// happy path.
type GoogleExchangeResult struct {
	SessionToken string
	UserID       string
	TenantID     string
	Email        string
}

// ErrGoogleNoVDMSUser is returned when no SeDoc user matches the Google
// user's email. Handler translates to 404.
var ErrGoogleNoVDMSUser = errors.New("google: no SeDoc user with this email")

// ErrGoogleNotConfigured is returned when SEDOC_GOOGLE_AUDIENCE /
// SEDOC_GOOGLE_ALLOWED_HDS are unset. Handler translates to 503 so a
// misconfigured deploy is distinguishable from a crash.
var ErrGoogleNotConfigured = errors.New("google: exchange not configured")

// ErrGoogleTokenInvalid wraps every ID-token rejection (bad signature,
// wrong aud, expired, unverified email, hd not allow-listed). Handler
// translates to 401 so the add-on knows to refresh the identity token
// and retry rather than treating it as an outage.
var ErrGoogleTokenInvalid = errors.New("google: invalid id token")

// ErrGoogleMultipleTenants is returned when the email exists in more
// than one SeDoc tenant. Handler translates to 409 + a list.
type ErrGoogleMultipleTenants struct {
	Candidates []TenantCandidate
}

func (e *ErrGoogleMultipleTenants) Error() string {
	return fmt.Sprintf("google: email belongs to %d SeDoc tenants", len(e.Candidates))
}

// ExchangeGoogleToken runs the verify → lookup → mint-session flow.
//
// `preferredTenantID` is optional — when non-empty the lookup is scoped
// to that tenant, which the add-on passes after the user picks one of
// the 409 candidates so the second call goes through.
func (s *Service) ExchangeGoogleToken(ctx context.Context, googleIDToken, ip, userAgent, preferredTenantID string) (*GoogleExchangeResult, error) {
	if googleIDToken == "" {
		return nil, errors.New("google_id_token required")
	}

	verified, verr := s.google.verify(ctx, googleIDToken)
	if verr != nil {
		if errors.Is(verr, ErrGoogleNotConfigured) {
			return nil, verr
		}
		return nil, fmt.Errorf("%w: %v", ErrGoogleTokenInvalid, verr)
	}
	email := verified.Email
	if email == "" {
		return nil, errors.New("google: verified token has no email claim")
	}
	s.log.Info().
		Str("google_hd", verified.HD).
		Str("google_sub", verified.Sub).
		Msg("google token verified; mapping by email until link-table migration lands")

	candidates, err := s.findUsersByEmailAcrossTenants(ctx, email, preferredTenantID)
	if err != nil {
		return nil, err
	}
	if len(candidates) == 0 {
		return nil, ErrGoogleNoVDMSUser
	}
	if len(candidates) > 1 && preferredTenantID == "" {
		return nil, &ErrGoogleMultipleTenants{Candidates: candidates}
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
		// Match the standard login flow's last-login bookkeeping so the
		// audit log doesn't show this user as "never logged in" just
		// because they prefer the add-on.
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
	return &GoogleExchangeResult{
		SessionToken: session.Token,
		UserID:       session.Session.UserID.String(),
		TenantID:     session.Session.TenantID.String(),
		Email:        email,
	}, nil
}
