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
