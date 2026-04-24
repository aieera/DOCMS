package internalauth

import (
	"github.com/vaultdms/vaultdms/pkg/trustedproxy"
)

// MustInit is the one-line bootstrap for service main.go. It:
//   - installs the process-wide trusted-proxy config (may panic in
//     VAULTDMS_ENV=production when VAULTDMS_TRUSTED_PROXY_CIDRS is unset);
//   - returns a Verifier if VAULTDMS_INTERNAL_AUTH_MODE is set, or
//     nil if the operator has not opted the service into the new
//     auth plane yet (see NewFromEnvOptional for rationale).
//
// A non-nil error means VAULTDMS_INTERNAL_AUTH_MODE was set but the
// rest of the config is incomplete (missing CA, secret, etc.). The
// caller should surface that as a fatal boot error rather than
// silently degrading to the gateway signature — an operator who
// asked for mTLS and didn't get it wants to know.
func MustInit() (*Verifier, error) {
	trustedproxy.SetDefault(trustedproxy.LoadFromEnv())
	return NewFromEnvOptional()
}
