// DSSSidecarSigner is the gRPC client shell for the Java DSS sidecar
// introduced by ADR 0025.
//
// Wave 9.2 ships the client **shell** only — the sidecar's Gradle
// project, the proto contract, and the actual JVM integration land
// in a dedicated follow-up (Wave 9.2b). Until then Sign returns
// ErrNotConfigured so a misconfigured deployment fails loudly at
// first request rather than quietly emitting invalid PDFs.
//
// The shape here is intentionally forward-compatible: when the
// proto lands, the Sign / Verify bodies become `client.Sign(ctx,
// req)` / `client.Verify(ctx, req)` and the rest stays put.
package signer

import (
	"context"
)

// DSSSidecarSigner is the gRPC client stub.
type DSSSidecarSigner struct {
	addr string
}

// NewDSSSidecarSigner constructs a sidecar client stub pointing at
// addr. Dial happens lazily on the first Sign call (Wave 9.2b).
func NewDSSSidecarSigner(addr string) *DSSSidecarSigner {
	return &DSSSidecarSigner{addr: addr}
}

// Sign currently returns ErrNotConfigured. See package doc.
func (d *DSSSidecarSigner) Sign(ctx context.Context, req Request) (*Response, error) {
	if err := validateCommon(req); err != nil {
		return nil, err
	}
	return nil, ErrNotConfigured
}

// Verify currently returns ErrNotConfigured. See package doc.
func (d *DSSSidecarSigner) Verify(ctx context.Context, pdfBytes []byte) (*VerificationReport, error) {
	return nil, ErrNotConfigured
}

var _ Signer = (*DSSSidecarSigner)(nil)
