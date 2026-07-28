// Package signer factory — env-driven wiring for the signer impl.
//
// SEDOC_SIGNER controls which implementation the signature
// service instantiates:
//
//	mock (default) — MockSigner, deterministic, not PAdES-valid.
//	                 Suitable for unit/integration tests, dev, CI.
//	dss            — DSSSidecarSigner, gRPC client to the Java
//	                 sidecar (ADR 0025). Shipped as a shell in
//	                 Wave 9.2; full wiring lands in Wave 9.2b.
//
// Unknown values return an error so typos at deploy time fail fast
// rather than silently falling back to the mock (which would ship
// Adobe-invalid signatures to production).
package signer

import (
	"fmt"
	"os"
)

// FromEnv constructs a Signer based on SEDOC_SIGNER. sidecarAddr
// is the gRPC endpoint for the DSS sidecar; ignored when the mock
// is selected.
func FromEnv(sidecarAddr string) (Signer, error) {
	switch kind := os.Getenv("SEDOC_SIGNER"); kind {
	case "mock":
		return NewMockSigner(), nil
	case "dss":
		// TODO(Wave 9.2b): real gRPC client. Today we return the
		// shell client so imports compile; Sign returns
		// ErrNotConfigured.
		return NewDSSSidecarSigner(sidecarAddr), nil
	case "":
		// Require an EXPLICIT choice. Defaulting to the mock silently shipped
		// non-PAdES (Adobe-invalid) signatures whenever SEDOC_SIGNER was unset —
		// including a production deploy that forgot to set it. Dev/CI must set
		// SEDOC_SIGNER=mock; production sets dss.
		return nil, fmt.Errorf("SEDOC_SIGNER is required (mock|dss); refusing to default to the non-PAdES mock signer")
	default:
		return nil, fmt.Errorf("SEDOC_SIGNER=%q: must be mock or dss", kind)
	}
}
