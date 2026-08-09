// Package main boots the SeDoc task service (task-assignment API).
package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/aieera/sedoc/pkg/config"
	"github.com/aieera/sedoc/pkg/database"
	"github.com/aieera/sedoc/pkg/events"
	"github.com/aieera/sedoc/pkg/health"
	"github.com/aieera/sedoc/pkg/license"
	"github.com/aieera/sedoc/pkg/logger"
	"github.com/aieera/sedoc/pkg/middleware"

	"github.com/aieera/sedoc/services/task/internal/handler"
	"github.com/aieera/sedoc/services/task/internal/service"
)

const serviceName = "task"

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

	// ---- Dependencies -------------------------------------------------------
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

	// ---- Service --------------------------------------------------------------
	svc := service.New(pool, *log.Z())

	// ---- Health ---------------------------------------------------------------
	hs := health.NewServerWithMeta(serviceName, cfg.Region, pool, rdb, nc, nil)
	go func() {
		if err := hs.Start(fmt.Sprintf(":%d", cfg.HealthPort)); err != nil {
			log.Error(ctx).Err(err).Msg("health server")
		}
	}()

	// ---- HTTP (session-authenticated task API, no gRPC surface) ---------------
	httpMux := http.NewServeMux()
	h := handler.New(svc, *log.Z())
	h.Register(httpMux)
	var httpRoot http.Handler = httpMux
	httpRoot = middleware.SessionAuth(middleware.SessionAuthConfig{Pool: pool})(httpRoot)
	// BUG-08 — response security headers, wrapped outermost so they also
	// land on the 401/403/429 responses written by the middleware below.
	secHeaders := middleware.SecurityHeaders(middleware.SecurityHeadersFromConfig(cfg))
	httpSrv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler:           secHeaders(middleware.RequireGatewaySignature()(httpRoot)),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Info(ctx).Int("port", cfg.HTTPPort).Msg(serviceName + " http listening")
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error(ctx).Err(err).Msg("http serve")
		}
	}()

	// ---- Outbox publisher -------------------------------------------------------
	outbox := database.NewOutboxPublisher(pool, js, serviceName, *log.Z())
	go outbox.Start(ctx)

	// ---- Notification sweep -----------------------------------------------------
	// ADR 0068 — hourly sweep. Stamps reminded_at / overdue_notified_at on
	// tasks crossing the 24h-out and overdue thresholds; emits one notify
	// event per claimed row. UPDATE…RETURNING makes the claim + emit pair
	// effectively idempotent (no double-fire on the next tick). Mirrors the
	// document service's equivalent block (cmd/server/main.go:1080-1100).
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		// Run once on boot so a deploy doesn't wait an hour to send the
		// first reminder after a cold start.
		_ = svc.SweepTaskNotifications(ctx)
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := svc.SweepTaskNotifications(ctx); err != nil {
					log.Warn(ctx).Err(err).Msg("task notification sweep failed")
				}
			}
		}
	}()

	log.Info(ctx).Str("version", version).Msg(serviceName + " started")
	<-ctx.Done()
	log.Info(context.Background()).Msg(serviceName + " shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	_ = hs.Shutdown(shutdownCtx)
	outbox.Stop()
}
