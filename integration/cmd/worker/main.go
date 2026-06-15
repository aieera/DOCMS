// Command worker is the ERP→SeDoc integration worker [A]: it serves the ERP
// webhook ingress and drains the DB-backed job queue, provisioning folders and
// creating/versioning documents in SeDoc. Holds the SeDoc API key; never writes
// back to the ERP.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"

	"github.com/aieera/sedoc/integration/internal/backfill"
	"github.com/aieera/sedoc/integration/internal/erp"
	"github.com/aieera/sedoc/integration/internal/sedoc"
	"github.com/aieera/sedoc/integration/internal/store"
	syncpkg "github.com/aieera/sedoc/integration/internal/sync"
	"github.com/aieera/sedoc/pkg/database"
)

func main() {
	log := zerolog.New(os.Stderr).With().Timestamp().Str("svc", "integration-worker").Logger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	dsn := env("SEDOC_INTEGRATION_DB_URL", "")
	if dsn == "" {
		log.Fatal().Msg("SEDOC_INTEGRATION_DB_URL is required")
	}
	baseURL := env("SEDOC_BASE_URL", "http://localhost:8081/api/v1")
	apiKey := env("SEDOC_API_KEY", "")
	workspaceID := env("SEDOC_WORKSPACE_ID", "")
	if apiKey == "" || workspaceID == "" {
		log.Fatal().Msg("SEDOC_API_KEY and SEDOC_WORKSPACE_ID are required")
	}
	erpBase := env("ERP_BASE_URL", "http://localhost:8095")
	buckets := envInt("SEDOC_INTEGRATION_BUCKETS", 256)
	port := envInt("SEDOC_INTEGRATION_HTTP_PORT", 8090)
	concurrency := envInt("SEDOC_INTEGRATION_CONCURRENCY", 8)
	// Proactive throttle: stay under SeDoc's 600/min :upsert/ingest ceiling
	// (10/s) with a small burst, so neither the event worker nor the backfill can
	// avalanche into 429s/DLQ. A SINGLE limiter is shared by every SeDoc client.
	ratePerMin := envInt("SEDOC_INTEGRATION_RATE_PER_MIN", 600)
	rateBurst := envInt("SEDOC_INTEGRATION_RATE_BURST", 20)

	if err := database.RunMigrations(dsn, "migrations"); err != nil {
		log.Fatal().Err(err).Msg("migrate")
	}
	// The integration owns a single-tenant DB (no tenant RLS), so the SeDoc
	// multi-tenant posture gate doesn't apply here.
	poolCfg := database.DefaultPoolConfig()
	poolCfg.SkipRLSPostureCheck = true
	pool, err := database.NewPool(ctx, dsn, poolCfg)
	if err != nil {
		log.Fatal().Err(err).Msg("db pool")
	}
	defer pool.Close()

	st := store.New(pool)
	limiter := sedoc.NewLimiter(float64(ratePerMin)/60.0, rateBurst)
	doc := sedoc.New(baseURL, apiKey).WithLimiter(limiter).WithWorkspace(workspaceID).
		WithS3DialHost(env("SEDOC_S3_DIAL_HOST", ""))
	src := erp.NewHTTPClient(erpBase).WithToken(env("ERP_API_TOKEN", ""))
	syncer := syncpkg.New(st, doc, src, syncpkg.Config{
		WorkspaceID: workspaceID, RegionPin: env("SEDOC_REGION_PIN", "us-east-1"), Buckets: buckets,
	})
	metrics := syncpkg.NewMetrics()
	worker := syncpkg.NewWorker(st, syncer, log, syncpkg.WorkerOptions{Concurrency: concurrency, Metrics: metrics})
	log.Info().Int("concurrency", concurrency).Int("rate_per_min", ratePerMin).Int("rate_burst", rateBurst).
		Msg("SeDoc write throttle engaged")
	go worker.Run(ctx)

	// Backfill poller (worker mode): claims pending backfill_runs (BFF-triggered)
	// and processes them in-process, sharing the same rate-limited SeDoc client.
	backfillConcurrency := envInt("SEDOC_INTEGRATION_BACKFILL_CONCURRENCY", 4)
	go backfill.NewRunner(st, syncer, src, backfillConcurrency, log).PollLoop(ctx, 5*time.Second)

	mux := http.NewServeMux()
	syncpkg.NewIngress(st, log).Register(mux)
	// Operational metrics for the Sync Dashboard (proxied by the BFF, admin-gated):
	// throughput + backlog + DLQ + shared-limiter state.
	mux.HandleFunc("GET /metrics/sync", syncpkg.MetricsHandler(metrics, limiter, st))
	srv := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info().Int("port", port).Msg("integration worker listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error().Err(err).Msg("http serve")
		}
	}()

	<-ctx.Done()
	log.Info().Msg("shutting down")
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(sctx)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}
