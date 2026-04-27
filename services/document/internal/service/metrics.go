package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Service-level Prometheus metrics. Registered on the default registry
// at package init; scraped from the shared /metrics endpoint wired in
// cmd/server/main.go.
//
// Adding a metric here: choose a name of the form
// `<service>_<subject>_<verb>_total|_seconds|_errors_total`. Include
// a Help string that names the source event or code path.

// versionUploadedEmitted counts successful outbox inserts of
// dms.version.uploaded.v1. Paired with the outbox publisher's
// downstream publish metric to detect outbox→NATS drain lag.
var versionUploadedEmitted = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "document_events_published_total",
		Help: "Outbox-inserted domain events, labelled by subject.",
	},
	[]string{"subject"},
)

// retentionCoverageRatio is the fraction of active documents matched
// by at least one active retention policy. ADR 0036 §"SLI dashboard".
// 1.0 = every active document has a retention policy assigned to it;
// 0.0 = no policy coverage. Compliance teams alert when this drops
// below the org's target (typically 0.95 for regulated tenants).
//
// Refreshed by the retention sweep (via SweepRetention's per-tenant
// pass) — we don't compute it on /metrics scrape because the query
// is a JOIN over potentially millions of rows. Push semantics, not
// pull.
var retentionCoverageRatio = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "document_retention_coverage_ratio",
		Help: "Fraction of active documents (lifecycle in active|retained) matched by at least one active retention_policy. 1.0 = full coverage.",
	},
	[]string{"tenant_id"},
)

// dispositionQueueDepth is the count of disposition_candidates waiting
// for human review per tenant + status. Surfaced on the SLI dashboard
// alongside the SLA — compliance officers should see backlog at a
// glance and alert when 'queued' depth crosses an org-defined ceiling.
var dispositionQueueDepth = promauto.NewGaugeVec(
	prometheus.GaugeOpts{
		Name: "document_disposition_queue_depth",
		Help: "Disposition_candidates rows by status, per tenant. Dashboard splits queued + approved (work-in-flight).",
	},
	[]string{"tenant_id", "status"},
)

// PublishRetentionMetrics is called at the end of each retention
// sweep + disposition execute pass to update both gauges. Exported
// so the handler/service callers can invoke it directly.
func PublishRetentionMetrics(tenantID string, coverage float64, queueDepthByStatus map[string]int) {
	retentionCoverageRatio.WithLabelValues(tenantID).Set(coverage)
	for status, depth := range queueDepthByStatus {
		dispositionQueueDepth.WithLabelValues(tenantID, status).Set(float64(depth))
	}
}
