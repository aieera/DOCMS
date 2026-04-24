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

func init() {
	prometheus.MustRegister(authTotal)
}

// record increments the counter. Defined as a variable so tests can
// swap it out without touching the Prometheus registry.
var record = func(method, outcome string) {
	authTotal.WithLabelValues(method, outcome).Inc()
}
