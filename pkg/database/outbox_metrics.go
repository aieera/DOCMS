package database

import "github.com/prometheus/client_golang/prometheus/promauto"
import "github.com/prometheus/client_golang/prometheus"

// Outbox drain observability (Wave 0.2). Registered process-wide via
// promauto so every service that runs an OutboxPublisher exports them
// under one name; the `service` label separates emitters.
var (
	// outboxPublishFailures counts per-row publish failures (a "no
	// responders"/unbound-subject row increments this every retry).
	outboxPublishFailures = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "outbox_publish_failures_total",
		Help: "Outbox rows that failed to publish to JetStream (per attempt).",
	}, []string{"service", "event_type"})

	// outboxDLQTotal counts rows moved to outbox_dlq (exhausted retries).
	outboxDLQTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "outbox_dlq_total",
		Help: "Outbox rows dead-lettered after exhausting MaxAttempts.",
	}, []string{"service", "event_type"})

	// outboxDLQDepth is the current outbox_dlq row count (alert on > 0).
	// Set from a COUNT(*) each drain so it survives process restarts.
	outboxDLQDepth = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "outbox_dlq_depth",
		Help: "Current number of rows sitting in outbox_dlq.",
	}, []string{"service"})

	// outboxDrainLagSeconds is the age of the oldest still-unpublished,
	// due outbox row — the drain's backlog lag.
	outboxDrainLagSeconds = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "outbox_drain_lag_seconds",
		Help: "Age in seconds of the oldest due, unpublished outbox row (0 when drained).",
	}, []string{"service"})
)
