package middleware

import (
	"crypto/hmac"
	"net/http"
	"os"
)

// GatewaySignatureHeader is the header Kong sets via request-transformer
// on every request it forwards. Backends reject requests that arrive
// without it OR with a value that doesn't match the shared secret.
// See deploy/gateway/kong.yaml and blueprint §3.1 / B2.2.
const GatewaySignatureHeader = "X-Gateway-Signature"

// GatewaySignatureEnv is the env var both the gateway and every backend
// read to get the shared secret. Rotated via `kubectl rollout restart`
// (gateway) followed by the backends; 60s overlap is acceptable because
// the middleware tolerates both old and new during rotation — see
// RequireGatewaySignatureWithSecrets.
const GatewaySignatureEnv = "VAULTDMS_GATEWAY_SECRET"

// RequireGatewaySignature returns middleware that rejects any request
// whose X-Gateway-Signature header does not match the configured shared
// secret. The shared secret is read once at startup from
// VAULTDMS_GATEWAY_SECRET. A missing secret panics at startup — we do
// NOT allow a service to accept unsigned traffic by accident.
//
// Trust model (§3.1 / B2.2):
//   - Kong is the only caller expected to set this header.
//   - Backends do NOT listen on a public network port in prod; this
//     middleware is defense in depth for the case where a misconfigured
//     cluster accidentally exposes a pod.
//   - Comparison is constant-time to avoid header-length oracles.
func RequireGatewaySignature() func(http.Handler) http.Handler {
	secret := os.Getenv(GatewaySignatureEnv)
	if secret == "" {
		panic(GatewaySignatureEnv + " is required — refusing to start a backend that would accept unsigned traffic")
	}
	return RequireGatewaySignatureWithSecrets([]string{secret})
}

// RequireGatewaySignatureWithSecrets is RequireGatewaySignature with
// explicit secrets. Accepts multiple valid values so rotation can run
// with both old and new secret for the overlap window. Callers should
// prefer the env-var constructor; this exists for tests and for the
// rotation runbook.
func RequireGatewaySignatureWithSecrets(secrets []string) func(http.Handler) http.Handler {
	if len(secrets) == 0 {
		panic("RequireGatewaySignatureWithSecrets: at least one secret required")
	}
	// Precompute byte slices so the hot path is compare-only.
	expected := make([][]byte, len(secrets))
	for i, s := range secrets {
		if s == "" {
			panic("RequireGatewaySignatureWithSecrets: empty secret")
		}
		expected[i] = []byte(s)
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip for internal health endpoints so Kubernetes probes
			// and docker healthchecks don't need the secret. These
			// paths expose only liveness/readiness; they never return
			// tenant data.
			switch r.URL.Path {
			case "/healthz", "/readyz", "/metrics":
				next.ServeHTTP(w, r)
				return
			}

			got := []byte(r.Header.Get(GatewaySignatureHeader))
			if len(got) == 0 {
				http.Error(w, "missing gateway signature", http.StatusUnauthorized)
				return
			}
			ok := false
			for _, want := range expected {
				if hmac.Equal(got, want) {
					ok = true
					break
				}
			}
			if !ok {
				http.Error(w, "invalid gateway signature", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}
