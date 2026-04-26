package crypto

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// KMS metrics surface the latency + error rate of envelope-encryption
// hot paths. Operators page on these when:
//   - kms_operations_total{outcome="error"} rate spikes (KMS provider
//     outage; uploads fail-closed when this happens)
//   - kms_latency_seconds p99 climbs above ~200ms (every upload waits
//     on a DEK; this latency lands directly on the user)
//
// Backends label distinguishes local (CGO-friendly dev fallback),
// vault (HashiCorp Transit), and aws (KMS). Op label is one of
// generate | decrypt — matching the KeyManager interface methods.

var kmsOperationsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "kms_operations_total",
		Help: "KMS envelope-encryption operations by backend, op (generate|decrypt), and outcome (ok|error).",
	},
	[]string{"backend", "op", "outcome"},
)

var kmsLatencySeconds = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "kms_latency_seconds",
		Help: "KMS operation latency. p99 climbing above 200ms hits user-facing upload latency.",
		// Buckets sized for KMS round-trips: local is sub-ms, Vault/AWS
		// typically 10–100ms; tail buckets catch network blips.
		Buckets: []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5},
	},
	[]string{"backend", "op"},
)

// recordKMS records both metrics for one KMS call. Use as:
//
//   start := time.Now()
//   ... kms call ...
//   recordKMS("aws", "generate", start, err)
func recordKMS(backend, op string, start time.Time, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	kmsOperationsTotal.WithLabelValues(backend, op, outcome).Inc()
	kmsLatencySeconds.WithLabelValues(backend, op).Observe(time.Since(start).Seconds())
}
