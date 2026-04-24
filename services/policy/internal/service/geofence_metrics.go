package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Wave 15.2 metrics. Counter labels are kept low-cardinality —
// `tenant` is not bounded but is capped by the number of active
// tenants per process; `mode` and `result` are small enumerations.

var GeofenceDecisionsTotal = promauto.NewCounterVec(
	prometheus.CounterOpts{
		Name: "geofence_decisions_total",
		Help: "Geofence decisions emitted by the HTTP middleware, labelled by tenant, mode (allow|deny|step_up|error), and result reason.",
	},
	[]string{"tenant", "mode", "result"},
)

var GeofenceLookupDuration = promauto.NewHistogram(
	prometheus.HistogramOpts{
		Name:    "geofence_lookup_duration_seconds",
		Help:    "Wall-clock duration of a single geofence decision evaluation (DB + resolver + decide).",
		Buckets: prometheus.DefBuckets,
	},
)

// ObserveDecision is the hook passed to middleware.GeofenceConfig.OnDecision.
func ObserveDecision(tenant, mode, result string) {
	GeofenceDecisionsTotal.WithLabelValues(tenant, mode, result).Inc()
}
