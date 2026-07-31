// Package main boots the SeDoc workflow service (Temporal-backed).
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"google.golang.org/grpc"

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	"github.com/aieera/sedoc/services/workflow/internal/activities"
	"github.com/aieera/sedoc/services/workflow/internal/handler"
	"github.com/aieera/sedoc/services/workflow/internal/repository"
	"github.com/aieera/sedoc/services/workflow/internal/service"
	"github.com/aieera/sedoc/services/workflow/internal/workflows"
)

const serviceName = "workflow"
const taskQueue = "vaultdms-workflow"

var version = "dev"

func main() {
	cfg, err := config.Load(serviceName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config load: %v\n", err)
		os.Exit(1)
	}
	cfg.ServiceVersion = version

	log := logger.New(serviceName, cfg.ServiceVersion, cfg.LogLevel)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// License (ADR 0095): load + hourly re-validate so the ipaas feature gate +
	// grace write-gate below have state. Absent = unlicensed-dev (gates no-op);
	// invalid JWT is fatal.
	if err := license.Init(); err != nil {
		log.Fatal(ctx).Err(err).Msg("license init")
	}
	license.StartReloader(ctx)

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()
	// FIX-7 follow-up: RLS posture gate. Refuses to start when the
	// connection role unexpectedly has BYPASSRLS; set
	// SEDOC_ALLOW_BYPASS_RLS=1 in dev to opt in.

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	// ---- Temporal ----------------------------------------------------------
	tc, err := client.Dial(client.Options{HostPort: cfg.TemporalAddr})
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("temporal connect")
	}
	defer tc.Close()

	// Register Temporal worker with workflows + activities.
	acts := &activities.Activities{
		Pool:   pool,
		Outbox: database.NewOutboxRepository(),
		Redis:  rdb,
		// Wave 12.4: cross-service erase targets. Empty = soft no-op.
		// "signature" (ADR 0025 Wave 9): the SignatureWorkflow's seal activity.
		ServiceURLs: map[string]string{
			"search":    os.Getenv("SEDOC_SEARCH_URL"),
			"qdrant":    os.Getenv("SEDOC_QDRANT_URL"),
			"connector": os.Getenv("SEDOC_CONNECTOR_URL"),
			"signature": os.Getenv("SEDOC_SIGNATURE_URL"),
		},
		InternalKey: os.Getenv("SEDOC_INTERNAL_API_KEY"),
		Log:         *log.Z(),
	}
	registerAll := func(w worker.Worker) {
		w.RegisterWorkflow(workflows.ApprovalWorkflow)
		w.RegisterWorkflow(workflows.ParallelApprovalWorkflow)
		w.RegisterWorkflow(workflows.ReviewWorkflow)
		w.RegisterWorkflow(workflows.RetentionWorkflow)
		w.RegisterWorkflow(workflows.SignatureWorkflow)
		w.RegisterWorkflow(workflows.ExportWorkflow)
		w.RegisterWorkflow(workflows.EraseWorkflow)
		w.RegisterWorkflow(workflows.AnonymizeWorkflow)
		w.RegisterWorkflow(workflows.ResidencyMigrationWorkflow)
		w.RegisterActivity(acts)
	}
	w := worker.New(tc, taskQueue, worker.Options{})
	registerAll(w)
	go func() {
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Error(ctx).Err(err).Msg("temporal worker")
		}
	}()
	// Second worker on the cross-service dispatch queue. The document
	// service (DSR privacy requests, residency migrations) and the
	// retention scheduler start workflows on `vaultdms-default` — the
	// queue the standalone cmd/worker binary owns in production. The
	// compose stack doesn't run that binary, so without this worker
	// those workflows sat unclaimed forever (privacy requests pending
	// for a month). Registering both queues here makes the inline
	// dev worker cover everything cmd/worker would.
	wDefault := worker.New(tc, "vaultdms-default", worker.Options{})
	registerAll(wDefault)
	go func() {
		if err := wDefault.Run(worker.InterruptCh()); err != nil {
			log.Error(ctx).Err(err).Msg("temporal worker (vaultdms-default)")
		}
	}()

	// ---- Service layer -----------------------------------------------------
	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Temporal: tc, Logger: *log.Z()})

	// ---- Health ------------------------------------------------------------
	hs := health.NewServerWithMeta("workflow", cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC --------------------------------------------------------------
	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	grpcLis, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.GRPCPort))
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("grpc listen")
	}
	go func() {
		log.Info(ctx).Int("port", cfg.GRPCPort).Msg("grpc listening")
		if err := grpcSrv.Serve(grpcLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
			log.Error(ctx).Err(err).Msg("grpc serve")
		}
	}()

	// ---- HTTP REST ---------------------------------------------------------
	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)
	// ADR 0064 — tenant-wide delegations + recall.
	h.RegisterRoutingPatterns(mux)
	// ADR 0090 — iPaaS trigger endpoint (Zapier / Make / n8n). API-key
	// authenticated; the /api/v1/integrations/triggers/ prefix is
	// whitelisted by RequireGatewaySignature.
	integrationsMux := http.NewServeMux()
	handler.NewIntegrationTriggersHandler(pool).Register(integrationsMux)
	// Per-tenant rate limit (ADR 0090) — 60/min/tenant, nested INSIDE
	// APIKeyAuth so the key's tenant is on ctx. See document main.go.
	integrationsRL := middleware.NewRateLimiter(rdb, 60)
	mux.Handle("/api/v1/integrations/triggers/workflows/completed",
		middleware.APIKeyAuth(middleware.APIKeyAuthConfig{
			Pool:          pool,
			RequiredScope: "integrations:read",
		})(middleware.RateLimitPerTenantHTTP(integrationsRL, "integrations")(
			middleware.RequireLicenseFeature("ipaas")(integrationsMux))))
	// License grace write-gate (ADR 0095): mutating requests → 423 in grace/
	// expired; reads pass. No-op when active/unlicensed-dev.
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(middleware.IdentityHeadersHTTP()(middleware.LicenseWriteGate(mux))), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher --------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	w.Stop()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}
