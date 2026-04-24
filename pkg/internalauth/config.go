package internalauth

import (
	"crypto/x509"
	"fmt"
	"os"
	"strings"
)

// Env vars read by LoadFromEnv. Exported so callers / tests can drive
// Verifier directly without round-tripping through the environment.
const (
	EnvMode          = "VAULTDMS_INTERNAL_AUTH_MODE"
	EnvCACert        = "VAULTDMS_INTERNAL_CA_CERT"
	EnvClientCert    = "VAULTDMS_INTERNAL_CLIENT_CERT"
	EnvClientKey     = "VAULTDMS_INTERNAL_CLIENT_KEY"
	EnvHMACSecret    = "VAULTDMS_INTERNAL_HMAC_SECRET"
	EnvSANAllowlist  = "VAULTDMS_INTERNAL_SAN_ALLOWLIST"
	EnvClockSkewSecs = "VAULTDMS_INTERNAL_HMAC_SKEW_SECS"
)

// Mode selects which authentication methods the verifier accepts on
// incoming requests.
type Mode string

const (
	ModeMTLS Mode = "mtls"
	ModeHMAC Mode = "hmac"
	ModeBoth Mode = "both"
)

// Config is the resolved configuration used to build a Verifier.
// Callers may populate this directly in tests or via LoadFromEnv in
// production.
type Config struct {
	Mode           Mode
	CAPool         *x509.CertPool // required when Mode != ModeHMAC
	HMACSecret     string         // required when Mode != ModeMTLS
	SANAllowlist   []string       // DNS SANs permitted on client certs
	ClockSkewSecs  int64          // HMAC timestamp tolerance, default 300
	ClientCertFile string         // informational; used by outgoing clients
	ClientKeyFile  string         // informational; used by outgoing clients
}

// LoadFromEnv populates Config from VAULTDMS_INTERNAL_* env vars.
// Returns an error (not a panic) so callers can decide whether an
// unconfigured service should refuse to start or fall through to a
// legacy path.
func LoadFromEnv() (Config, error) {
	cfg := Config{
		Mode:           Mode(strings.ToLower(strings.TrimSpace(getenvDefault(EnvMode, string(ModeBoth))))),
		SANAllowlist:   splitAndTrim(os.Getenv(EnvSANAllowlist)),
		ClientCertFile: os.Getenv(EnvClientCert),
		ClientKeyFile:  os.Getenv(EnvClientKey),
		HMACSecret:     os.Getenv(EnvHMACSecret),
		ClockSkewSecs:  parseSkew(os.Getenv(EnvClockSkewSecs)),
	}

	switch cfg.Mode {
	case ModeMTLS, ModeHMAC, ModeBoth:
		// ok
	default:
		return Config{}, fmt.Errorf("invalid %s=%q (want mtls|hmac|both)", EnvMode, cfg.Mode)
	}

	if cfg.Mode != ModeHMAC {
		caPath := os.Getenv(EnvCACert)
		if caPath == "" {
			return Config{}, fmt.Errorf("%s is required when mode=%s", EnvCACert, cfg.Mode)
		}
		pool, err := loadCAPool(caPath)
		if err != nil {
			return Config{}, fmt.Errorf("load %s: %w", EnvCACert, err)
		}
		cfg.CAPool = pool
	}

	if cfg.Mode != ModeMTLS {
		if cfg.HMACSecret == "" {
			return Config{}, fmt.Errorf("%s is required when mode=%s", EnvHMACSecret, cfg.Mode)
		}
	}

	return cfg, nil
}

func loadCAPool(path string) (*x509.CertPool, error) {
	pem, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("no PEM certificates found in %s", path)
	}
	return pool, nil
}

func splitAndTrim(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getenvDefault(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func parseSkew(s string) int64 {
	if s == "" {
		return 300
	}
	var out int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 300
		}
		out = out*10 + int64(c-'0')
	}
	if out <= 0 {
		return 300
	}
	return out
}
