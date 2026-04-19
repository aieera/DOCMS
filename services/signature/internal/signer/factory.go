// Package signer factory — env-driven wiring for the signer impl.
//
// VAULTDMS_SIGNER controls which implementation the signature
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

// FromEnv constructs a Signer based on VAULTDMS_SIGNER. sidecarAddr
// is the gRPC endpoint for the DSS sidecar; ignored when the mock
// is selected.
func FromEnv(sidecarAddr string) (Signer, error) {
	kind := os.Getenv("VAULTDMS_SIGNER")
	if kind == "" {
		kind = "mock"
	}
	switch kind {
	case "mock":
		return NewMockSigner(), nil
	case "dss":
		// TODO(Wave 9.2b): real gRPC client. Today we return the
		// shell client so imports compile; Sign returns
		// ErrNotConfigured.
		return NewDSSSidecarSigner(sidecarAddr), nil
	default:
		return nil, fmt.Errorf("VAULTDMS_SIGNER=%q: must be mock or dss", kind)
	}
}
