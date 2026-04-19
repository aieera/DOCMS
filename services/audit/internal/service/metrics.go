package service

import "github.com/prometheus/client_golang/prometheus"

// §8.8 / G8 — chain-integrity metrics. The daily CronJob in
// deploy/helm/vaultdms/templates/cronjobs/audit-verify.yaml POSTs
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

func init() {
	prometheus.MustRegister(
		auditVerifyTotal,
		auditChainBreakTotal,
		auditVerifyEventsScanned,
	)
}
