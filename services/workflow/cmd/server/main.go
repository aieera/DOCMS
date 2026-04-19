// Package main boots the VaultDMS workflow service (Temporal-backed).
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

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/workflow/internal/activities"
	"github.com/vaultdms/vaultdms/services/workflow/internal/handler"
	"github.com/vaultdms/vaultdms/services/workflow/internal/repository"
	"github.com/vaultdms/vaultdms/services/workflow/internal/service"
	"github.com/vaultdms/vaultdms/services/workflow/internal/workflows"
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

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()

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
		ServiceURLs: map[string]string{
			"search":    os.Getenv("VAULTDMS_SEARCH_URL"),
			"qdrant":    os.Getenv("VAULTDMS_QDRANT_URL"),
			"connector": os.Getenv("VAULTDMS_CONNECTOR_URL"),
		},
		Log: *log.Z(),
	}
	w := worker.New(tc, taskQueue, worker.Options{})
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
	go func() {
		if err := w.Run(worker.InterruptCh()); err != nil {
			log.Error(ctx).Err(err).Msg("temporal worker")
		}
	}()

	// ---- Service layer -----------------------------------------------------
	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Temporal: tc, Logger: *log.Z()})

	// ---- Health ------------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, nil)
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
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: mux, ReadHeaderTimeout: 5 * time.Second}
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
