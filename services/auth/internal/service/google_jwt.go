// Google Workspace ID-token validation (ADR 0116) — the Google add-on
// analogue of m365_jwt.go.
//
// The Google Workspace Add-on obtains an OpenID Connect ID token for
// the signed-in user via Apps Script `ScriptApp.getIdentityToken()`
// and posts it to POST /api/v1/auth/google/exchange. We validate that
// the token:
//  1. Has a valid RS256 signature against Google's JWKS (handled by
//     go-oidc, which caches keys + handles rollover).
//  2. Has `iss` == https://accounts.google.com (go-oidc enforces this
//     against the discovery document).
//  3. Has `aud` equal to the add-on's OAuth 2.0 client ID
//     (SEDOC_GOOGLE_AUDIENCE).
//  4. Has unexpired `exp` / valid `nbf` / `iat` (go-oidc enforces).
//  5. Carries a verified `email` (`email_verified` == true) whose
//     Workspace hosted domain (`hd`) is on the SeDoc allow-list
//     (SEDOC_GOOGLE_ALLOWED_HDS).
//
// Google issues ID tokens from a SINGLE issuer, so — unlike the M365
// verifier's per-`tid` provider map — one cached provider suffices.
// The security boundary that the M365 code gets from its `tid`
// allow-list, we get from the `hd` (hosted-domain) allow-list:
// requiring `hd` rejects personal @gmail.com accounts and any Google
// Workspace domain a SeDoc operator hasn't explicitly trusted.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/coreos/go-oidc/v3/oidc"
)

// Google environment variables. Read once at New() time so tests can
// swap them without touching the host env.
const (
	envGoogleAudience   = "SEDOC_GOOGLE_AUDIENCE"
	envGoogleAllowedHDs = "SEDOC_GOOGLE_ALLOWED_HDS"

	googleIssuer = "https://accounts.google.com"
)

// VerifiedGoogleIdentity is the result of a successful ID-token
// validation. Callers should prefer `Sub` (Google's stable per-user
// identifier) for identity decisions and use `Email` only as the
// mapping key until a (hd, sub) link-table lands, mirroring the M365
// (tid, oid) plan.
type VerifiedGoogleIdentity struct {
	Sub   string // Stable per-user Google account id (the `sub` claim).
	Email string // Verified `email` claim (email_verified == true).
	HD    string // Google Workspace hosted domain (verified, allow-listed).
}

// googleVerifier holds the cached oidc.Provider plus the configured
// audience + hosted-domain allow-list. Methods on *Service delegate here.
type googleVerifier struct {
	audience   string
	allowedHDs map[string]struct{}
	providers  sync.Map // constant googleIssuer key → *oidc.Provider
}

// newGoogleVerifier reads the two env vars once. A missing audience or
// an empty allow-list disables the Google exchange path entirely —
// handlers surface a 503 rather than falling back to insecure behavior.
func newGoogleVerifier() *googleVerifier {
	v := &googleVerifier{
		audience:   strings.TrimSpace(os.Getenv(envGoogleAudience)),
		allowedHDs: map[string]struct{}{},
	}
	for _, d := range strings.Split(os.Getenv(envGoogleAllowedHDs), ",") {
		d = strings.TrimSpace(strings.ToLower(d))
		if d != "" {
			v.allowedHDs[d] = struct{}{}
		}
	}
	return v
}

// configured reports whether Google exchange is operational. Returns
// false when either env var is missing — handlers must surface this as
// "feature disabled" rather than silently accepting tokens.
func (v *googleVerifier) configured() bool {
	return v != nil && v.audience != "" && len(v.allowedHDs) > 0
}

// jsonBool decodes a JSON boolean OR its string form ("true"/"false").
// Google's OIDC ID tokens send `email_verified` as a real boolean, but
// some Google token surfaces have historically sent the string form;
// accepting both keeps a legitimate token from being rejected on a type
// mismatch while still failing closed on anything that isn't truthy.
type jsonBool bool

func (b *jsonBool) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true", `"true"`:
		*b = true
	case "false", `"false"`, "null", `""`:
		*b = false
	default:
		return fmt.Errorf("google: email_verified: unexpected value %q", string(data))
	}
	return nil
}

// googleClaims is the subset of ID-token claims we consume. `aud`/`iss`/
// `exp` are enforced by go-oidc's verifier before we decode, so they are
// deliberately not re-read here.
type googleClaims struct {
	Sub           string   `json:"sub"`
	Email         string   `json:"email"`
	EmailVerified jsonBool `json:"email_verified"`
	HD            string   `json:"hd"`
}

// verify cryptographically validates the supplied ID token and returns
// the (sub, email, hd) tuple. Every failure mode returns an error:
// missing/invalid claims, unverified email, non-allow-listed hosted
// domain, expired token, wrong audience, or signature mismatch.
func (v *googleVerifier) verify(ctx context.Context, raw string) (*VerifiedGoogleIdentity, error) {
	if !v.configured() {
		return nil, fmt.Errorf("%w (set %s and %s)", ErrGoogleNotConfigured, envGoogleAudience, envGoogleAllowedHDs)
	}
	provider, err := v.provider(ctx)
	if err != nil {
		return nil, fmt.Errorf("google: oidc provider: %w", err)
	}
	// go-oidc checks signature, `aud` == audience, `iss`, and exp/nbf/iat.
	idToken, err := provider.Verifier(&oidc.Config{ClientID: v.audience}).Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("google: verify: %w", err)
	}
	var claims googleClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("google: claims: %w", err)
	}
	return v.checkClaims(claims)
}

// checkClaims applies the SeDoc-specific policy on top of go-oidc's
// cryptographic verification. Pure (no network) so it is unit-testable
// in isolation from the JWKS round-trip.
func (v *googleVerifier) checkClaims(c googleClaims) (*VerifiedGoogleIdentity, error) {
	if !bool(c.EmailVerified) {
		return nil, errors.New("google: token email is not verified")
	}
	hd := strings.ToLower(strings.TrimSpace(c.HD))
	if hd == "" {
		return nil, errors.New("google: token missing hd claim (personal accounts are not accepted)")
	}
	if _, ok := v.allowedHDs[hd]; !ok {
		return nil, fmt.Errorf("google: hosted domain %s is not on the SeDoc allow-list", hd)
	}
	email := strings.ToLower(strings.TrimSpace(c.Email))
	if email == "" {
		return nil, errors.New("google: verified token has no email claim")
	}
	if strings.TrimSpace(c.Sub) == "" {
		return nil, errors.New("google: token missing sub claim")
	}
	return &VerifiedGoogleIdentity{Sub: c.Sub, Email: email, HD: hd}, nil
}

// provider returns the cached *oidc.Provider for Google, creating one on
// first use. Race-safe via LoadOrStore, mirroring the M365 verifier.
func (v *googleVerifier) provider(ctx context.Context) (*oidc.Provider, error) {
	if p, ok := v.providers.Load(googleIssuer); ok {
		return p.(*oidc.Provider), nil
	}
	p, err := oidc.NewProvider(ctx, googleIssuer)
	if err != nil {
		return nil, err
	}
	actual, _ := v.providers.LoadOrStore(googleIssuer, p)
	return actual.(*oidc.Provider), nil
}

// compile-time assertion that jsonBool satisfies json.Unmarshaler.
var _ json.Unmarshaler = (*jsonBool)(nil)
