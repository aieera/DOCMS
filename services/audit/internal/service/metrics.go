package service

import "github.com/prometheus/client_golang/prometheus"

// §8.8 / G8 — chain-integrity metrics. The daily CronJob in
// deploy/helm/sedoc/templates/cronjobs/audit-verify.yaml POSTs
// /api/v1/audit/verify-integrity; VerifyIntegrity() bumps these
// counters so alertmanager's tiered rules can fire a P0 on any
// chain break.
//
// Labels:
//   tenant_id — so dashboards and alerts can scope to the compromised
//               tenant without leaking one tenant's verifier state
//               into another tenant's view.
//   result    — "ok" | "break"; counted separately so dashboards
//               show both the health beat and the incident count.

var (
	auditVerifyTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_verify_total",
			Help: "Count of audit chain verifications by tenant and outcome.",
		},
		[]string{"tenant_id", "result"},
	)

	auditChainBreakTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_chain_break_total",
			Help: "Cumulative count of detected audit chain breaks. MUST stay at 0 in production; any non-zero value triggers a P0 alert.",
		},
		[]string{"tenant_id"},
	)

	auditVerifyEventsScanned = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_verify_events_scanned_total",
			Help: "Number of audit events walked during integrity verification.",
		},
		[]string{"tenant_id"},
	)
)

// BUG-01 — ingest + partition-maintenance metrics.
//
// The partitioning bug (audit_events had no partition covering the
// current month, so every INSERT failed with SQLSTATE 23514) was invisible
// on every dashboard: the consumer logged at error level and NAK'd, and
// nothing counted it. Verification metrics stayed green because
// VerifyIntegrity walked an empty log and passed vacuously. These make
// ingest health a first-class, alertable signal.
//
// audit_ingest_events_total{result="error"} moving off zero means audit
// evidence is being lost after MaxDeliver — treat it exactly as
// seriously as audit_chain_break_total.
var (
	auditIngestTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_ingest_events_total",
			Help: "Audit events consumed from NATS by outcome (ok|error). Any sustained 'error' rate means audit evidence is being dropped.",
		},
		[]string{"result"},
	)

	// Labelled by subject so the failing event class is identifiable
	// without grepping logs. Cardinality is bounded by the dms.* event
	// taxonomy, not by tenant or resource.
	auditIngestFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_ingest_failures_total",
			Help: "Audit ingest failures by NATS subject. MUST stay at 0; a non-zero value means events are being NAK'd and will be lost after MaxDeliver.",
		},
		[]string{"subject"},
	)

	auditPartitionEnsureTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "audit_partition_ensure_total",
			Help: "Partition-maintenance passes over audit_events by outcome (ok|error).",
		},
		[]string{"result"},
	)

	auditPartitionCreatedTotal = prometheus.NewCounter(
		prometheus.CounterOpts{
			Name: "audit_partition_created_total",
			Help: "Monthly audit_events partitions created by the in-process maintainer.",
		},
	)

	auditDefaultPartitionRows = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "audit_default_partition_rows",
			Help: "Rows currently in the audit_events DEFAULT partition. They are captured and queryable, but a non-zero value means created_at values are landing outside the maintained monthly window.",
		},
	)
)

func init() {
	prometheus.MustRegister(
		auditVerifyTotal,
		auditChainBreakTotal,
		auditVerifyEventsScanned,
		auditIngestTotal,
		auditIngestFailures,
		auditPartitionEnsureTotal,
		auditPartitionCreatedTotal,
		auditDefaultPartitionRows,
	)
}
