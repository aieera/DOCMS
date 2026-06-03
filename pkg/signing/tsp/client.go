// Package tsp wraps eIDAS Qualified Trust Service Providers behind
// a single TSPClient interface so the signature service doesn't have
// to know about Swisscom-vs-Intesi-vs-InfoCert wire shapes.
//
// Per ADR 0070, every QTSP we surveyed implements the same
// four-step ceremony:
//
//   Register   — create or look up a signer identity at the QTSP
//                (some flows need a "subject" record provisioned
//                before a transaction can start).
//   Authorize  — open a signing transaction for one document hash
//                and return a redirect URL the user's browser visits
//                to authenticate (national eID, MobileID, video-
//                ident — that part is the QTSP's problem).
//   Sign       — exchange the auth code we get on the redirect
//                callback for the actual CMS-signed hash + the
//                signer's qualified certificate chain.
//   Validate   — confirm the chain currently chains to a TL-trusted
//                root + isn't revoked. Used by the verify endpoint.
//
// All four are network calls. Each adapter is responsible for its
// own transport-level details (TLS pinning, mTLS, OAuth2 token
// caching) — the interface itself is shape-only.
package tsp

import (
	"context"
	"errors"
	"time"
)

// Provider identifies which QTSP implementation handles a request.
// Stored in tsp_signing_sessions.provider; widened here whenever a
// new adapter lands.
type Provider string

const (
	ProviderSwisscom Provider = "swisscom"
	ProviderIntesi   Provider = "intesi"
	ProviderInfoCert Provider = "infocert"
	// ProviderMock is the in-memory adapter used by CI + Playwright.
	// It produces deterministic but NOT cryptographically-valid
	// signatures, so production must never resolve this provider.
	ProviderMock Provider = "mock"
)

// TSPClient is the four-method shape every adapter satisfies. It
// is safe for concurrent use; adapters carry their own connection
// pool / token cache state.
type TSPClient interface {
	// Provider reports which QTSP this client targets. Lets the
	// service-layer factory log + metric-tag without an adapter-
	// specific type assertion.
	Provider() Provider

	// Register provisions or looks up a signer identity at the
	// QTSP. Idempotent — repeat calls for the same SignerEmail
	// must return the same SubjectID.
	Register(ctx context.Context, req RegisterReq) (*RegisterResp, error)

	// Authorize opens a signing transaction for one document hash.
	// Returns the redirect URL the user must visit + an external
	// transaction id we persist so we can correlate the return-URL
	// callback later.
	Authorize(ctx context.Context, req AuthorizeReq) (*AuthorizeResp, error)

	// Sign exchanges the authorization code we received on the
	// callback for the actual signed hash + cert chain. Once the
	// QTSP has answered Sign, the transaction is consumed — calling
	// Sign twice with the same code returns ErrAlreadyConsumed.
	Sign(ctx context.Context, req SignReq) (*SignResp, error)

	// Validate verifies a previously-issued certificate against the
	// QTSP's chain + revocation feed at the present moment. Used
	// by the verify endpoint to surface "this signature was
	// qualified at sign time AND the signer's cert is still on the
	// EU Trusted List right now". Cheap (cached) for repeated
	// requests.
	Validate(ctx context.Context, req ValidateReq) (*ValidateResp, error)
}

// RegisterReq names a signer to be vetted at the QTSP. SignerEmail
// is the canonical identity — adapters that need a dedicated
// "subject id" derive it deterministically from email.
type RegisterReq struct {
	TenantID    string
	SignerEmail string
	SignerName  string
	// CountryCode is ISO 3166-1 alpha-2; some QTSPs use it to pick
	// a national eID identity broker.
	CountryCode string
}

// RegisterResp returns the QTSP-side identifier the rest of the
// ceremony references.
type RegisterResp struct {
	SubjectID string
}

// AuthorizeReq begins a signing transaction.
type AuthorizeReq struct {
	TenantID     string
	SubjectID    string
	DocumentHash string // hex-encoded SHA-256 of the to-be-signed bytes
	HashAlgo     string // "SHA-256" — kept explicit so logs/audit are unambiguous
	// ReturnURL is OUR endpoint the QTSP redirects the browser to
	// after authentication. Already includes the session id +
	// HMAC anti-tamper signature.
	ReturnURL string
	// SignerEmail is duplicated here because some QTSPs need it
	// passed on every transaction (Intesi).
	SignerEmail string
	// Reason / Location populate the PAdES signerInfo via the QTSP
	// signature-policy attributes.
	Reason   string
	Location string
}

// AuthorizeResp is what the frontend needs to redirect the user.
type AuthorizeResp struct {
	RedirectURL  string
	ExternalID   string
	ExpiresAt    time.Time
}

// SignReq trades an auth code for a signed hash.
type SignReq struct {
	TenantID     string
	ExternalID   string
	AuthCode     string
	DocumentHash string
}

// SignResp carries the signed hash + cert chain. SignedHash is the
// PKCS#1 / CMS bytes the PAdES embed step plugs into the
// /Contents field; CertPEM + ChainPEM populate the qes_certificates
// row + the DSS dictionary for LTV.
type SignResp struct {
	SignedHash    []byte
	CertPEM       string
	ChainPEM      string
	SubjectDN     string
	IssuerDN      string
	SerialHex     string
	NotBefore     time.Time
	NotAfter      time.Time
	// LTVRevocation is the OCSP/CRL material captured at sign time.
	// JSON-encoded so it round-trips through the JSONB column
	// untouched. Empty when the QTSP didn't return revocation in
	// the same call (we'd then ask for it via a follow-up).
	LTVRevocation []byte
}

// ValidateReq asks the QTSP whether a previously-issued cert is
// currently OK.
type ValidateReq struct {
	TenantID string
	CertPEM  string
}

// ValidateResp is the boolean answer + a short reason for logs.
type ValidateResp struct {
	Valid          bool
	Reason         string
	OnTrustList    bool
	RevocationTime *time.Time
}

// Errors. Adapters wrap these with %w so callers can errors.Is.
var (
	ErrNotConfigured   = errors.New("tsp: provider not configured")
	ErrUnauthorized    = errors.New("tsp: provider rejected credentials")
	ErrTransport       = errors.New("tsp: transport failure")
	ErrAlreadyConsumed = errors.New("tsp: auth code already consumed")
	ErrSessionExpired  = errors.New("tsp: session expired at provider")
	ErrInvalidCert     = errors.New("tsp: certificate not on trusted list")
)

// Config is the union of every adapter's settings. The service
// builds one from SEDOC_QES_* envs at boot and hands the right
// slice to each adapter's New function. Empty fields → adapter
// returns ErrNotConfigured on any call.
type Config struct {
	// Default expiry for an Authorize transaction. 15 min matches
	// every sandbox we tested; runbook documents per-tenant
	// override.
	SessionTTL time.Duration

	Swisscom SwisscomConfig
	Intesi   IntesiConfig
	InfoCert InfoCertConfig
}

// DefaultConfig returns a Config with the safe-but-empty defaults.
// Callers fill in per-provider creds before passing to New*.
func DefaultConfig() Config {
	return Config{SessionTTL: 15 * time.Minute}
}
