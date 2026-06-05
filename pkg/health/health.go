// Package health serves /healthz, /readyz, and /metrics on a dedicated port,
// so liveness / readiness probing is isolated from application traffic.
package health

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/redis/go-redis/v9"
	"github.com/aieera/sedoc/pkg/storage"
)

// Server bundles the dependency handles required by /readyz.
//
// ADR 0110 — `service` + `region` are echoed in /healthz so the
// load-balancer health-check + the frontend residency banner can
// confirm which cluster they're talking to. Both are populated from
// the shared Config struct at boot; if the operator forgot to set
// SEDOC_REGION_ID the value is empty and /healthz reports
// `"region":"unknown"` — that's an actionable signal, not a silent
// failure.
type Server struct {
	pg      *pgxpool.Pool
	rdb     *redis.Client
	nats    *nats.Conn
	s3      *storage.S3Client
	mu      sync.Mutex
	http    *http.Server
	service string
	region  string
	// extra holds service-specific routes registered via Handle before
	// Start — e.g. the storage service's internal admin re-encrypt endpoint.
	// They share the health port (already exposed for probes) rather than
	// forcing a second listener.
	extra map[string]http.Handler
}

// Handle registers an additional route on the health server's mux. Must be
// called before Start. Intended for narrow internal/admin endpoints that
// shouldn't warrant a separate listener; protect them with their own auth.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.extra == nil {
		s.extra = make(map[string]http.Handler)
	}
	s.extra[pattern] = h
}

// NewServer wires the server. Any dependency may be nil; readiness reports
// "not configured" for nil deps and does not treat them as failures.
//
// Deprecated: prefer NewServerWithMeta so /healthz reports the
// cluster region. Kept for ABI compatibility with services that
// haven't migrated yet — they'll get a /healthz response with
// `"region":"unknown"` until they switch constructors.
func NewServer(pg *pgxpool.Pool, rdb *redis.Client, nc *nats.Conn, s3 *storage.S3Client) *Server {
	return &Server{pg: pg, rdb: rdb, nats: nc, s3: s3}
}

// NewServerWithMeta is the canonical constructor since ADR 0110.
// `service` is the short name ("document", "auth", …) and `region`
// is the cluster region pulled from Config.Region (env
// SEDOC_REGION_ID).
func NewServerWithMeta(service, region string, pg *pgxpool.Pool, rdb *redis.Client, nc *nats.Conn, s3 *storage.S3Client) *Server {
	return &Server{pg: pg, rdb: rdb, nats: nc, s3: s3, service: service, region: region}
}

// Start listens on addr (e.g. ":8081"). Blocks until the server stops.
func (s *Server) Start(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleLive)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", promhttp.Handler())

	s.mu.Lock()
	for pattern, h := range s.extra {
		mux.Handle(pattern, h)
	}
	s.http = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	s.mu.Unlock()

	if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

// Shutdown gracefully stops the server.
func (s *Server) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	srv := s.http
	s.mu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (s *Server) handleLive(w http.ResponseWriter, _ *http.Request) {
	region := s.region
	if region == "" {
		region = "unknown"
	}
	service := s.service
	if service == "" {
		service = "unknown"
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "alive",
		"service": service,
		"region":  region,
	})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	out := map[string]string{}
	ok := true

	if s.pg != nil {
		if err := s.pg.Ping(ctx); err != nil {
			out["postgres"] = "fail: " + err.Error()
			ok = false
		} else {
			out["postgres"] = "ok"
		}
	}
	if s.rdb != nil {
		if err := s.rdb.Ping(ctx).Err(); err != nil {
			out["redis"] = "fail: " + err.Error()
			ok = false
		} else {
			out["redis"] = "ok"
		}
	}
	if s.nats != nil {
		if s.nats.Status() != nats.CONNECTED {
			out["nats"] = "fail: " + s.nats.Status().String()
			ok = false
		} else {
			out["nats"] = "ok"
		}
	}
	if s.s3 != nil {
		if err := s.s3.Ping(ctx, "health-check"); err != nil {
			out["s3"] = "fail: " + err.Error()
			ok = false
		} else {
			out["s3"] = "ok"
		}
	}

	code := http.StatusOK
	if !ok {
		code = http.StatusServiceUnavailable
	}
	writeJSON(w, code, out)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
