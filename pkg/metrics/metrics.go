// Package metrics provides Prometheus instrumentation for all SeDoc
// services. Call Register() once at startup, then use the exported
// counters/histograms throughout the codebase.
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ---- HTTP metrics ---------------------------------------------------------

var HTTPRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "http_requests_total",
	Help: "Total HTTP requests by method, path, status, and tenant.",
}, []string{"method", "path", "status", "tenant_id"})

var HTTPRequestDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "http_request_duration_seconds",
	Help:    "HTTP request latency in seconds.",
	Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 5.0},
}, []string{"method", "path"})

// ---- gRPC metrics ---------------------------------------------------------

var GRPCRequestsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "grpc_requests_total",
	Help: "Total gRPC requests by method and status.",
}, []string{"method", "status"})

var GRPCDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "grpc_request_duration_seconds",
	Help:    "gRPC request latency in seconds.",
	Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 5.0},
}, []string{"method"})

// ---- Database metrics -----------------------------------------------------

var DBQueryDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "db_query_duration_seconds",
	Help:    "Database query latency in seconds.",
	Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1.0},
}, []string{"query"})

var DBConnections = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "db_connections",
	Help: "Database connection pool state.",
}, []string{"pool", "state"})

// ---- Search metrics -------------------------------------------------------

var SearchLatency = promauto.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "search_latency_seconds",
	Help:    "Search request latency by mode.",
	Buckets: []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1.0, 2.0},
}, []string{"mode"})

// ---- Storage metrics ------------------------------------------------------

var OCRPagesTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ocr_pages_total",
	Help: "Total OCR pages processed by engine.",
}, []string{"engine"})

var UploadBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "upload_bytes_total",
	Help: "Total bytes uploaded.",
})

var DownloadBytesTotal = promauto.NewCounter(prometheus.CounterOpts{
	Name: "download_bytes_total",
	Help: "Total bytes downloaded.",
})

// ---- WebSocket metrics ----------------------------------------------------

var WebSocketConnections = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "websocket_connections",
	Help: "Current active WebSocket connections.",
})

// ---- Cache metrics --------------------------------------------------------

var CacheHits = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "cache_hits_total",
	Help: "Cache hits by cache name.",
}, []string{"cache"})

var CacheMisses = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "cache_misses_total",
	Help: "Cache misses by cache name.",
}, []string{"cache"})

// ---- Event bus metrics ----------------------------------------------------

var EventBusPublished = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "event_bus_published_total",
	Help: "Events published by topic.",
}, []string{"topic"})

var EventBusConsumed = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "event_bus_consumed_total",
	Help: "Events consumed by topic and consumer group.",
}, []string{"topic", "group"})

var EventBusLag = promauto.NewGaugeVec(prometheus.GaugeOpts{
	Name: "event_bus_lag",
	Help: "Consumer lag by topic and group.",
}, []string{"topic", "group"})

// ---- Handler + middleware -------------------------------------------------

// Handler returns the promhttp handler for /metrics.
func Handler() http.Handler {
	return promhttp.Handler()
}

// HTTPMiddleware wraps an http.Handler to record request count and latency.
func HTTPMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &responseWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)

		tenant := r.Header.Get("X-Tenant-ID")
		if tenant == "" {
			tenant = "_anon"
		}
		path := normalizePath(r.URL.Path)
		HTTPRequestsTotal.WithLabelValues(r.Method, path, strconv.Itoa(rw.status), tenant).Inc()
		HTTPRequestDuration.WithLabelValues(r.Method, path).Observe(time.Since(start).Seconds())
	})
}

type responseWriter struct {
	http.ResponseWriter
	status int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

// normalizePath collapses UUIDs and numeric IDs to reduce cardinality.
func normalizePath(p string) string {
	// Replace UUID segments with :id
	parts := make([]byte, 0, len(p))
	i := 0
	for i < len(p) {
		if p[i] == '/' {
			parts = append(parts, '/')
			i++
			// Check if next segment looks like a UUID (36 chars with dashes)
			j := i
			for j < len(p) && p[j] != '/' {
				j++
			}
			seg := p[i:j]
			if len(seg) == 36 && seg[8] == '-' {
				parts = append(parts, ":id"...)
			} else if isNumeric(seg) {
				parts = append(parts, ":id"...)
			} else {
				parts = append(parts, seg...)
			}
			i = j
		} else {
			parts = append(parts, p[i])
			i++
		}
	}
	return string(parts)
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
