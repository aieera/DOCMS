package internalauth

import (
	"net/http"
	"os"
	"strings"
)

// NewFromEnv loads Config from VAULTDMS_INTERNAL_* env vars and
// constructs a Verifier. Error if the configured mode is incomplete.
func NewFromEnv() (*Verifier, error) {
	cfg, err := LoadFromEnv()
	if err != nil {
		return nil, err
	}
	return New(cfg)
}

// NewFromEnvOptional returns (nil, nil) if VAULTDMS_INTERNAL_AUTH_MODE
// is unset — the operator has not opted the service into the new
// auth plane yet. If the var is set, behaves like NewFromEnv.
//
// Use this during the rollout so a service compiles and runs with
// existing RequireGatewaySignature behaviour until the operator
// provisions certs/secrets and flips the flag.
func NewFromEnvOptional() (*Verifier, error) {
	if os.Getenv(EnvMode) == "" {
		return nil, nil
	}
	return NewFromEnv()
}

// Mux returns an http.Handler that routes requests through one of
// three paths:
//
//   - /healthz, /readyz, /metrics: unauthenticated (kube probes)
//   - /internal/* : verifier.RequireInternal (mTLS / HMAC per mode)
//   - everything else: fallback middleware (typically the existing
//     middleware.RequireGatewaySignature for Kong-fronted /api/*)
//
// A nil verifier collapses the split: fallback is applied to every
// non-probe path. This is the rollout default — services keep their
// pre-internalauth behaviour until operators set VAULTDMS_INTERNAL_AUTH_MODE.
func Mux(inner http.Handler, v *Verifier, fallback func(http.Handler) http.Handler) http.Handler {
	if fallback == nil {
		fallback = func(h http.Handler) http.Handler { return h }
	}
	external := fallback(inner)

	if v == nil {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if isProbePath(r.URL.Path) {
				inner.ServeHTTP(w, r)
				return
			}
			external.ServeHTTP(w, r)
		})
	}

	internal := v.RequireInternal(inner)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isProbePath(r.URL.Path) {
			inner.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/internal/") {
			internal.ServeHTTP(w, r)
			return
		}
		external.ServeHTTP(w, r)
	})
}

func isProbePath(p string) bool {
	return p == "/healthz" || p == "/readyz" || p == "/metrics"
}
