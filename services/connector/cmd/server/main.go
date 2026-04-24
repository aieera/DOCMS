// Package main boots the VaultDMS connector service — webhooks, MCP server,
// and OAuth connectors for external systems.
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
	"google.golang.org/grpc"

	"github.com/vaultdms/vaultdms/pkg/config"
	"github.com/vaultdms/vaultdms/pkg/database"
	"github.com/vaultdms/vaultdms/pkg/events"
	"github.com/vaultdms/vaultdms/pkg/health"
	"github.com/vaultdms/vaultdms/pkg/logger"
	"github.com/vaultdms/vaultdms/pkg/middleware"
	"github.com/vaultdms/vaultdms/services/connector/internal/handler"
	"github.com/vaultdms/vaultdms/services/connector/internal/mcp"
	"github.com/vaultdms/vaultdms/services/connector/internal/providers/google"
	"github.com/vaultdms/vaultdms/services/connector/internal/providers/microsoft"
	"github.com/vaultdms/vaultdms/services/connector/internal/providers/salesforce"
	"github.com/vaultdms/vaultdms/services/connector/internal/repository"
	"github.com/vaultdms/vaultdms/services/connector/internal/service"
	"github.com/vaultdms/vaultdms/services/connector/internal/webhook"
)

const serviceName = "connector"

var version = "dev"

// envOr returns the env value for `key`, falling back to `fallback`
// when unset. Used for OAuth provider config that has sensible
// defaults (e.g. M365 "common" tenant, Salesforce login.salesforce.com).
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

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

	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Logger: *log.Z()})

	// OAuth provider registry. Each provider is built from env vars
	// the operator sets per tenant deployment; an empty client_id
	// still registers the provider so auth-url calls return a
	// misconfig error rather than "unknown provider".
	registry := service.ProviderRegistry{
		"microsoft365": microsoft.New(
			os.Getenv("M365_CLIENT_ID"),
			os.Getenv("M365_CLIENT_SECRET"),
			envOr("M365_TENANT_ID", "common"),
			*log.Z(),
		),
		"salesforce": salesforce.New(
			os.Getenv("SALESFORCE_CLIENT_ID"),
			os.Getenv("SALESFORCE_CLIENT_SECRET"),
			envOr("SALESFORCE_INSTANCE_URL", "https://login.salesforce.com"),
			*log.Z(),
		),
		"google_workspace": google.New(
			os.Getenv("GOOGLE_CLIENT_ID"),
			os.Getenv("GOOGLE_CLIENT_SECRET"),
			*log.Z(),
		),
	}
	svc.AttachOAuth(rdb, registry)

	// Start NATS event fanout → webhook deliveries.
	if err := svc.StartEventFanout(ctx, js); err != nil {
		log.Fatal(ctx).Err(err).Msg("event fanout")
	}

	// Start webhook delivery worker.
	deliveryWorker := webhook.NewDeliveryWorker(repo, *log.Z())
	go deliveryWorker.Start(ctx)
	defer deliveryWorker.Stop()

	// MCP server.
	mcpSrv := mcp.NewServer(*log.Z())

	// ---- Health ----------------------------------------------------------
	hs := health.NewServer(pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- gRPC ------------------------------------------------------------
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

	// ---- HTTP REST -------------------------------------------------------
	mux := http.NewServeMux()
	h := handler.New(svc, mcpSrv, *log.Z())
	h.Register(mux)
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(mux), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox ----------------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}
