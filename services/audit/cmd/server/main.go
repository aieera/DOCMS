// Package main boots the SeDoc audit service (append-only hash-chained log).
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

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"
	sedocv1 "github.com/aieera/sedoc/proto/gen/go/sedoc/v1"
	"github.com/aieera/sedoc/services/audit/internal/handler"
	"github.com/aieera/sedoc/services/audit/internal/repository"
	"github.com/aieera/sedoc/services/audit/internal/service"
)

const serviceName = "audit"

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

	// ADR 0095 — license validation. Init reads SEDOC_LICENSE_JWT (or
	// /etc/vaultdms/license.jwt), verifies the RS256 signature against the
	// public key bundled in pkg/license/dev_pubkey.go, and caches the parsed
	// claims for license.Current() readers. Absent license is OK
	// (unlicensed_dev_mode); a present-but-invalid license is fatal, and
	// SEDOC_REQUIRE_LICENSE=true escalates absence to fatal too.
	if err := license.Init(); err != nil {
		log.Fatal(ctx).Err(err).Msg("license init")
	}
	license.StartReloader(ctx)

	pool, err := database.NewPool(ctx, cfg.DatabaseURL, database.DefaultPoolConfig())
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("postgres connect")
	}
	defer pool.Close()
	// FIX-7: audit_events has FORCE ROW LEVEL SECURITY with an
	// INSERT WITH CHECK keyed on app.current_tenant. If the
	// connection role bypasses RLS we'd silently miss the WITH CHECK
	// gap; if it doesn't bypass and the GUC isn't set, every Insert

	rdb := redis.NewClient(&redis.Options{Addr: cfg.RedisURL, Password: cfg.RedisPassword, DB: cfg.RedisDB})
	defer func() { _ = rdb.Close() }()

	nc, js, err := events.ConnectNATS(cfg.NATSURL)
	if err != nil {
		log.Fatal(ctx).Err(err).Msg("nats connect")
	}
	defer nc.Close()

	repo := repository.New(pool)
	svc := service.New(service.Config{Repo: repo, Redis: rdb, Logger: *log.Z()})

	if err := svc.StartConsumer(ctx, js); err != nil {
		log.Fatal(ctx).Err(err).Msg("start consumer")
	}

	hs := health.NewServerWithMeta("audit", cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	grpcSrv := grpc.NewServer(grpc.ChainUnaryInterceptor(
		middleware.RecoveryInterceptor(log),
		middleware.CorrelationInterceptor(),
		middleware.TenantInterceptor(pool),
		middleware.UserIdentityInterceptor(),
		middleware.RequestLogInterceptor(log),
	))
	// Register the AuditService gRPC server. Without this the server served no
	// methods, so the graphql-gateway's per-document Activity feed (Audit.Query)
	// always returned empty.
	sedocv1.RegisterAuditServiceServer(grpcSrv, handler.NewGRPCHandler(svc))
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

	mux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(mux)
	// §3.1 / B2.3 — reject unsigned traffic. See pkg/middleware/gatewaysig.go.
	// FIX-1 follow-up: Kong now strips X-Auth-Tenant-ID + X-User-*
	// at the edge, so audit handlers must read identity from the
	// SessionAuth-populated ctx instead of headers. SessionAuthOptional
	// lets sessionless paths (none today on audit) keep working.
	httpSrv := &http.Server{Addr: fmt.Sprintf(":%d", cfg.HTTPPort), Handler: middleware.RequireGatewaySignature()(middleware.SessionAuthOptional(middleware.SessionAuthConfig{Pool: pool})(mux)), ReadHeaderTimeout: 5 * time.Second}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg("http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	grpcSrv.GracefulStop()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
}
