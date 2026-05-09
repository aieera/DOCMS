// Package pades implements PAdES-LTV signing-side embed + verifier-
// side validation per ADR 0072.
//
// What's here:
//
//   parser.go   — locates incremental revisions, /Sig dicts, /DSS,
//                 /VRI; pulls ByteRange + Contents.
//   cms.go      — verifies a detached SignedData against the
//                 ByteRange digest.
//   ocsp.go     — fetches + verifies OCSP single-responses.
//   crl.go      — fetches CRLs from the cert's CRLDistributionPoints.
//   tsa.go      — RFC 3161 timestamp client.
//   dss.go      — incremental-update writer that appends /DSS + /VRI.
//   ltv.go      — Verifier + Embedder facades the rest of the
//                 service consumes.
//
// What we deliberately don't do:
//
//   - Encrypted PDFs (signed PDFs MUST be visible to validators).
//   - Object-stream-compressed PDFs (PDF 1.5 /ObjStm). Real-world
//     signed PDFs are flat by convention; we surface a structured
//     error rather than misreport.
//   - Full PDF rendering. We never paint the page; we just verify.
//
// All times are UTC. All lookups have a pluggable HTTPClient so
// tests run hermetically.
package pades

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"time"
)

// Level mirrors signer.Level so callers don't have to map between
// types. Avoiding an import-cycle: signer depends on this package
// at runtime (Verify delegates here), not the other way around.
type Level string

const (
	LevelBB   Level = "PAdES-B-B"
	LevelBT   Level = "PAdES-B-T"
	LevelBLT  Level = "PAdES-B-LT"
	LevelBLTA Level = "PAdES-B-LTA"
)

// CertStatus encodes the eIDAS Validation Report vocabulary for
// "what's the cert's standing relative to sign time + now".
//
//   StatusValid         — chain valid at sign time AND still valid.
//   StatusIndeterminate — chain valid at sign time; expired since.
//                          Common for B-LT signatures past cert
//                          expiry — the embedded OCSP is what
//                          carries the proof. UI should treat as
//                          "verifiable, but seek expert review for
//                          new transactions".
//   StatusRevoked       — cert was revoked at sign time. Hard fail.
//   StatusUnknown       — couldn't determine; CRL/OCSP unreachable.
//                          UI shows yellow.
type CertStatus string

const (
	StatusValid         CertStatus = "valid"
	StatusIndeterminate CertStatus = "indeterminate"
	StatusRevoked       CertStatus = "revoked"
	StatusUnknown       CertStatus = "unknown"
)

// SignatureInfo is one entry per /Sig dict.
type SignatureInfo struct {
	FieldName      string
	SignerName     string
	SignerEmail    string
	Issuer         string
	SerialHex      string
	SignedAt       time.Time
	Level          Level
	CertStatus     CertStatus
	ChainValid     bool
	TimestampValid bool
	TamperEvident  bool
	Reason         string
	Location       string
	// Errors hold per-signature error codes; empty when everything
	// validates cleanly. Codes use a short stable vocabulary
	// ("digest_mismatch", "chain_unverifiable", "ocsp_revoked",
	// "tsa_invalid", ...) so the UI can map to copy.
	Errors []string
}

// Report is what Verifier.Validate returns for a whole document.
type Report struct {
	SignatureCount int
	Signatures     []SignatureInfo
	// LTVEnabled is true when /DSS is present AND every signature
	// has a /VRI entry that resolves to OCSP (or CRL) covering
	// the cert at sign time.
	LTVEnabled bool
	// LTVAge is now - youngest LTV material in the doc. Surfaced
	// to the UI so a 3-year-old signature shows "LTV from 30 days
	// ago" if Adobe's annual re-stamp ran. Zero when no LTV.
	LTVAge time.Duration
	// TamperEvident: no incremental update after the last signed
	// revision touched bytes outside ByteRange. False here is the
	// "document modified after signing" state.
	TamperEvident bool
	// Errors are document-level codes; per-sig errors live in each
	// SignatureInfo.Errors.
	Errors []string
	// ParsedAt stamps when the report was generated (used by the
	// frontend "re-validate" UX).
	ParsedAt time.Time
}

// Tier reports which validation tier produced the report. UIs that
// want to show "Tier-1: pass / Tier-2: pass" pull this off the
// Report when running through the EU DSS demo wrapper.
type Tier string

const (
	TierUnspecified Tier = ""
	Tier1           Tier = "tier-1" // pure-Go pades package
	Tier2           Tier = "tier-2" // EU DSS validator + Adobe Reader spot-check
)

// Errors. Adapters wrap with %w so callers can errors.Is.
var (
	ErrUnsupportedEncrypted = errors.New("pades: encrypted PDFs not supported")
	ErrUnsupportedObjStream = errors.New("pades: object streams not supported")
	ErrNoSignatures         = errors.New("pades: no signatures found")
	ErrTamperedSignature    = errors.New("pades: bytes outside ByteRange were modified")
	ErrParse                = errors.New("pades: parse error")
	ErrTSA                  = errors.New("pades: tsa error")
)

// VerifierOptions tunes what counts as a successful chain walk.
// Roots is the set of trust anchors the verifier uses to resolve
// the signer's certificate chain. Empty → use the OS trust store.
// Per-tenant policy lives one layer up; this struct is the wire
// shape between the service and the package.
type VerifierOptions struct {
	Roots          *x509.CertPool
	Intermediates  *x509.CertPool
	HTTPClient     *http.Client
	// Now overrides time.Now for tests. Zero → real clock.
	Now time.Time
	// SkipNetworkLookups disables OCSP + CRL fetching when LTV is
	// not embedded. Useful for hermetic tests; production keeps it
	// false so we have a network path to fall back on.
	SkipNetworkLookups bool
}

// EmbedOptions controls Embedder.Embed.
type EmbedOptions struct {
	// UpgradeToBT wraps a fresh RFC 3161 TSA timestamp around the
	// signature, lifting B-B → B-T.
	UpgradeToBT bool
	// UpgradeToBLT attaches OCSP + CRL responses for every cert
	// in the chain into a /DSS dictionary, lifting B-T → B-LT.
	UpgradeToBLT bool
	// HTTPClient for OCSP / CRL / TSA fetches.
	HTTPClient *http.Client
	// TSAURL overrides the embedder's default TSA when set.
	TSAURL string
}

// nowOr returns t if non-zero, else time.Now().UTC().
func nowOr(t time.Time) time.Time {
	if t.IsZero() {
		return time.Now().UTC()
	}
	return t
}

// httpOr returns hc if non-nil, else http.DefaultClient.
func httpOr(hc *http.Client) *http.Client {
	if hc == nil {
		return http.DefaultClient
	}
	return hc
}

// _ keeps context imported for the package-level type signatures
// even before the methods that use it ship.
var _ context.Context = nil
