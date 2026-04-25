package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Storage-service observability for the upload finalize path.
//
// Naming follows the convention in services/document/internal/service/metrics.go:
// <service>_<subject>_<verb>_total|_seconds. Tenant label is included on
// counters where per-tenant rate matters for noisy-neighbor triage; we
// deliberately keep it off the histogram to avoid cardinality explosion.
var (
	uploadFinalizeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_upload_finalize_total",
			Help: "CompleteUpload calls by outcome (scan_result label).",
		},
		[]string{"scan_result"}, // clean | infected | error | rejected_mime
	)

	mimeMismatchTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_mime_mismatch_total",
			Help: "Uploads where server-detected MIME disagreed with client Content-Type.",
		},
		[]string{"tenant"},
	)

	mimeRejectedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_mime_rejected_total",
			Help: "Uploads rejected because detected MIME was in the executable deny-list.",
		},
		[]string{"tenant"},
	)

	scanDurationSeconds = promauto.NewHistogram(
		prometheus.HistogramOpts{
			Name:    "clamav_scan_duration_seconds",
			Help:    "Wall-clock time for one ClamAV INSTREAM scan.",
			Buckets: prometheus.ExponentialBuckets(0.05, 2, 12), // 50ms .. ~200s
		},
	)

	scanOutcomeTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "clamav_scan_result_total",
			Help: "ClamAV scan outcomes. virus_scan_coverage SLI = (clean+infected)/total.",
		},
		[]string{"result"}, // clean | infected | error | unavailable
	)

	quarantineEventsTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_quarantine_events_total",
			Help: "Objects moved to the quarantine bucket.",
		},
		[]string{"reason"}, // virus | blocked_mime | mime_mismatch
	)

	uploadSizeRejectedTotal = promauto.NewCounterVec(
		prometheus.CounterOpts{
			Name: "storage_upload_size_rejected_total",
			Help: "Uploads rejected at initiate for exceeding the tenant plan ceiling.",
		},
		[]string{"plan"},
	)

	reconcilePendingSweep = promauto.NewCounter(
		prometheus.CounterOpts{
			Name: "storage_scan_reconcile_swept_total",
			Help: "Scan rows re-enqueued by the pending>1h reconciliation sweep.",
		},
	)
)
