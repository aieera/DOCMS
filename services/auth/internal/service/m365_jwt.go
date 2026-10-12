// M365 JWT validation — ADR 0112 + post-audit hardening.
//
// Replaces the original "trust Graph /me" path which let any caller
// with ANY Entra-issued bearer token impersonate any SeDoc user
// whose email happened to match. Now every exchange must produce a
// JWT that:
//  1. Has a valid RS256 signature against the issuing tenant's JWKS
//     (handled by go-oidc, which caches keys + handles rollover).
//  2. Has `aud` equal to the configured SeDoc Entra application
//     ID (SEDOC_M365_AUDIENCE).
//  3. Has `tid` on the allow-list of Entra directories that SeDoc
//     accepts (SEDOC_M365_ALLOWED_TIDS, CSV).
//  4. Has unexpired `exp` / valid `nbf` (go-oidc enforces these).
//
// We surface the *verified* `tid` + `oid` claims (not the unverified
// `mail` field) so callers can key authentication off Microsoft-issued
// identifiers. The (tid, oid) pair is stable across email changes and
// can't be forged from another Entra tenant.
//
// Provider construction is per-tid because Microsoft's signing keys
// are scoped to the issuing directory. We cache providers in a
// sync.Map keyed by tid; first call to a new tid fetches discovery +
// JWKS, every subsequent call reuses go-oidc's in-memory key cache.
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
	"github.com/golang-jwt/jwt/v5"
)

// M365 environment variables. Read once at New() time so tests can
// swap them without touching the host env.
const (
	envM365Audience    = "SEDOC_M365_AUDIENCE"
	envM365AllowedTIDs = "SEDOC_M365_ALLOWED_TIDS"
)

// VerifiedEntraIdentity is the result of a successful JWT validation.
// The token signature is verified, but the `email` claim's CONTENT is
// not: Entra lets a user or admin set it to any address unless the
// domain is verified, which Entra reports in the optional `xms_edov`
// claim. Callers map by UPN and use Email only when EmailVerified.
type VerifiedEntraIdentity struct {
	TID string // Entra directory the user logged into (verified).
	OID string // Stable per-user GUID inside that directory.
	// UPN is the `preferred_username` claim (lowercased).
	UPN string
	// Email is the raw `email` claim (lowercased). Untrusted unless
	// EmailVerified.
	Email string
	// EmailVerified is the `xms_edov` claim: the email's domain is
	// verified in the issuing directory.
	EmailVerified bool
	// AMR is the `amr` claim (authentication methods, e.g. pwd, mfa).
	AMR []string
}

// M365TokenVerifier validates an Entra access token presented to the
// add-in exchange. The default (Config.M365Verifier nil) is the
// env-configured JWKS verifier below.
type M365TokenVerifier interface {
	Verify(ctx context.Context, raw string) (*VerifiedEntraIdentity, error)
}

// entraClaims is the subset of token claims the exchange consumes.
type entraClaims struct {
	TID               string          `json:"tid"`
	OID               string          `json:"oid"`
	Email             string          `json:"email"`
	PreferredUsername string          `json:"preferred_username"`
	XMSEdov           json.RawMessage `json:"xms_edov"`
	AMR               []string        `json:"amr"`
}

// lenientTrue reports whether a JSON claim value is affirmatively true:
// the boolean true, the string "true"/"1", or the number 1. Anything
// else (absent, null, false, garbage) is false — the claim only ever
// unlocks behavior, so unknown shapes fail closed.
func lenientTrue(raw json.RawMessage) bool {
	switch strings.ToLower(strings.TrimSpace(string(raw))) {
	case "true", `"true"`, `"1"`, "1":
		return true
	}
	return false
}

// identityFromClaims normalises verified claims into the identity the
// exchange maps. Pure, so the claim policy is unit-testable.
func identityFromClaims(c *entraClaims) (*VerifiedEntraIdentity, error) {
	if c.OID == "" {
		return nil, errors.New("m365: token missing oid claim")
	}
	return &VerifiedEntraIdentity{
		TID:           strings.ToLower(c.TID),
		OID:           c.OID,
		UPN:           strings.ToLower(strings.TrimSpace(c.PreferredUsername)),
		Email:         strings.ToLower(strings.TrimSpace(c.Email)),
		EmailVerified: lenientTrue(c.XMSEdov),
		AMR:           c.AMR,
	}, nil
}

// m365Verifier holds the cached oidc.Provider per Entra tid plus the
// configured audience + allow-list. Methods on *Service delegate here.
type m365Verifier struct {
	audience    string
	allowedTIDs map[string]struct{}
	providers   sync.Map // tid (string) → *oidc.Provider
}

// newM365Verifier reads the two env vars once. An empty allow-list or
// missing audience disables the M365 exchange path entirely — handlers
// should surface a 503 rather than fall back to insecure behavior.
func newM365Verifier() *m365Verifier {
	v := &m365Verifier{
		audience:    strings.TrimSpace(os.Getenv(envM365Audience)),
		allowedTIDs: map[string]struct{}{},
	}
	for _, t := range strings.Split(os.Getenv(envM365AllowedTIDs), ",") {
		t = strings.TrimSpace(strings.ToLower(t))
		if t != "" {
			v.allowedTIDs[t] = struct{}{}
		}
	}
	return v
}

// configured reports whether M365 exchange is operational. Returns
// false when either env var is missing — handlers must surface this
// as "feature disabled" rather than silently accepting tokens.
func (v *m365Verifier) configured() bool {
	return v != nil && v.audience != "" && len(v.allowedTIDs) > 0
}

// Verify cryptographically validates the supplied JWT and returns the
// verified identity claims. Returns an error for every failure mode:
// missing/invalid claims, untrusted Entra tenant, expired token,
// wrong audience, or signature mismatch.
func (v *m365Verifier) Verify(ctx context.Context, raw string) (*VerifiedEntraIdentity, error) {
	if !v.configured() {
		return nil, fmt.Errorf("%w (set %s and %s)", ErrM365NotConfigured, envM365Audience, envM365AllowedTIDs)
	}
	// Extract `tid` BEFORE verification so we can pick the right
	// JWKS endpoint. tid in an unverified token is untrusted, but
	// since we re-verify the signature against the matching directory
	// AND check that `iss` ends in /{tid}/v2.0, a tid spoof can only
	// route to a directory whose keys we then fail to verify against.
	parsed, _, err := new(jwt.Parser).ParseUnverified(raw, jwt.MapClaims{})
	if err != nil {
		return nil, fmt.Errorf("m365: parse: %w", err)
	}
	mc, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return nil, errors.New("m365: unexpected claims shape")
	}
	tid, _ := mc["tid"].(string)
	tid = strings.ToLower(strings.TrimSpace(tid))
	if tid == "" {
		return nil, errors.New("m365: token missing tid claim")
	}
	if _, ok := v.allowedTIDs[tid]; !ok {
		// Reject BEFORE any network call so an attacker can't probe
		// Entra discovery for arbitrary tenants.
		return nil, fmt.Errorf("m365: token issued by Entra tenant %s is not on the SeDoc allow-list", tid)
	}

	provider, err := v.providerForTID(ctx, tid)
	if err != nil {
		return nil, fmt.Errorf("m365: oidc provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{
		ClientID: v.audience,
		// go-oidc enforces exp/nbf/iat automatically. SkipIssuerCheck
		// stays false so we also verify iss matches what discovery
		// declared for this tid.
	})
	idToken, err := verifier.Verify(ctx, raw)
	if err != nil {
		return nil, fmt.Errorf("m365: verify: %w", err)
	}

	var claims entraClaims
	if err := idToken.Claims(&claims); err != nil {
		return nil, fmt.Errorf("m365: claims: %w", err)
	}
	// Belt-and-suspenders: the verified `tid` claim must match what
	// we used to pick the provider. (oidc.Verifier already covered
	// signature + aud; this catches a misconfiguration where the
	// allow-list and the actual signing directory diverge.)
	if !strings.EqualFold(claims.TID, tid) {
		return nil, errors.New("m365: verified tid does not match unverified tid")
	}
	return identityFromClaims(&claims)
}

// providerForTID returns a cached *oidc.Provider for the given Entra
// directory, creating one on first use.
func (v *m365Verifier) providerForTID(ctx context.Context, tid string) (*oidc.Provider, error) {
	if p, ok := v.providers.Load(tid); ok {
		return p.(*oidc.Provider), nil
	}
	issuer := "https://login.microsoftonline.com/" + tid + "/v2.0"
	p, err := oidc.NewProvider(ctx, issuer)
	if err != nil {
		return nil, err
	}
	// Race-safe: LoadOrStore returns the existing provider if a
	// concurrent caller beat us to the cache.
	actual, _ := v.providers.LoadOrStore(tid, p)
	return actual.(*oidc.Provider), nil
}
