package database

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Outbox publisher metrics. Surface the two SLIs ops actually pages on:
// drain lag (created_at → published_at, observed at publish success) and
// publish error rate (so a stuck NATS connection is visible without
// having to grep logs).

var outboxPublishLagSeconds = promauto.NewHistogramVec(
	prometheus.HistogramOpts{
		Name: "outbox_publish_lag_seconds",
		Help: "Time from outbox row insert (in the producer tx) to successful NATS publish.",
		// Buckets sized for the 100ms poll cadence: most rows publish in
		// 0.1–0.3s; tail buckets catch the redelivery / NATS-down cases.
		Buckets: []float64{0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60, 300},
	},
	[]string{"subject"},
)

var outboxPublishErrorsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "outbox_publish_errors_total",
		Help: "Outbox→NATS publish failures by subject and reason (marshal|publish).",
	},
	[]string{"subject", "reason"},
)
