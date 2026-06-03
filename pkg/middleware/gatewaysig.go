package middleware

import (
	"crypto/hmac"
	"net/http"
	"os"
	"strings"
)

// publicExternalCallbackPrefixes are paths that vendors (DocuSign,
// Adobe Sign, …) hit directly without going through the gateway, so
// they cannot carry X-Gateway-Signature. Each path authenticates
// itself via vendor-specific HMAC of the body (webhooks) or our own
// HMAC-signed `state` parameter (OAuth callbacks). The handler is
// responsible for that check; the gateway-signature middleware just
// lets the request through.
//
// Match is path-prefix so per-provider sub-paths
// (e.g. /webhook/docusign/{tenant}) are covered by one entry.
var publicExternalCallbackPrefixes = []string{
	"/api/v1/signatures/esign/oauth/callback",
	"/api/v1/signatures/esign/webhook/",
	// ADR 0089 — native-connector OAuth callbacks (Google etc.).
	"/api/v1/connectors/oauth/callback",
	// ADR 0090 — iPaaS trigger endpoints called by Zapier / Make / n8n.
	// API-key middleware (Bearer vdms_...) authenticates these instead
	// of the gateway. Path-prefix match covers documents / signatures /
	// workflows under the same /triggers/ root.
	"/api/v1/integrations/triggers/",
}

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
const GatewaySignatureEnv = "SEDOC_GATEWAY_SECRET"

// GatewaySignaturePrevEnv is the optional previous-secret env var read
// alongside GatewaySignatureEnv. When set, requests carrying EITHER value
// are accepted — operationally this is the "overlap window" of a secret
// rotation:
//
//  1. Set GatewaySignaturePrevEnv = current secret on every backend +
//     restart. Backends now accept current OR current (no-op accept).
//  2. Set GatewaySignatureEnv = NEW secret, leave GatewaySignaturePrevEnv
//     at the old value, restart backends. Both old + new are accepted.
//  3. Update the gateway to send the new secret.
//  4. Once gateway traffic is fully on the new secret, unset
//     GatewaySignaturePrevEnv and restart backends. Only the new secret
//     is accepted from then on.
//
// Empty / unset means "no previous secret"; the middleware then only
// accepts GatewaySignatureEnv.
const GatewaySignaturePrevEnv = "SEDOC_GATEWAY_SECRET_PREV"

// RequireGatewaySignature returns middleware that rejects any request
// whose X-Gateway-Signature header does not match the configured shared
// secret. The active secret is read at startup from
// SEDOC_GATEWAY_SECRET; if SEDOC_GATEWAY_SECRET_PREV is also set,
// that value is accepted in parallel so secret rotation can run with no
// downtime. A missing active secret panics at startup — we do NOT allow
// a service to accept unsigned traffic by accident.
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
	secrets := []string{secret}
	if prev := os.Getenv(GatewaySignaturePrevEnv); prev != "" && prev != secret {
		secrets = append(secrets, prev)
	}
	return RequireGatewaySignatureWithSecrets(secrets)
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

			// Public external-callback paths — these are hit DIRECTLY
			// by third-party vendors (DocuSign, Adobe Sign) without
			// going through our gateway, so they have no chance to
			// carry X-Gateway-Signature. They each authenticate
			// themselves through a different mechanism:
			//   * OAuth callbacks → HMAC-signed `state` parameter
			//     (see esign.OAuthConfig.VerifyState)
			//   * Webhooks → vendor-specific body HMAC (DocuSign HMAC
			//     header, Adobe webhook signature) verified inside
			//     the handler before any side effect.
			// Whitelisted by path-prefix so per-provider sub-paths
			// (e.g. /webhook/docusign/{tenant}) match.
			path := r.URL.Path
			for _, pfx := range publicExternalCallbackPrefixes {
				if strings.HasPrefix(path, pfx) {
					next.ServeHTTP(w, r)
					return
				}
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
