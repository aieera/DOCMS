// Package signer is the narrow contract between the signature
// service and whatever produces a PAdES-signed PDF.
//
// ADR 0025 picks EU Commission DSS (Java 17) as the primary signer,
// running as a per-pod sidecar. This package abstracts that choice
// behind a Go interface so:
//
//   - The signature service's business logic (envelopes, signer
//     sequence, Temporal workflow wiring) is decoupled from the
//     signing-library identity.
//   - Tests, dev, and CI can run against MockSigner without needing
//     a JVM on the image.
//   - A future Go-native implementation (pdfcpu-based, see ADR 0025
//     §5) swaps in by changing only `factory.go`.
//
// Wave 9.2 ships **MockSigner** (deterministic, NOT PAdES-valid) and
// the **DSSSidecarSigner** gRPC client shell. The sidecar itself —
// the Gradle/Java project that actually produces B-LT PDFs — lands
// in Wave 9.2b, a dedicated follow-up prompt. Until then, setting
// `SEDOC_SIGNER=dss` will return a not-configured error at boot;
// `SEDOC_SIGNER=mock` (the default) keeps tests and dev green.
package signer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Level enumerates the PAdES baseline levels. Values mirror the
// ETSI EN 319 142 profile names so logs and metrics use the exact
// standard vocabulary.
type Level string

const (
	LevelBB  Level = "PAdES-B-B"
	LevelBT  Level = "PAdES-B-T"
	LevelBLT Level = "PAdES-B-LT"
	// LevelBLTA is the archive-timestamp chain level. Wave 9 targets
	// B-LT; LevelBLTA is declared here so the type surface is stable
	// for the follow-up that enables archive timestamps.
	LevelBLTA Level = "PAdES-B-LTA"
)

// Mode selects between the two signing flows spec'd in §8.2:
//
//   - ModeServerHSM — KMS-resident cert + key. Used by SaaS tenants;
//     the signer calls pkg/crypto.KeyManager per request.
//   - ModeUserHeld  — the caller provides a detached CMS signature
//     and certificate chain. Used by on-prem customers with their
//     own smartcards / PIV tokens.
type Mode string

const (
	ModeServerHSM Mode = "server_hsm"
	ModeUserHeld  Mode = "user_held"
)

// Request is the input to Sign. Fields are named after DSS /
// RFC 3647 terminology so the Java sidecar can reuse them
// verbatim at the gRPC boundary.
type Request struct {
	// PDFBytes is the document to be signed. For incremental signing
	// (multiple signers on one document), this is the **current**
	// revision — the previous signer's signature must already be
	// embedded.
	PDFBytes []byte

	// SignerName, SignerEmail feed the signature appearance +
	// PAdES signerInfo. Required.
	SignerName  string
	SignerEmail string

	// Level is the target PAdES baseline. B-LT is the Wave 9 DoD.
	Level Level

	// Mode selects server-HSM vs user-held (see ModeServerHSM docs).
	Mode Mode

	// KMSAlias is required when Mode == ModeServerHSM. The signer
	// unwraps the private key via pkg/crypto.KeyManager for this
	// request only; the key never persists in the signer process.
	KMSAlias string

	// DetachedSignature + CertChain are required when
	// Mode == ModeUserHeld. CertChain is leaf-first, PEM-encoded.
	DetachedSignature []byte
	CertChain         [][]byte

	// TSAURL is the timestamp authority for B-T / B-LT. If empty the
	// signer uses its configured default.
	TSAURL string

	// FieldName is the PAdES signature-field name. Convention:
	// "Signer_<order>" so multi-signer envelopes are greppable in
	// `pdfsig --list`.
	FieldName string

	// Reason / Location / ContactInfo populate the PAdES signerInfo.
	// Optional but recommended for audit compliance.
	Reason      string
	Location    string
	ContactInfo string
}

// Response is what Sign returns. The new PDFBytes replaces the input
// — callers persist the full signed revision; there is no diff.
type Response struct {
	PDFBytes   []byte
	Level      Level
	SignedAt   time.Time
	Fingerprint string // SHA-256 hex of the signed bytes; used for
	// tamper-evident storage + the signature-completed event payload.
	ValidationReport string // optional DSS detailed-report JSON, for audit
}

// Signer is the interface the signature service consumes. All
// implementations must be safe for concurrent use — the service
// invokes Sign from multiple goroutines (one per signer in an
// envelope).
type Signer interface {
	// Sign produces a new PAdES revision of req.PDFBytes. Errors are
	// typed: ErrInvalidRequest → 400 upstream, ErrTSAUnavailable /
	// ErrKMSUnavailable → 503, other errors → 500.
	Sign(ctx context.Context, req Request) (*Response, error)

	// Verify inspects a signed PDF and returns the embedded signer
	// information + LTV status. Used by the /signatures/verify
	// endpoint.
	Verify(ctx context.Context, pdfBytes []byte) (*VerificationReport, error)
}

// VerificationReport summarises a verified PDF for downstream UIs.
// Returned by Verify.
type VerificationReport struct {
	SignatureCount int
	Signatures     []SignatureInfo
	// LTVEnabled is true when every signer has embedded validation
	// material (chain + OCSP/CRL) and the DSS dictionary is present.
	// Adobe Reader shows the green LTV tick when this is true.
	LTVEnabled bool
	// TamperEvident means no byte-range incremental update after the
	// last signature. False here is the "document has been modified
	// after signing" state.
	TamperEvident bool
}

// SignatureInfo is one embedded signature.
type SignatureInfo struct {
	SignerName string
	SignedAt   time.Time
	Issuer     string
	Valid      bool
	Level      Level
	Reason     string
}

// Typed errors. Callers use errors.Is to branch on these rather than
// string-matching messages.
var (
	ErrInvalidRequest   = errors.New("signer: invalid request")
	ErrTSAUnavailable   = errors.New("signer: timestamp authority unavailable")
	ErrKMSUnavailable   = errors.New("signer: KMS unavailable")
	ErrSidecarUnreachable = errors.New("signer: sidecar unreachable")
	ErrNotConfigured    = errors.New("signer: not configured")
)

// validateCommon runs the input checks every implementation needs
// before touching PDF bytes. Exported so the sidecar client can
// short-circuit network calls on bad input.
func validateCommon(req Request) error {
	if len(req.PDFBytes) == 0 {
		return fmt.Errorf("%w: pdf_bytes empty", ErrInvalidRequest)
	}
	if req.SignerName == "" {
		return fmt.Errorf("%w: signer_name required", ErrInvalidRequest)
	}
	if req.Level == "" {
		req.Level = LevelBLT // default per Wave 9 DoD
	}
	switch req.Mode {
	case ModeServerHSM:
		if req.KMSAlias == "" {
			return fmt.Errorf("%w: kms_alias required for server_hsm mode", ErrInvalidRequest)
		}
	case ModeUserHeld:
		if len(req.DetachedSignature) == 0 {
			return fmt.Errorf("%w: detached_signature required for user_held mode", ErrInvalidRequest)
		}
		if len(req.CertChain) == 0 {
			return fmt.Errorf("%w: cert_chain required for user_held mode", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: mode must be server_hsm or user_held", ErrInvalidRequest)
	}
	return nil
}

// fingerprintOf returns the SHA-256 hex digest of b.
func fingerprintOf(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
