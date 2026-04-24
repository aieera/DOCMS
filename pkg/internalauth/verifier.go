package internalauth

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Verifier holds the resolved auth config and exposes the two
// middlewares. A single instance per service; safe for concurrent use.
type Verifier struct {
	cfg Config
	now func() time.Time // swappable for HMAC skew tests
}

// New constructs a Verifier from Config. Returns an error if the
// config is inconsistent (e.g., mTLS mode without CAPool).
func New(cfg Config) (*Verifier, error) {
	switch cfg.Mode {
	case ModeMTLS, ModeHMAC, ModeBoth:
	default:
		return nil, fmt.Errorf("invalid mode %q", cfg.Mode)
	}
	if cfg.Mode != ModeHMAC && cfg.CAPool == nil {
		return nil, fmt.Errorf("CAPool required for mode %s", cfg.Mode)
	}
	if cfg.Mode != ModeMTLS && cfg.HMACSecret == "" {
		return nil, fmt.Errorf("HMACSecret required for mode %s", cfg.Mode)
	}
	if cfg.ClockSkewSecs <= 0 {
		cfg.ClockSkewSecs = 300
	}
	setModeInfo(cfg.Mode)
	setCertExpiry(readClientCertExpiry(cfg.ClientCertFile))
	return &Verifier{cfg: cfg, now: time.Now}, nil
}

// readClientCertExpiry parses the local service's own client cert and
// returns NotAfter keyed by each DNS SAN. Silent no-op on unreadable
// or unparseable input — the gauge simply stays empty, which the
// admin panel surfaces as "cert metadata unavailable" rather than
// blocking service startup. CA / HMAC-only modes hit this path too.
func readClientCertExpiry(path string) map[string]int64 {
	if path == "" {
		return nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	out := make(map[string]int64, len(cert.DNSNames))
	for _, san := range cert.DNSNames {
		out[san] = cert.NotAfter.Unix()
	}
	return out
}

// Mode returns the configured auth mode.
func (v *Verifier) Mode() Mode { return v.cfg.Mode }

// RequireInternalMTLS returns a middleware that rejects any request
// whose TLS peer certificate does not validate against the internal
// CA and carry a SAN on the allowlist.
//
// If the verifier is configured for ModeHMAC only, this middleware
// falls through to a 401 — calling it is a programming error, not a
// config issue, so we don't panic at mount time (Verifier is shared).
func (v *Verifier) RequireInternalMTLS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v.cfg.Mode == ModeHMAC {
			record("mtls", "mode_mismatch")
			http.Error(w, "internal mTLS not enabled", http.StatusUnauthorized)
			return
		}
		if err := v.verifyMTLS(r); err != nil {
			// Outcome label set inside verifyMTLS via recordOutcome.
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		record("mtls", "ok")
		next.ServeHTTP(w, r)
	})
}

// RequireInternalHMAC returns the HMAC middleware. Deprecated in
// favour of RequireInternalMTLS; retained for services that have not
// yet cut over.
func (v *Verifier) RequireInternalHMAC(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if v.cfg.Mode == ModeMTLS {
			record("hmac", "mode_mismatch")
			http.Error(w, "internal HMAC not enabled", http.StatusUnauthorized)
			return
		}
		if err := v.verifyHMAC(r); err != nil {
			http.Error(w, err.Error(), http.StatusUnauthorized)
			return
		}
		record("hmac", "ok")
		next.ServeHTTP(w, r)
	})
}

// RequireInternal returns a middleware that satisfies the configured
// Mode. In ModeBoth, mTLS is tried first (if TLS peer cert present)
// and HMAC is accepted as fallback. Prefer this in service wiring so
// the rollout flag is honoured without per-caller branching.
func (v *Verifier) RequireInternal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Liveness/readiness/metrics must never need these creds
		// — kube probes don't carry them. Matches the carve-out in
		// RequireGatewaySignature.
		switch r.URL.Path {
		case "/healthz", "/readyz", "/metrics":
			next.ServeHTTP(w, r)
			return
		}

		switch v.cfg.Mode {
		case ModeMTLS:
			if err := v.verifyMTLS(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			record("mtls", "ok")
		case ModeHMAC:
			if err := v.verifyHMAC(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			record("hmac", "ok")
		case ModeBoth:
			if hasPeerCert(r) {
				if err := v.verifyMTLS(r); err != nil {
					// mTLS was attempted and failed → do NOT
					// silently fall through to HMAC. A cert
					// that can't validate is a stronger
					// negative signal than no cert at all.
					http.Error(w, err.Error(), http.StatusUnauthorized)
					return
				}
				record("mtls", "ok")
				next.ServeHTTP(w, r)
				return
			}
			if err := v.verifyHMAC(r); err != nil {
				http.Error(w, err.Error(), http.StatusUnauthorized)
				return
			}
			record("hmac", "ok")
		}
		next.ServeHTTP(w, r)
	})
}
