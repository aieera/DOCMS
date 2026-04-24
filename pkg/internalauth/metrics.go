package internalauth

import "github.com/prometheus/client_golang/prometheus"

// internal_auth_total tracks each /internal/* authentication attempt.
// Labels:
//   - method: "mtls" | "hmac" | "none" (no credential presented)
//   - outcome: "ok" | "bad_cert" | "bad_san" | "bad_sig" | "skew" | "missing" | "mode_mismatch"
//
// Registered on the default registry at package init so it shows up
// on every service's /metrics endpoint without per-service plumbing.
var authTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "internal_auth_total",
		Help: "Count of /internal/* authentication attempts by method and outcome",
	},
	[]string{"method", "outcome"},
)

// internal_auth_mode_info is an info-style metric — value is always 1.
// Exactly one series per running process: label "mode" reflects the
// configured VAULTDMS_INTERNAL_AUTH_MODE. Consumed by the admin panel
// in web/ to render the current mode per service without an extra
// fan-out endpoint.
var modeInfo = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "internal_auth_mode_info",
		Help: "The configured VAULTDMS_INTERNAL_AUTH_MODE as a label; value is always 1",
	},
	[]string{"mode"},
)

// internal_auth_cert_expiry_timestamp_seconds reports the NotAfter of
// each SAN in the allowlist (when mode includes mTLS). Prometheus and
// Grafana render this as a Unix timestamp; the admin panel derives
// "days until expiry" in the browser.
//
// Recorded once at Verifier construction. Operators rotate via
// cert-manager (30-day leaves), which causes a pod restart and a
// fresh reading — we don't re-read cert files at runtime.
var certExpiry = prometheus.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "internal_auth_cert_expiry_timestamp_seconds",
		Help: "NotAfter of each configured peer-cert SAN, in Unix seconds (0 if unknown)",
	},
	[]string{"san"},
)

func init() {
	prometheus.MustRegister(authTotal, modeInfo, certExpiry)
}

// record increments the counter. Defined as a variable so tests can
// swap it out without touching the Prometheus registry.
var record = func(method, outcome string) {
	authTotal.WithLabelValues(method, outcome).Inc()
}

// setModeInfo publishes the configured mode exactly once per process.
// Called from New(); any prior value is reset so a misconfigured
// restart doesn't leave a stale label set behind.
func setModeInfo(mode Mode) {
	modeInfo.Reset()
	modeInfo.WithLabelValues(string(mode)).Set(1)
}

// setCertExpiry publishes NotAfter per SAN. Takes a map so callers
// that don't have certs (pure HMAC mode) can pass nil and the metric
// stays empty.
func setCertExpiry(perSAN map[string]int64) {
	certExpiry.Reset()
	for san, unix := range perSAN {
		certExpiry.WithLabelValues(san).Set(float64(unix))
	}
}
